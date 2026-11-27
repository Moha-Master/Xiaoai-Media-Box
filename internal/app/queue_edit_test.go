package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
)

// editPlayer 记录设备侧收到过什么，用来断言“什么时候该重推整窗、什么时候该静音”。
type editPlayer struct {
	pushes     [][]player.WindowTrack
	plays      []int64
	resumes    int
	stops      int
	seeks      []int
	wantWindow bool
}

func (p *editPlayer) Play(ctx context.Context, url string, t netease.Track) error {
	p.plays = append(p.plays, t.ID)
	return nil
}
func (p *editPlayer) Pause(ctx context.Context) error { return nil }
func (p *editPlayer) Resume(ctx context.Context) error {
	p.resumes++
	return nil
}
func (p *editPlayer) Next(ctx context.Context) error { return nil }
func (p *editPlayer) Prev(ctx context.Context) error { return nil }
func (p *editPlayer) Stop(ctx context.Context) error {
	p.stops++
	return nil
}
func (p *editPlayer) Seek(ctx context.Context, ms int) error {
	p.seeks = append(p.seeks, ms)
	return nil
}
func (p *editPlayer) Volume(ctx context.Context, v int) error { return nil }
func (p *editPlayer) Describe() string                        { return "editPlayer" }

// PlayWindow 只有 windowMode 才会被调到；不实现它也能验证单曲路径。
func (p *editPlayer) PlayWindow(ctx context.Context, items []player.WindowTrack, start int) error {
	p.pushes = append(p.pushes, items)
	return nil
}

func editSession(t *testing.T, n int, windowMode bool) (*App, *Session, *editPlayer) {
	t.Helper()
	tracks := make([]netease.Track, n)
	for i := range tracks {
		tracks[i] = netease.Track{ID: int64(100 + i), Name: "曲" + string(rune('A'+i)), Duration: 200_000}
	}
	p := &editPlayer{wantWindow: windowMode}
	a := &App{cfg: config.Config{PublicBase: "http://nas:8088", Window: 4, QueuePersist: true}}
	// 单曲模式下 startCurrent 要解析直链：给个假解析器，测试不碰网易云
	a.urlFn = func(id int64) (string, int, error) {
		return "http://cdn/" + strconv.FormatInt(id, 10), 300, nil
	}
	q := queue.NewQueue(queue.LoopList)
	q.Replace(tracks, "test", 0)
	s := &Session{
		app:        a,
		speaker:    config.SpeakerConfig{ID: "living", Name: "客厅", Control: "noop"},
		q:          q,
		player:     p,
		windowMode: windowMode,
	}
	a.sessions = map[string]*Session{"living": s}
	a.order = []string{"living"}
	return a, s, p
}

func post(t *testing.T, h http.Handler, path string, body any) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest("POST", path, nil)
	} else {
		raw, _ := json.Marshal(body)
		r = httptest.NewRequest("POST", path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	out := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s 返回非法 JSON（%d）：%s", path, w.Code, w.Body)
	}
	return w.Code, out
}

func TestQueueRemoveWhilePlayingRepushesWindow(t *testing.T) {
	a, s, p := editSession(t, 6, true)
	s.q.Jump(2)
	s.q.MarkPlaying()
	s.q.SyncPosition(30000, true)

	code, resp := post(t, a.Routes(), "/api/queue/remove?speaker=living", map[string]any{"index": 4})
	if code != 200 {
		t.Fatalf("HTTP %d：%v", code, resp)
	}
	if len(p.pushes) != 1 {
		t.Fatalf("播放中删队列后面的歌也应重推整窗，pushes=%d", len(p.pushes))
	}
	if got := resp["index"]; got != float64(2) {
		t.Fatalf("当前下标不该变：%v", got)
	}
	if got := resp["total"]; got != float64(5) {
		t.Fatalf("总数应为 5：%v", got)
	}
	// 进度恢复：pushWindow 先归零，再 seek 回 30s（posBefore 是处理请求那一刻测的，允许毫秒误差）
	if len(p.seeks) != 1 || p.seeks[0] < 29_000 || p.seeks[0] > 31_000 {
		t.Fatalf("应 seek 回原进度，得到 %v", p.seeks)
	}
	if s.q.PositionMs() < 29000 {
		t.Fatalf("队列位置没跟上：%d", s.q.PositionMs())
	}
}

