package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
)

// seekRecorder 记下每次 Seek 的目标，用来验证异步复核到底补发了几次。
type seekRecorder struct {
	mu     sync.Mutex
	target []int
}

func (r *seekRecorder) seeks() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.target...)
}
func (r *seekRecorder) Seek(ctx context.Context, ms int) error {
	r.mu.Lock()
	r.target = append(r.target, ms)
	r.mu.Unlock()
	return nil
}
func (r *seekRecorder) Play(ctx context.Context, url string, t netease.Track) error { return nil }
func (r *seekRecorder) Pause(ctx context.Context) error                             { return nil }
func (r *seekRecorder) Resume(ctx context.Context) error                            { return nil }
func (r *seekRecorder) Next(ctx context.Context) error                              { return nil }
func (r *seekRecorder) Prev(ctx context.Context) error                              { return nil }
func (r *seekRecorder) Stop(ctx context.Context) error                              { return nil }
func (r *seekRecorder) Volume(ctx context.Context, v int) error                     { return nil }
func (r *seekRecorder) Describe() string                                            { return "seek-recorder" }

func newPushSession(t *testing.T) (*Session, *seekRecorder) {
	t.Helper()
	rec := &seekRecorder{}
	q := queue.NewQueue(queue.LoopList)
	q.Replace([]netease.Track{
		{ID: 1, Name: "甲", Duration: 200000},
		{ID: 2, Name: "乙", Duration: 200000},
	}, "test", 0)
	q.MarkPlaying()
	q.SyncPosition(10000, true)
	s := &Session{
		app: &App{}, // view() 会走 EnsureLiked，需要非 nil App（ne==nil 时它自己跳过）
		speaker: config.SpeakerConfig{ID: "living", Name: "test", Control: "ssh",
			StatusMode: "push", StatusPoll: -1},
		player:     rec,
		q:          q,
		probeEvery: 60 * time.Second,
		pushTim:    pushTimingFromConfig(config.SpeakerConfig{StatusMode: "push"}),
		windowMode: true,
		pushedTo:   -1,
		winFrom:    -1,
		probe:      nil, // 推送模式：没有拉取通道
	}
	return s, rec
}

// 静默超过阈值 → 判离线、位置冻结；恢复上报 → 解除。
func TestPushLivenessFreeze(t *testing.T) {
	s, _ := newPushSession(t)

	// 刚收到一帧：在线
	s.updateDeviceStatus(player.DeviceStatus{Status: 1, Position: 12000, AudioID: "1"})
	if s.unreachable.Load() {
		t.Fatal("刚收到上报不该判离线")
	}
	if off, _ := s.unreachableView(); off {
		t.Fatal("unreachableView 不该报离线")
	}

	// 把"上次上报"推到阈值之外
	s.lastPushAt.Store(time.Now().Add(-s.pushTiming().offline - time.Second).Unix())
	s.checkPushLiveness()
	if !s.unreachable.Load() {
		t.Fatal("静默超阈值应判离线")
	}
	off, frozen := s.unreachableView()
	if !off || frozen <= 0 {
		t.Fatalf("应给出冻结位置，实际 off=%v frozen=%d", off, frozen)
	}

	// 对外视图：playing 归零 + Unreachable + 位置是冻结值（不是继续插值的值）
	v := s.view(0)
	if !v.Unreachable || v.Playing {
		t.Fatalf("离线时视图应 Unreachable=true / Playing=false，实际 %+v", v)
	}
	if v.PositionMs != frozen {
		t.Fatalf("视图位置应是冻结值 %d，实际 %d", frozen, v.PositionMs)
	}
	// 内部真值不受显示层影响：队列本身仍是播放态，恢复后不用重建
	if s.q.View(0).Playing != true {
		t.Fatal("冻结只该影响对外视图，不该改队列真值")
	}
	// 设备调试页要把这份回读标成"最后一次已知"，否则同屏出现两个矛盾事实
	if dv := s.deviceView(); dv["stale"] != true {
		t.Fatalf("离线时 deviceView 应带 stale=true，实际 %v", dv["stale"])
	}

	// 再来一帧：解除
	s.updateDeviceStatus(player.DeviceStatus{Status: 1, Position: 30000, AudioID: "1"})
	if s.unreachable.Load() {
		t.Fatal("恢复上报后应解除离线")
	}
	if s.view(0).Unreachable {
		t.Fatal("恢复后视图不该再带 Unreachable")
	}
	// 在线时不该再有 stale（键缺失，不是 false——前端按存在性判断）
	if dv := s.deviceView(); dv["stale"] != nil {
		t.Fatalf("在线时 deviceView 不该有 stale：%v", dv["stale"])
	}
}

