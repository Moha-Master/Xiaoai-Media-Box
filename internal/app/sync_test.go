package app

import (
	"context"
	"testing"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
)

func TestSyncPosition(t *testing.T) {
	q := mkQueue(1)
	q.MarkPlaying()
	q.SyncPosition(9000, true)
	if pos := q.PositionMs(); pos < 9000 {
		t.Fatalf("纠偏后应 >= 9000，得到 %d", pos)
	}
	q.SyncPosition(9000, false)
	if q.View(0).Playing {
		t.Fatal("SyncPosition(false) 后应处于暂停")
	}
	if pos := q.PositionMs(); pos != 9000 {
		t.Fatalf("暂停后位置应冻结在 9000，得到 %d", pos)
	}
}

func TestFindByID(t *testing.T) {
	q := mkQueue(3) // id 1..3
	if _, idx, ok := q.FindByID(3); !ok || idx != 2 {
		t.Fatalf("FindByID(3) → idx=%d ok=%v", idx, ok)
	}
	if _, _, ok := q.FindByID(99); ok {
		t.Fatal("不存在的 id 不该命中")
	}
}

func testSession(n int) *Session {
	app := &App{}
	q := queue.NewQueue(queue.LoopList)
	tracks := make([]netease.Track, n)
	for i := range tracks {
		tracks[i] = netease.Track{ID: int64(i + 1), Name: "t", Duration: 200000}
	}
	q.Replace(tracks, "test", 0)
	q.MarkPlaying()
	return &Session{
		app:        app,
		speaker:    config.SpeakerConfig{ID: "test", Name: "test"},
		q:          q,
		probeEvery: 5 * time.Second,
		pushedTo:   -1,
	}
}

func TestSyncDeviceCorrectsPosition(t *testing.T) {
	s := testSession(3)
	st := player.DeviceStatus{Status: 1, AudioID: "1", Position: 12000, Duration: 200000}
	s.syncWithDevice(context.Background(), st)
	v := s.q.View(0)
	if v.Index != 0 || !v.Playing {
		t.Fatalf("同曲纠偏不该换歌：index=%d playing=%v", v.Index, v.Playing)
	}
	if v.PositionMs < 12000 {
		t.Fatalf("位置应被纠正到 >= 12000，得到 %d", v.PositionMs)
	}
}

func TestSyncDeviceFollowsExternalNext(t *testing.T) {
	s := testSession(3)
	// 用户在机身按了下一首：设备报的是队列里的第 3 首
	st := player.DeviceStatus{Status: 1, AudioID: "3", Position: 500, Duration: 200000}
	s.syncWithDevice(context.Background(), st)
	if v := s.q.View(0); v.Index != 2 || !v.Playing {
		t.Fatalf("应跟到 index=2，得到 index=%d playing=%v", v.Index, v.Playing)
	}
}

func TestSyncDeviceYieldsToForeignAudio(t *testing.T) {
	s := testSession(3)
	// 设备在放 TTS / App 点的别的歌：audio_id 不在我们队列里
	st := player.DeviceStatus{Status: 1, AudioID: "888888", Position: 1000, Duration: 5000}
	s.syncWithDevice(context.Background(), st)
	if v := s.q.View(0); v.Playing {
		t.Fatal("设备在放别人的东西时 xiaoai-mb 应让位（暂停）")
	}
	if v := s.q.View(0); v.Index != 0 {
		t.Fatalf("让位不该改变队列下标，得到 %d", v.Index)
	}
}

func TestSyncDeviceIgnoresTransitionWindow(t *testing.T) {
	s := testSession(3)
	// 刚推完第 2 首，设备还在报第 1 首：过渡期内不该把队列拽回去
	s.expectID = 2
	s.expectUntil = time.Now().Add(5 * time.Second)
	s.q.Jump(1)
	st := player.DeviceStatus{Status: 1, AudioID: "1", Position: 190000, Duration: 200000}
	s.syncWithDevice(context.Background(), st)
	if v := s.q.View(0); v.Index != 1 {
		t.Fatalf("过渡期内不该跟设备跑，得到 index=%d", v.Index)
	}
}

