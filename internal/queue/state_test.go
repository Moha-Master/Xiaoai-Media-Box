package queue

import (
	"fmt"
	"testing"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
)

func mkTracks(n int) []netease.Track {
	out := make([]netease.Track, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, netease.Track{
			ID: int64(i + 1), Name: fmt.Sprintf("t%d", i+1), Duration: 200_000,
		})
	}
	return out
}

// 快照要能原样回来，且 playing 必须被抹掉（重启后不能自动出声）。
func TestStateRoundTrip(t *testing.T) {
	q := NewQueue(LoopList)
	q.Replace(mkTracks(3), "心动模式·X", 1)
	q.SetShuffle(false)
	q.SyncPosition(12_345, true)

	st := q.ToState()
	if st.Playing != true {
		t.Fatal("导出的快照应如实记下“当时在播”")
	}

	q2 := NewQueue(LoopNone)
	q2.FromState(st)
	v := q2.FullView()
	if v.Total != 3 || v.Index != 1 || v.Source != "心动模式·X" {
		t.Fatalf("恢复不对：total=%d index=%d source=%q", v.Total, v.Index, v.Source)
	}
	if v.Repeat != string(LoopList) {
		t.Fatalf("loop 应恢复成 list，得到 %q", v.Repeat)
	}
	if v.Playing {
		t.Fatal("恢复后不该处于播放态")
	}
	if v.PositionMs != 12_345 {
		t.Fatalf("进度应为 12345，得到 %d", v.PositionMs)
	}
}

// 快照里混进坏数据（id=0、下标越界、进度超过时长）都要能兜住。
func TestFromStateToleratesGarbage(t *testing.T) {
	q := NewQueue(LoopList)
	q.FromState(State{Tracks: []netease.Track{{ID: 0}, {ID: 7, Duration: 1000}}, Index: 99, PositionMs: 50_000})
	v := q.FullView()
	if v.Total != 1 || v.Index != 0 || v.Current == nil || v.Current.ID != 7 {
		t.Fatalf("应过滤掉空 id 并把下标夹回 0，得到 total=%d index=%d", v.Total, v.Index)
	}
	if v.PositionMs != 0 {
		t.Fatalf("进度超过时长应归零，得到 %d", v.PositionMs)
	}

	q2 := NewQueue(LoopList)
	q2.FromState(State{Tracks: nil})
	if q2.Len() != 0 {
		t.Fatal("空快照不该动队列")
	}
}

// 无限心动让队列只增不减，落盘必须封顶，且当前曲不能因为裁剪错位。
func TestToStateTrimsAroundCurrent(t *testing.T) {
	q := NewQueue(LoopList)
	q.Replace(mkTracks(3000), "很长", 2000)
	st := q.ToState()
	if len(st.Tracks) != StateTrackCap {
		t.Fatalf("应裁到 %d 首，得到 %d", StateTrackCap, len(st.Tracks))
	}
	if st.Index < 0 || st.Index >= len(st.Tracks) {
		t.Fatalf("裁剪后下标越界：%d", st.Index)
	}
	if st.Tracks[st.Index].ID != 2001 {
		t.Fatalf("裁剪后当前曲应是 id=2001，得到 %d", st.Tracks[st.Index].ID)
	}

	q2 := NewQueue(LoopList)
	q2.FromState(st)
	if id, _, ok := q2.Current(); !ok || id.ID != 2001 {
		t.Fatalf("恢复后当前曲应是 id=2001，得到 %d", id.ID)
	}
}

// 队列很短时不该被裁剪碰过。
func TestToStateNoTrimBelowCap(t *testing.T) {
	q := NewQueue(LoopList)
	q.Replace(mkTracks(StateTrackCap), "刚好", 0)
	if len(q.ToState().Tracks) != StateTrackCap {
		t.Fatal("刚好到上限不该裁")
	}
}