// 还没收到过任何一帧（刚启动）不该被判离线。
func TestPushLivenessNotArmedBeforeFirstFrame(t *testing.T) {
	s, _ := newPushSession(t)
	s.checkPushLiveness()
	if s.unreachable.Load() {
		t.Fatal("启动阶段没收到上报不等于离线")
	}
}

// seek 之后：真值到位就静默确认；没到位补发；补发够了就放弃并锚回真值。
func TestPendingSeekVerify(t *testing.T) {
	s, rec := newPushSession(t)

	// 下发一次 seek（probe==nil → 登记异步复核）
	if err := s.Seek(context.Background(), 60000); err != nil {
		t.Fatalf("Seek 应返回已下发：%v", err)
	}
	if len(rec.seeks()) != 1 {
		t.Fatalf("应下发 1 次 seek，实际 %d", len(rec.seeks()))
	}
	s.seekMu.Lock()
	ps := s.pendSeek
	s.seekMu.Unlock()
	if ps == nil || ps.targetMs != 60000 {
		t.Fatalf("应登记待校验的 seek，实际 %+v", ps)
	}

	// 第一帧真值就在目标附近 → 确认，不补发
	s.verifyPendingSeek(context.Background(), 1, 61000)
	if n := len(rec.seeks()); n != 1 {
		t.Fatalf("已生效不该补发，实际 seek 次数 %d", n)
	}
	s.seekMu.Lock()
	if s.pendSeek != nil {
		t.Fatal("确认后应清空登记")
	}
	s.seekMu.Unlock()

	// 再登记一次，这次设备没理我们 → 补发
	if err := s.Seek(context.Background(), 120000); err != nil {
		t.Fatal(err)
	}
	s.verifyPendingSeek(context.Background(), 1, 20000) // 还在原处
	if n := len(rec.seeks()); n != 3 {
		t.Fatalf("没到位应补发一次（共 3 次），实际 %d", n)
	}

	// 换歌了：目标已不存在，直接放弃，不补发
	before := len(rec.seeks())
	s.verifyPendingSeek(context.Background(), 2, 5000)
	if len(rec.seeks()) != before {
		t.Fatal("换歌后不该再补发旧 seek")
	}
	s.seekMu.Lock()
	if s.pendSeek != nil {
		t.Fatal("换歌后应清空登记")
	}
	s.seekMu.Unlock()
}

// 容差必须覆盖一个采样周期：设备 seek 成功后只会更靠前，但 daemon 报的是上一拍的值。
func TestPendingSeekToleranceCoversSample(t *testing.T) {
	s, rec := newPushSession(t)
	if err := s.Seek(context.Background(), 60000); err != nil {
		t.Fatal(err)
	}
	// 落后正好一个采样间隔（5s）+ 1s：按设计应算"已生效"
	s.verifyPendingSeek(context.Background(), 1, 60000-6000)
	if len(rec.seeks()) != 1 {
		t.Fatalf("一个采样周期内的滞后不该被当成失败，实际 seek 次数 %d", len(rec.seeks()))
	}
}

// 绕回判定的位置窗口要跟着采样间隔放宽，否则 repeat=none 的"播完即停"会漏。
func TestWrapPosWindow(t *testing.T) {
	s, _ := newPushSession(t)
	if got := s.wrapPosWindow(); got != 7000 {
		t.Fatalf("push 模式（采样 5s）窗口应为 7000ms，实际 %d", got)
	}
	s.speaker.StatusMode = "ssh"
	if got := s.wrapPosWindow(); got != 3000 {
		t.Fatalf("拉取模式应维持 3000ms，实际 %d", got)
	}
}

