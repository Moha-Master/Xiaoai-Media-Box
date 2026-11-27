package app

import (
	"context"
	"strconv"
	"testing"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
)

// trialInfoMap 构造一个批量直链测试钩子：108914 是副歌切片（69s–92s，片段 22752ms）。
func trialInfoMap(ids []int64) (map[int64]netease.PlayInfo, error) {
	m := map[int64]netease.PlayInfo{}
	for _, id := range ids {
		if id == 108914 {
			m[id] = netease.PlayInfo{
				URL: "http://cdn/108914", Expi: 1200,
				ClipMs: 22752, StartMs: 69000, EndMs: 92000,
				Br: 128000, Level: "standard",
			}
			continue
		}
		m[id] = netease.PlayInfo{URL: "http://cdn/" + strconv.FormatInt(id, 10), Expi: 1200, ClipMs: 200000}
	}
	return m, nil
}

// 试听曲推窗：WindowTrack.Duration 必须传**片段真实长度**（设备拿到的就是这段音频），
// 队列里的曲目要写上试听区间，Duration 保持原曲总长。
func TestPushWindowTrialUsesClipDuration(t *testing.T) {
	_, s, p := editSession(t, 3, true)
	s.q.Jump(0)
	s.app.songURLsFn = trialInfoMap
	// 把当前曲换成副歌切片
	tracks := s.q.Snapshot()
	tracks[0].ID = 108914
	tracks[0].Duration = 267946 // 《那女孩对我说》原曲总长
	s.q.Replace(tracks, "test", 0)

	if err := s.startCurrent(context.Background()); err != nil {
		t.Fatalf("推窗失败：%v", err)
	}
	if len(p.pushes) != 1 || len(p.pushes[0]) == 0 {
		t.Fatalf("应推窗：%v", p.pushes)
	}
	wt := p.pushes[0][0]
	if wt.SongID != 108914 {
		t.Fatalf("SongID=%d", wt.SongID)
	}
	if wt.Duration != 22752 {
		t.Fatalf("推窗 Duration 应为片段长度 22752，得到 %d（原曲 267946 会骗设备做完整时间轴）", wt.Duration)
	}
	cur, _, _ := s.q.Current()
	if cur.TrialStartMs != 69000 || cur.TrialEndMs != 92000 {
		t.Fatalf("试听区间没写进队列曲目：%+v", cur)
	}
	if cur.Duration != 267946 {
		t.Fatalf("队列的 Duration 必须保持原曲总长，得到 %d", cur.Duration)
	}
	// 当前曲重锚定：设备在片段 0 秒 = 原曲 69 秒
	if v := s.q.View(0); v.PositionMs != 69000 {
		t.Fatalf("推窗后当前曲位置应重锚定到原曲轴 69000，得到 %d", v.PositionMs)
	}
}

// seek 区间映射：69s 副歌切片，seek 到原曲 80s → 设备收到 11s（80−69）。
func TestSeekTrialClipMapsToDeviceAxis(t *testing.T) {
	_, s, p := editSession(t, 2, true)
	s.q.Jump(0)
	tracks := s.q.Snapshot()
	tracks[0].ID = 108914
	tracks[0].Duration = 267946
	tracks[0].TrialStartMs = 69000
	tracks[0].TrialEndMs = 92000
	s.q.Replace(tracks, "test", 0)
	s.q.MarkPlaying()
	s.q.SyncPosition(69000, true) // 刚起播（原曲轴）

	if err := s.Seek(context.Background(), 80_000); err != nil {
		t.Fatalf("seek 失败：%v", err)
	}
	if len(p.seeks) != 1 || p.seeks[0] != 11_000 {
		t.Fatalf("设备应收到 11000ms（原曲 80000 − start 69000），得到 %v", p.seeks)
	}
	// 服务端状态留在原曲轴
	if v := s.q.View(0); v.PositionMs != 80_000 {
		t.Fatalf("队列位置应为原曲轴 80000，得到 %d", v.PositionMs)
	}
}

// seek 钳制：拖到片段外要钳到最近的区间端点。
func TestSeekTrialClampsIntoPlayableRange(t *testing.T) {
	_, s, p := editSession(t, 2, true)
	s.q.Jump(0)
	tracks := s.q.Snapshot()
	tracks[0].ID = 108914
	tracks[0].Duration = 267946
	tracks[0].TrialStartMs = 69000
	tracks[0].TrialEndMs = 92000
	s.q.Replace(tracks, "test", 0)
	s.q.MarkPlaying()

	// 拖到 30s（片段起点之前）→ 钳到 69s，设备收到 0
	if err := s.Seek(context.Background(), 30_000); err != nil {
		t.Fatalf("seek 失败：%v", err)
	}
	if p.seeks[len(p.seeks)-1] != 0 {
		t.Fatalf("起点之前应钳到 69s（设备 0ms），得到 %v", p.seeks)
	}
	// 拖到 3 分钟（片段终点之后）→ 钳到 end-1000=91s，设备收到 22000
	if err := s.Seek(context.Background(), 180_000); err != nil {
		t.Fatalf("seek 失败：%v", err)
	}
	if p.seeks[len(p.seeks)-1] != 22_000 {
		t.Fatalf("终点之后应钳到 91s（设备 22000ms），得到 %v", p.seeks)
	}
}

// filterPlayable 批量化：一次 resolveMany 替代 N 次逐首，且把试听区间写进返回的 Track。
func TestFilterPlayableBatchWritesTrial(t *testing.T) {
	a := &App{cfg: config.Config{PublicBase: "http://nas"}}
	a.songURLsFn = func(ids []int64) (map[int64]netease.PlayInfo, error) {
		if len(ids) != 3 {
			t.Fatalf("应一次批量请求 3 首，得到 %d", len(ids))
		}
		m, _ := trialInfoMap(ids)
		delete(m, 404) // 404 模拟无直链（版权/VIP）
		return m, nil
	}
	in := []netease.Track{
		{ID: 108914, Duration: 267946},
		{ID: 101, Duration: 200000},
		{ID: 404, Duration: 200000}, // 钩子里没有 → 不可播，应被剔除
	}
	out := a.filterPlayable(in, 0)
	if len(out) != 2 {
		t.Fatalf("应剩 2 首，得到 %d", len(out))
	}
	if out[0].ID != 108914 || out[0].TrialStartMs != 69000 || out[0].TrialEndMs != 92000 {
		t.Fatalf("试听区间应写进返回的 Track：%+v", out[0])
	}
}

// runScheduler 的「临近播完」对试听曲应在片段终点触发，而不是原曲 3'58"。
// 这里只验证判定数据源：PlayableEndMs 走的是 TrialEndMs。
func TestTrialEndProbeTriggerPoint(t *testing.T) {
	tr := netease.Track{ID: 108914, Duration: 267946, TrialStartMs: 69000, TrialEndMs: 92000}
	if end := tr.PlayableEndMs(); end != 92000 {
		t.Fatalf("临近播完判定应使用片段终点 92000，得到 %d", end)
	}
	// 位置爬到 91000 时就该触发（end-1200）
	if !(91000 >= tr.PlayableEndMs()-1200) {
		t.Fatal("91000ms 应已触发临近播完")
	}
	_ = queue.Wrap
}