func TestFinishedGuardIsIdempotent(t *testing.T) {
	s := testSession(2)
	cur, _, _ := s.q.Current()
	if !s.endedID.CompareAndSwap(0, cur.ID) {
		t.Fatal("首次 CAS 应成功")
	}
	if s.endedID.CompareAndSwap(0, cur.ID) || s.endedID.Load() != cur.ID {
		t.Fatal("第二次应被挡住")
	}
}

func TestWindowLoopNoneStopsWhenWrappedToWindowHead(t *testing.T) {
	s := testSession(4)
	s.windowMode = true
	s.q.SetLoop(queue.LoopNone)
	// 整窗从第 3 首（index=2）推到队尾；设备播完绕回窗口第一首（id=3）
	s.q.Jump(2)
	s.winFrom = 2
	s.q.Jump(3) // 已经播到队尾那首
	s.endedID.Store(0)
	s.syncWithDevice(context.Background(), player.DeviceStatus{Status: 1, AudioID: "3", Position: 200, Duration: 200000})
	v := s.q.View(0)
	if v.Playing {
		t.Fatalf("不循环时绕回窗口头应停止，playing=%v index=%d", v.Playing, v.Index)
	}
	if v.Index != 3 {
		t.Fatalf("停止后队列下标应停在队尾那首，得到 %d", v.Index)
	}
}

func TestWindowLoopListFollowsWrapAround(t *testing.T) {
	s := testSession(4)
	s.windowMode = true
	s.q.SetLoop(queue.LoopList)
	s.q.Jump(3)
	s.winFrom = 0
	s.syncWithDevice(context.Background(), player.DeviceStatus{Status: 1, AudioID: "1", Position: 200, Duration: 200000})
	v := s.q.View(0)
	if !v.Playing || v.Index != 0 {
		t.Fatalf("列表循环绕回应继续播，playing=%v index=%d", v.Playing, v.Index)
	}
}

// queue.LoopNone 下，设备把这一窗放完会绕回窗口开头——要认出来并当作"队列播完"停下。
// 迁移到 payload 后设备侧的循环由 player_set_loop type=1 实现，这套判定不变，
// 所以这里用单元测试把它钉住。
func TestWindowWrapStopsWhenLoopNone(t *testing.T) {
	s := testSession(2)
	s.windowMode = true
	s.q.SetLoop(queue.LoopNone)
	s.q.Jump(1)   // 我们以为在放第 2 首
	s.winFrom = 0 // 这一窗从第 1 首开始，已经绕了一整圈
	s.q.SyncPosition(150000, true)

	// 设备报：又回到第 1 首、而且刚起播（<3s）→ 就是绕回
	s.syncWithDevice(context.Background(), player.DeviceStatus{
		Status: 1, AudioID: "1", Position: 500, Duration: 200000,
	})
	if s.q.View(0).Playing {
		t.Fatal("queue.LoopNone 下窗口绕回队首应视为播完并停下")
	}
}

// queue.LoopList 下同样的"绕回"不该停——那只是正常循环。
func TestWindowWrapKeepsPlayingWhenLoopList(t *testing.T) {
	s := testSession(2)
	s.windowMode = true
	s.q.SetLoop(queue.LoopList)
	s.q.Jump(1)
	s.winFrom = 0
	s.q.SyncPosition(150000, true)

	s.syncWithDevice(context.Background(), player.DeviceStatus{
		Status: 1, AudioID: "1", Position: 500, Duration: 200000,
	})
	if !s.q.View(0).Playing {
		t.Fatal("queue.LoopList 下绕回是正常循环，不该停")
	}
}

func mkQueue(n int) *queue.Queue {
	tracks := make([]netease.Track, n)
	for i := range tracks {
		tracks[i] = netease.Track{ID: int64(i + 1), Name: "t", Duration: 1000}
	}
	q := queue.NewQueue(queue.LoopList)
	q.Replace(tracks, "test", 0)
	return q
}