// 阈值由 daemon 心跳推出来：改了心跳，静默阈值必须跟着变。
func TestPushTimingDerived(t *testing.T) {
	def := pushTimingFromConfig(config.SpeakerConfig{})
	if def.sample != 5*time.Second || def.hbPlay != 15*time.Second || def.hbIdle != 30*time.Second {
		t.Fatalf("默认节奏不对：%+v", def)
	}
	if def.offline != 60*time.Second {
		t.Fatalf("默认静默阈值应为 60s（max(3×15, 2×30)），实际 %s", def.offline)
	}
	// 心跳拉长 → 阈值也要拉长，否则会误判离线
	long := pushTimingFromConfig(config.SpeakerConfig{ReportHBPlay: 60, ReportHBIdle: 120})
	if long.offline != 240*time.Second {
		t.Fatalf("hb 60/120 时阈值应为 240s，实际 %s", long.offline)
	}
}

// /api/state 在离线时要带顶层 unreachable，且 queue.unreachable 同步。
func TestStatePayloadUnreachable(t *testing.T) {
	s, _ := newPushSession(t)
	s.updateDeviceStatus(player.DeviceStatus{Status: 1, Position: 12000, AudioID: "1"})
	s.lastPushAt.Store(time.Now().Add(-time.Minute).Unix())
	s.checkPushLiveness()

	a := &App{sessions: map[string]*Session{"living": s}, order: []string{"living"}}
	s.app = a
	// 设备块在数据过期后会消失，顶层 unreachable 必须还在（HA 的 available 靠它）
	out := s.statePayload(context.Background(), false)
	if out["unreachable"] != true {
		t.Fatalf("离线时顶层应带 unreachable=true，实际 %v", out["unreachable"])
	}
	if _, ok := out["device"]; ok {
		t.Fatal("数据过期后 device 块应消失")
	}
	qv, _ := out["queue"].(queue.QueueView)
	if !qv.Unreachable {
		t.Fatal("queue 视图应带 Unreachable")
	}

	// 签名必须因为离线而改变，否则 SSE 不会推这一帧，HA 永远不知道
	s2, _ := newPushSession(t)
	s2.updateDeviceStatus(player.DeviceStatus{Status: 1, Position: 12000, AudioID: "1"})
	if s.stateSig() == s2.stateSig() {
		t.Fatal("在线/离线的签名不该相同，否则 HA 收不到 available=false")
	}
}

// 设备调试页的"重新回读"在推送模式下要说清来源，不能假装是服务端主动拉的；
// 而且"等下一帧"必须真的在新数据到达时立刻返回，不是死等一个完整超时。
func TestDeviceFreshNote(t *testing.T) {
	s, _ := newPushSession(t)
	a := &App{sessions: map[string]*Session{"living": s}, order: []string{"living"}}
	s.app = a
	h := a.Routes()

	// 300ms 后送一帧，模拟 daemon 上报到达
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.updateDeviceStatus(player.DeviceStatus{Status: 1, Position: 43210, AudioID: "1"})
	}()

	t0 := time.Now()
	req := httptest.NewRequest("GET", "/api/device?speaker=living&fresh=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	waited := time.Since(t0)

	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("应 200，实际 %d %s", rec.Code, body)
	}
	if !strings.Contains(body, "推送模式") {
		t.Fatalf("推送模式应说明回读来源，实际 %s", body)
	}
	if !strings.Contains(body, "43210") {
		t.Fatalf("等到的那帧应体现在响应里，实际 %s", body)
	}
	if waited > 2*time.Second {
		t.Fatalf("新数据到达后应立刻返回，实际等了 %s", waited)
	}
}

// 没人上报时也必须**有界**返回（不能把 HTTP 请求挂死在 daemon 上）。
func TestDeviceFreshNoteBounded(t *testing.T) {
	s, _ := newPushSession(t)
	s.speaker.ReportInterval = 1 // 采样 1s → 最多等 1+2 秒
	s.pushTim = pushTimingFromConfig(s.speaker)
	a := &App{sessions: map[string]*Session{"living": s}, order: []string{"living"}}
	s.app = a

	t0 := time.Now()
	req := httptest.NewRequest("GET", "/api/device?speaker=living&fresh=1", nil)
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, req)
	if waited := time.Since(t0); waited > 5*time.Second {
		t.Fatalf("无上报时应有界返回，实际挂了 %s", waited)
	}
	if rec.Code != 200 {
		t.Fatalf("等不到也应 200（带旧数据），实际 %d %s", rec.Code, rec.Body.String())
	}
}
