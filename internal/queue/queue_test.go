package queue

import (
	"testing"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
)

func mkQueue(n int) *Queue {
	tracks := make([]netease.Track, n)
	for i := range tracks {
		tracks[i] = netease.Track{ID: int64(i + 1), Name: "t", Duration: 1000}
	}
	q := NewQueue(LoopList)
	q.Replace(tracks, "test", 0)
	return q
}

func TestWrap(t *testing.T) {
	cases := []struct{ i, n, want int }{
		{0, 3, 0}, {3, 3, 0}, {-1, 3, 2}, {5, 3, 2}, {0, 0, 0},
	}
	for _, c := range cases {
		if got := Wrap(c.i, c.n); got != c.want {
			t.Errorf("Wrap(%d,%d)=%d want %d", c.i, c.n, got, c.want)
		}
	}
}

func TestMoveListWraps(t *testing.T) {
	q := mkQueue(3)
	want := []int{1, 2, 0, 1}
	for i, w := range want {
		if _, idx, ok := q.Move(1, false); !ok || idx != w {
			t.Fatalf("第 %d 次前进得到 idx=%d（期望 %d）ok=%v", i, idx, w, ok)
		}
	}
	// 当前在 1，后退一首应回到 0
	if _, idx, _ := q.Move(-1, false); idx != 0 {
		t.Fatalf("后退一首应为 0，得到 %d", idx)
	}
	// 再后退应回绕到 2
	if _, idx, _ := q.Move(-1, false); idx != 2 {
		t.Fatalf("后退越界应回绕到 2，得到 %d", idx)
	}
}

func TestSingleLoopNatural(t *testing.T) {
	q := mkQueue(3)
	q.Move(1, false) // 到 1
	q.SetLoop(LoopSingle)
	if _, idx, _ := q.Move(1, true); idx != 1 {
		t.Fatalf("单曲循环自然播完应原地重放，得到 idx=%d", idx)
	}
	if _, idx, _ := q.Move(1, false); idx != 2 {
		t.Fatalf("单曲循环手动下一首应前进，得到 idx=%d", idx)
	}
}

func TestShuffleNeverRepeats(t *testing.T) {
	q := mkQueue(5)
	q.SetShuffle(true)
	for i := 0; i < 50; i++ {
		before := q.View(0).Index
		_, idx, _ := q.Move(1, false)
		if idx == before {
			t.Fatalf("随机模式不应重复当前曲（idx=%d）", idx)
		}
	}
}

func TestNoLoopStopsAtEnd(t *testing.T) {
	q := mkQueue(3)
	q.SetLoop(LoopNone)
	if _, idx, ok := q.Move(1, false); !ok || idx != 1 {
		t.Fatalf("不循环时前进应成功，得到 idx=%d ok=%v", idx, ok)
	}
	if _, _, ok := q.Move(1, false); !ok {
		t.Fatal("第二首应还能前进")
	}
	if _, idx, ok := q.Move(1, false); ok {
		t.Fatalf("队尾再前进应返回 ok=false，得到 idx=%d", idx)
	}
	if idx := q.View(0).Index; idx != 2 {
		t.Fatalf("走到头后下标应停在 2，得到 %d", idx)
	}
}

func TestNoLoopPrevAtStart(t *testing.T) {
	q := mkQueue(3)
	q.SetLoop(LoopNone)
	if _, _, ok := q.Move(-1, false); ok {
		t.Fatal("队首再后退应返回 ok=false")
	}
}

func TestJumpAndReplace(t *testing.T) {
	q := mkQueue(4)
	if _, idx, _ := q.Jump(10); idx != 2 {
		t.Fatalf("Jump(10) 应回绕到 2，得到 %d", idx)
	}
	q.Replace(nil, "empty", 0)
	if v := q.View(5); v.Total != 0 || v.Current != nil {
		t.Fatalf("清空后应无当前曲，得到 %+v", v)
	}
	if _, _, ok := q.Move(1, false); ok {
		t.Fatalf("空队列 Move 应返回 false")
	}
}

func TestPositionTracksPlaying(t *testing.T) {
	q := mkQueue(1)
	q.MarkPlaying()
	q.basePos = 5000
	if pos := q.PositionMs(); pos < 5000 {
		t.Fatalf("播放中位置应 >= basePos，得到 %d", pos)
	}
	q.MarkPaused()
	frozen := q.PositionMs()
	if frozen < 5000 {
		t.Fatalf("暂停后位置应保留，得到 %d", frozen)
	}
}
