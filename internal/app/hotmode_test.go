package app

import (
	"os"
	"testing"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
)

// 主时机：本窗最后一首刚起播 → 重推（从 0 秒重来听不出来）。
func TestHotmodeActionLastSong(t *testing.T) {
	if got := hotmodeAction(19, 19, 0, true, 1200, 30, 5, 0); got != "last" {
		t.Fatalf("最后一首刚起播应返回 last，得到 %q", got)
	}
	// 已经播远了就别再重推，否则会把歌拽回开头
	if got := hotmodeAction(19, 19, 0, true, hotLastSongWin+1, 30, 5, 0); got != "" {
		t.Fatalf("最后一首播过窗口期不该重推，得到 %q", got)
	}
}

// 兜底：错过了主时机，设备绕回窗口开头 → 同样在 0 秒处重推。
func TestHotmodeActionWrap(t *testing.T) {
	if got := hotmodeAction(0, 19, 0, true, 800, 30, 5, 0); got != "wrap" {
		t.Fatalf("绕回窗口开头应返回 wrap，得到 %q", got)
	}
	// 刚推完窗时 idx==winFrom 但还没离开过第一首，不能重推（否则死循环）
	if got := hotmodeAction(0, 19, 0, false, 800, 30, 5, 0); got != "" {
		t.Fatalf("刚推完窗不该再次重推，得到 %q", got)
	}
}

// 队列中间的歌不该触发重推。
func TestHotmodeActionIdle(t *testing.T) {
	if got := hotmodeAction(7, 19, 0, true, 60000, 30, 5, 0); got != "" {
		t.Fatalf("中间曲目不该重推，得到 %q", got)
	}
}

// restock 时机：手动 seek 到窗口中段后，下一次切歌的起播窗口期，
// 只要未播剩余低于补货阈值且推窗后队列有新歌，就重推把新歌带进设备窗口。
func TestHotmodeActionRestock(t *testing.T) {
	// 当前曲不在窗口末尾（idx=7, pushedTo=19），刚起播，剩余不足
	if got := hotmodeAction(7, 19, 0, true, 3000, 30, 5, 10); got != "restock" {
		t.Fatalf("起播期 + 剩余不足 + 有新歌应返回 restock，得到 %q", got)
	}
	// 推窗后队列没有新歌（winLenDelta=0）：推了也白推，不触发
	if got := hotmodeAction(7, 19, 0, true, 3000, 30, 5, 0); got != "" {
		t.Fatalf("没有新歌时 restock 不该触发，得到 %q", got)
	}
	// 播远了才重推会把歌拽回开头：不触发
	if got := hotmodeAction(7, 19, 0, true, 60000, 30, 5, 10); got != "" {
		t.Fatalf("播过起播窗口期不该 restock，得到 %q", got)
	}
	// below<=0（续杯关闭）不触发
	if got := hotmodeAction(7, 19, 0, true, 3000, 0, 5, 10); got != "" {
		t.Fatalf("below=0 不该 restock，得到 %q", got)
	}
}

// last 优先于 restock：最后一首刚起播时按 last 走（语义更准）。
func TestHotmodeActionLastBeatsRestock(t *testing.T) {
	if got := hotmodeAction(19, 19, 0, true, 1200, 30, 0, 10); got != "last" {
		t.Fatalf("应返回 last，得到 %q", got)
	}
}

// 退避阶梯：30s → 2min → 10min（真实接口错误才走到这里）。
func TestHotBackoffLadder(t *testing.T) {
	cases := []struct {
		fails int
		want  time.Duration
	}{{0, 30 * time.Second}, {1, 30 * time.Second}, {2, 2 * time.Minute}, {3, 10 * time.Minute}, {9, 10 * time.Minute}}
	for _, c := range cases {
		if got := hotBackoff(c.fails); got != c.want {
			t.Fatalf("hotBackoff(%d)=%v，期望 %v", c.fails, got, c.want)
		}
	}
}

