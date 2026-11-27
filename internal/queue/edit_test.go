package queue

import "testing"

// 移动/删除队列时的下标语义：当前曲目身份不变，除非被删的就是它。
func TestRemoveAt(t *testing.T) {
	cases := []struct {
		name           string
		n, idx, remove int
		wantIdx        int
		wantCurrent    bool // 删掉的是不是当前曲
		wantLen        int
	}{
		{"删当前曲之前", 5, 2, 0, 1, false, 4},
		{"删当前曲", 5, 2, 2, 2, true, 4},
		{"删当前曲之后", 5, 2, 4, 2, false, 4},
		{"删当前曲且是末尾", 5, 4, 4, 3, true, 4},
		{"只剩一首", 1, 0, 0, -1, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := mkQueue(c.n)
			q.Jump(c.idx)
			// 制造一点播放进度，确认删当前曲后位置归零
			q.SyncPosition(5000, true)
			removed, idx, currentRemoved, ok := q.RemoveAt(c.remove)
			if !ok {
				t.Fatal("RemoveAt 应成功")
			}
			if removed.ID != int64(c.remove+1) {
				t.Fatalf("删错了：id=%d", removed.ID)
			}
			if idx != c.wantIdx {
				t.Fatalf("下标=%d 期望 %d", idx, c.wantIdx)
			}
			if currentRemoved != c.wantCurrent {
				t.Fatalf("currentRemoved=%v 期望 %v", currentRemoved, c.wantCurrent)
			}
			if q.Len() != c.wantLen {
				t.Fatalf("长度=%d 期望 %d", q.Len(), c.wantLen)
			}
			if c.wantCurrent && c.wantLen > 0 && q.PositionMs() != 0 {
				t.Fatalf("删掉当前曲后位置应归零，得到 %d", q.PositionMs())
			}
		})
	}

	q := mkQueue(3)
	if _, _, _, ok := q.RemoveAt(3); ok {
		t.Fatal("越界应失败")
	}
	if _, _, _, ok := q.RemoveAt(-1); ok {
		t.Fatal("负下标应失败")
	}
}

func TestMoveAt(t *testing.T) {
	cases := []struct {
		name          string
		from, to, idx int
		wantIndex     int
	}{
		{"当前曲不动，从前面移到后面", 0, 3, 2, 1},
		{"当前曲不动，从后面移到前面", 3, 0, 1, 2},
		{"移动当前曲", 2, 4, 2, 4},
		{"从当前位置之前移走一首", 0, 2, 2, 1}, // 当前曲左移一位
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := mkQueue(5)
			q.Jump(c.idx)
			cur, _, _ := q.Current()
			curID := cur.ID
			idx, ok := q.MoveAt(c.from, c.to)
			if !ok {
				t.Fatal("MoveAt 应成功")
			}
			if idx != c.wantIndex {
				t.Fatalf("下标=%d 期望 %d", idx, c.wantIndex)
			}
			after, _, _ := q.Current()
			if c.from != c.idx && after.ID != curID {
				t.Fatalf("没移动当前曲，当前曲却变了：%d → %d", curID, after.ID)
			}
			if q.Len() != 5 {
				t.Fatalf("移动不应改变长度：%d", q.Len())
			}
		})
	}

	q := mkQueue(3)
	for _, bad := range [][2]int{{-1, 0}, {0, 3}, {1, 1}} {
		if _, ok := q.MoveAt(bad[0], bad[1]); ok {
			t.Fatalf("MoveAt(%d,%d) 应失败", bad[0], bad[1])
		}
	}
	// 顺序真的变了
	q2 := mkQueue(3)
	q2.MoveAt(0, 2)
	want := []int64{2, 3, 1}
	for i, id := range want {
		tr, ok := q2.At(i)
		if !ok || tr.ID != id {
			t.Fatalf("第 %d 首应为 %d，得到 %d", i, id, tr.ID)
		}
	}
}