func TestQueueRemoveCurrentAdvances(t *testing.T) {
	a, s, p := editSession(t, 4, true)
	s.q.Jump(1)
	s.q.MarkPlaying()
	s.q.SyncPosition(10000, true)

	code, resp := post(t, a.Routes(), "/api/queue/remove?speaker=living", map[string]any{"index": 1})
	if code != 200 {
		t.Fatalf("HTTP %d：%v", code, resp)
	}
	if resp["current"] != true {
		t.Fatalf("应报告删掉的是当前曲：%v", resp)
	}
	cur, _, _ := s.q.Current()
	if cur.ID != 102 { // 原来的下一首顶上来
		t.Fatalf("当前曲应为 102，得到 %d", cur.ID)
	}
	if len(p.seeks) != 0 {
		t.Fatalf("换了当前曲不该 seek 回旧进度：%v", p.seeks)
	}
	// 新当前曲从 0 开始（RemoveAt 重置了计时，允许毫秒级误差）
	if s.q.PositionMs() > 50 {
		t.Fatalf("新当前曲应从 0 开始：%d", s.q.PositionMs())
	}
}

func TestQueueRemoveLastTrackWhilePlayingStops(t *testing.T) {
	a, s, p := editSession(t, 2, true)
	s.q.Jump(1) // 最后一首
	s.q.MarkPlaying()

	code, resp := post(t, a.Routes(), "/api/queue/remove?speaker=living", map[string]any{"index": 1})
	if code != 200 {
		t.Fatalf("HTTP %d：%v", code, resp)
	}
	if p.stops != 1 {
		t.Fatalf("删掉正在播的最后一首应停设备，stops=%d", p.stops)
	}
	if v := s.q.View(0); v.Playing {
		t.Fatal("队列应转为暂停")
	}
	if len(p.pushes) != 0 {
		t.Fatal("停下来就够了，不该再推窗")
	}
}

func TestQueueRemoveEverythingStops(t *testing.T) {
	a, s, p := editSession(t, 1, true)
	s.q.MarkPlaying()
	code, resp := post(t, a.Routes(), "/api/queue/remove", map[string]any{"index": 0})
	if code != 200 {
		t.Fatalf("HTTP %d：%v", code, resp)
	}
	if p.stops != 1 || s.q.Len() != 0 || resp["total"] != float64(0) {
		t.Fatalf("应停下并返回空队列：stops=%d len=%d resp=%v", p.stops, s.q.Len(), resp)
	}
	if s.q.View(0).Playing {
		t.Fatal("队列空了不该还在播")
	}
}

func TestQueueClearAll(t *testing.T) {
	a, s, p := editSession(t, 3, true)
	s.q.MarkPlaying()
	code, resp := post(t, a.Routes(), "/api/queue/remove", map[string]any{"all": true})
	if code != 200 || resp["cleared"] != true {
		t.Fatalf("HTTP %d：%v", code, resp)
	}
	if p.stops != 1 || s.q.Len() != 0 {
		t.Fatalf("应停设备并清空：stops=%d len=%d", p.stops, s.q.Len())
	}
}

// 暂停时编辑队列不该出声：只记标记，等 resume 再重推。
func TestQueueEditWhilePausedDefersRepush(t *testing.T) {
	a, s, p := editSession(t, 5, true)
	s.q.Jump(2)
	s.q.MarkPlaying()
	s.q.SyncPosition(25000, true)
	s.q.MarkPaused() // 停在 25s

	code, resp := post(t, a.Routes(), "/api/queue/remove?speaker=living", map[string]any{"index": 3})
	if code != 200 {
		t.Fatalf("HTTP %d：%v", code, resp)
	}
	if len(p.pushes) != 0 {
		t.Fatalf("暂停中不该推窗：%d 次", len(p.pushes))
	}
	if pend, pos := s.pendingEdit(); !pend || pos < 24_900 || pos > 25_200 {
		t.Fatalf("应留下待同步标记：pend=%v pos=%d", pend, pos)
	}

	// resume：重推整窗 + 跳回 25s，而不是 player.Resume（设备手里是过时的窗）
	code, resp = post(t, a.Routes(), "/api/resume?speaker=living", nil)
	if code != 200 {
		t.Fatalf("resume HTTP %d：%v", code, resp)
	}
	if len(p.pushes) != 1 {
		t.Fatalf("resume 应重推整窗，pushes=%d", len(p.pushes))
	}
	if p.resumes != 0 {
		t.Fatal("resume 不该走设备的 player.Resume（窗口已经变了）")
	}
	if len(p.seeks) != 1 || p.seeks[0] != 25000 {
		t.Fatalf("应跳回 25s：%v", p.seeks)
	}
	if pend, _ := s.pendingEdit(); pend {
		t.Fatal("resume 之后标记应清掉")
	}
	if !s.q.View(0).Playing {
		t.Fatal("resume 后应为播放态")
	}
}