// 取种子要排除当前曲；队列只有一首时退化成它自己。
func TestRandomSeedSkipsCurrent(t *testing.T) {
	_, s := newEventSession()
	for i := 0; i < 50; i++ {
		id, ok := s.randomSeed()
		if !ok {
			t.Fatal("randomSeed 不该失败")
		}
		if id == 1 { // newEventSession 的当前曲
			t.Fatal("种子不能是当前曲")
		}
		if id != 2 {
			t.Fatalf("种子应为唯一的另一首 2，得到 %d", id)
		}
	}

	q := queue.NewQueue(queue.LoopList)
	q.Replace([]netease.Track{{ID: 9, Name: "only"}}, "test", 0)
	s.q = q
	if id, ok := s.randomSeed(); !ok || id != 9 {
		t.Fatalf("只有一首时应退化成它自己，得到 %d/%v", id, ok)
	}
}

// 续杯参数：0 跟随 window，负数关闭，失败上限缺省 3。
func TestHotmodeConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := dir + "/" + name
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cfg, err := config.Load(write("auto.json", `{"window":30}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HotmodeExtendBelow != 30 {
		t.Fatalf("below 缺省应跟随 window=30，得到 %d", cfg.HotmodeExtendBelow)
	}
	if cfg.HotmodeExtendMaxFail != 3 {
		t.Fatalf("max_fail 缺省应为 3，得到 %d", cfg.HotmodeExtendMaxFail)
	}

	cfg, err = config.Load(write("off.json", `{"window":30,"hotmode_extend_below":-1}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HotmodeExtendBelow != 0 {
		t.Fatalf("负数应归一成 0（关闭），得到 %d", cfg.HotmodeExtendBelow)
	}
}

// 裁剪后的窗口边界必须跟着平移：winFrom/pushedTo 是**队列下标**，
// 错一位续杯时机就全乱（真机上表现成"再也不重推"或"反复重启当前曲"）。
func TestTrimHotHistoryShiftsWindow(t *testing.T) {
	s := testSession(130)
	if _, _, ok := s.q.Jump(120); !ok {
		t.Fatal("Jump 失败")
	}
	s.hotMu.Lock()
	s.winFrom, s.pushedTo = 118, 129
	s.hotMu.Unlock()

	if got := s.trimHotHistory(100); got != 20 {
		t.Fatalf("应裁掉 20 首（index 120 - keepBack 100），得到 %d", got)
	}
	v := s.q.View(0)
	if v.Total != 110 || v.Index != 100 {
		t.Fatalf("队列应为 110 首、index=100，实际 total=%d index=%d", v.Total, v.Index)
	}
	from, to, _ := s.windowBounds()
	if from != 98 || to != 109 {
		t.Fatalf("窗口边界应平移到 98/109，实际 %d/%d", from, to)
	}
	// 平移后 last 时机仍然认得出（当前曲走到窗口末尾就该重推）
	if got := hotmodeAction(109, to, from, true, 1000, 30, 5, 0); got != "last" {
		t.Fatalf("平移后 last 时机应成立，得到 %q", got)
	}
}

// 窗口开头被整段裁掉时，winFrom 置 -1 让 wrap 判定作废。
// 钳成 0 会造出"假绕回"：设备根本没绕回，服务端却认为绕回了而乱推。
func TestTrimHotHistoryInvalidatesWrapWhenWindowGone(t *testing.T) {
	s := testSession(130)
	if _, _, ok := s.q.Jump(120); !ok {
		t.Fatal("Jump 失败")
	}
	s.hotMu.Lock()
	s.winFrom, s.pushedTo = 5, 129 // 窗口开头早已播完并被裁掉
	s.hotMu.Unlock()

	s.trimHotHistory(100)
	from, to, _ := s.windowBounds()
	if from != -1 {
		t.Fatalf("窗口开头被裁光时 winFrom 应为 -1（禁用 wrap），得到 %d", from)
	}
	if to != 109 {
		t.Fatalf("pushedTo 应平移到 109，得到 %d", to)
	}
	if got := hotmodeAction(0, to, from, true, 100, 30, 5, 0); got == "wrap" {
		t.Fatal("winFrom=-1 时不该判成 wrap")
	}
}

// 没到裁剪线时不该动任何东西。
func TestTrimHotHistoryNoop(t *testing.T) {
	s := testSession(30)
	s.hotMu.Lock()
	s.winFrom, s.pushedTo = 0, 29
	s.hotMu.Unlock()
	if got := s.trimHotHistory(100); got != 0 {
		t.Fatalf("index 未超过 keepBack 不该裁剪，得到 %d", got)
	}
	from, to, _ := s.windowBounds()
	if from != 0 || to != 29 {
		t.Fatalf("不该改动窗口边界，实际 %d/%d", from, to)
	}
}
