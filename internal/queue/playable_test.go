package queue

import "testing"

// SetPlayable：当前曲第一次学到试听区间时，位置要从片段轴换算回原曲轴
// （设备在片段的 0 秒 = 原曲的 start 处）。
func TestSetPlayableReanchorsCurrent(t *testing.T) {
	q := mkQueue(3)
	q.Move(1, false)           // 当前是 id=2
	q.SyncPosition(5000, true) // 片段轴：设备已播 5 秒
	if !q.SetPlayable(2, 69000, 92000) {
		t.Fatal("第一次写入应返回 true")
	}
	// 位置 = start + 设备已播 = 69000 + 5000
	if v := q.View(0); v.PositionMs != 74000 {
		t.Fatalf("重锚定后位置应为 74000，得到 %d", v.PositionMs)
	}
	cur, _, _ := q.Current()
	if cur.TrialStartMs != 69000 || cur.TrialEndMs != 92000 {
		t.Fatalf("试听区间没写进曲目：%+v", cur)
	}
}

// SetPlayable 幂等：同样的区间重复写入返回 false，且**不得**再次叠加 start。
func TestSetPlayableIdempotent(t *testing.T) {
	q := mkQueue(2)
	q.SetPlayable(1, 69000, 92000)
	q.SyncPosition(69000, true) // 已换算到原曲轴
	if q.SetPlayable(1, 69000, 92000) {
		t.Fatal("同样的区间重复写入应返回 false")
	}
	if v := q.View(0); v.PositionMs != 69000 {
		t.Fatalf("重复写入不得叠加 start：位置应为 69000，得到 %d", v.PositionMs)
	}
}

func TestSetPlayableUnknownID(t *testing.T) {
	q := mkQueue(2)
	if q.SetPlayable(999, 0, 20000) {
		t.Fatal("队列里没有的 id 应返回 false")
	}
}

func TestSetPlayableStartZero(t *testing.T) {
	q := mkQueue(1)
	q.SyncPosition(12000, true)
	q.SetPlayable(1, 0, 35000)
	// start=0：区间写入但位置不变（片段轴 == 原曲轴）
	if v := q.View(0); v.PositionMs != 12000 {
		t.Fatalf("start=0 不该挪位置，得到 %d", v.PositionMs)
	}
}

// TrimHistory：裁掉当前曲之前的历史，index 平移，返回移除条数。
func TestTrimHistoryShiftsIndex(t *testing.T) {
	q := mkQueue(10)
	q.Move(5, false) // index=5
	if got := q.TrimHistory(2); got != 3 {
		t.Fatalf("应移除 3 首，得到 %d", got)
	}
	if v := q.View(0); v.Index != 2 || v.Total != 7 {
		t.Fatalf("index 应平移到 2、总数 7，得到 %d/%d", v.Index, v.Total)
	}
	// 当前曲身份不变
	if cur, idx, _ := q.Current(); cur.ID != 6 || idx != 2 {
		t.Fatalf("当前曲应是 id=6 @2，得到 %v @%d", cur.ID, idx)
	}
}

func TestTrimHistoryNoop(t *testing.T) {
	q := mkQueue(5)
	q.Move(3, false)
	if got := q.TrimHistory(5); got != 0 {
		t.Fatalf("keepBack 覆盖全部历史时应为 no-op，得到 %d", got)
	}
	if got := q.TrimHistory(3); got != 0 {
		t.Fatalf("恰好保留 keepBack 首时应为 no-op，得到 %d", got)
	}
	if v := q.View(0); v.Index != 3 {
		t.Fatalf("no-op 不得挪 index，得到 %d", v.Index)
	}
}

// 下标平移雷区：移除后 View/Move/Jump 语义必须依然正确（错一位续杯就全乱）。
func TestTrimHistoryThenNavigate(t *testing.T) {
	q := mkQueue(10)
	q.Move(6, false) // index=6
	q.TrimHistory(2) // 移除 4 首 → index=2，总 6 首
	if _, idx, ok := q.Move(1, false); !ok || idx != 3 {
		t.Fatalf("裁剪后前进应到 3，得到 %d ok=%v", idx, ok)
	}
	if _, idx, _ := q.Jump(5); idx != 5 {
		t.Fatalf("裁剪后 Jump(5) 应有效，得到 %d", idx)
	}
	if _, idx, _ := q.Jump(6); idx != 0 {
		t.Fatalf("Jump 越界按既有语义回绕到 0，得到 %d", idx)
	}
}

func TestTrimHistoryEmptyQueue(t *testing.T) {
	q := mkQueue(0)
	if got := q.TrimHistory(100); got != 0 {
		t.Fatalf("空队列应返回 0，得到 %d", got)
	}
}

// 带试听区间的曲目经 TrimHistory 平移后区间必须原样保留。
func TestTrimHistoryKeepsTrialInfo(t *testing.T) {
	q := mkQueue(4)
	q.tracks[3].TrialStartMs, q.tracks[3].TrialEndMs = 69000, 92000
	q.Move(3, false)
	q.TrimHistory(1)
	cur, _, _ := q.Current()
	if cur.ID != 4 || cur.TrialStartMs != 69000 || cur.TrialEndMs != 92000 {
		t.Fatalf("裁剪后当前曲/区间丢失：%+v", cur)
	}
}