func TestQueueMoveKeepsCurrentTrack(t *testing.T) {
	a, s, p := editSession(t, 5, true)
	s.q.Jump(2)
	s.q.MarkPlaying()
	s.q.SyncPosition(8000, true)
	before, _, _ := s.q.Current()

	code, resp := post(t, a.Routes(), "/api/queue/move?speaker=living", map[string]any{"from": 0, "to": 4})
	if code != 200 {
		t.Fatalf("HTTP %d：%v", code, resp)
	}
	after, _, _ := s.q.Current()
	if before.ID != after.ID {
		t.Fatalf("移动别的曲目不该换掉当前曲：%d → %d", before.ID, after.ID)
	}
	if got := resp["index"]; got != float64(1) {
		t.Fatalf("当前下标应左移一位：%v", got)
	}
	if len(p.pushes) != 1 || len(p.pushes[0]) != 4 {
		t.Fatalf("应重推一窗 4 首：%v", p.pushes)
	}
	if len(p.seeks) != 1 || p.seeks[0] < 7_900 || p.seeks[0] > 8_200 {
		t.Fatalf("应 seek 回 8s：%v", p.seeks)
	}
}

func TestQueueMoveRejectsBadIndex(t *testing.T) {
	a, _, _ := editSession(t, 3, true)
	code, resp := post(t, a.Routes(), "/api/queue/move?speaker=living", map[string]any{"from": 0, "to": 9})
	if code != 400 {
		t.Fatalf("越界应 400：%d %v", code, resp)
	}
	code, resp = post(t, a.Routes(), "/api/queue/remove?speaker=living", map[string]any{"index": 7})
	if code != 400 {
		t.Fatalf("越界删除应 400：%d %v", code, resp)
	}
}

// 单曲模式（非整窗）：只有当前曲被删/被换才重新下发。
func TestQueueEditSingleModeOnlyRestartsWhenCurrentChanged(t *testing.T) {
	a, s, p := editSession(t, 4, false)
	s.q.Jump(1)
	s.q.MarkPlaying()

	if _, resp := post(t, a.Routes(), "/api/queue/remove?speaker=living", map[string]any{"index": 3}); resp["ok"] != true {
		t.Fatalf("删除队尾失败：%v", resp)
	}
	if len(p.plays) != 0 {
		t.Fatalf("没动到当前曲不该重新下发：%v", p.plays)
	}

	if _, resp := post(t, a.Routes(), "/api/queue/remove?speaker=living", map[string]any{"index": 1}); resp["ok"] != true {
		t.Fatalf("删除当前曲失败：%v", resp)
	}
	if len(p.plays) != 1 {
		t.Fatalf("当前曲被删应下发新的当前曲：%v", p.plays)
	}
}

// 暂停态 seek 不该把播放态翻成播放（无探针时没有任何机制能纠正回来）。
func TestSeekWhilePausedKeepsPaused(t *testing.T) {
	_, s, _ := editSession(t, 4, true)
	s.q.Jump(1)
	s.q.MarkPlaying()
	s.q.SyncPosition(30_000, true)
	s.q.MarkPaused()

	if err := s.Seek(context.Background(), 60_000); err != nil {
		t.Fatalf("seek 失败：%v", err)
	}
	v := s.q.View(0)
	if v.Playing {
		t.Fatal("暂停态 seek 后不该变成播放")
	}
	if v.PositionMs < 59_000 {
		t.Fatalf("位置没落到目标：%d", v.PositionMs)
	}
}
