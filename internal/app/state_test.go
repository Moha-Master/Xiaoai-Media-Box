package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
)

func threeTracks() []netease.Track {
	return []netease.Track{
		{ID: 11, Name: "a", Duration: 200_000},
		{ID: 22, Name: "b", Duration: 200_000},
		{ID: 33, Name: "c", Duration: 200_000},
	}
}

// 造一个开了持久化的 Session（沿用 events_test 的夹具，但不启任何 goroutine）。
func persistSession(dir, id string) *Session {
	a, s := newEventSession()
	a.cfg = config.Config{QueuePersist: true, BaseDir: dir, Window: 20, HotmodeExtendBelow: 20}
	s.speaker.ID = id
	s.windowMode = true
	return s
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := persistSession(dir, "living")
	s.q.Replace(threeTracks(), "心动模式·X", 1)
	s.q.SyncPosition(9_000, true)
	s.StartHotmode("454706825")
	s.hotMu.Lock()
	s.hotPushed, s.winFrom, s.pushedTo, s.winAdvanced = 4, 0, 2, true
	s.hotMu.Unlock()
	s.volMu.Lock()
	s.volume, s.muted, s.mutePrev = 42, true, 66
	s.volMu.Unlock()

	s.saveStateIfChanged()
	if _, err := os.Stat(s.statePath()); err != nil {
		t.Fatalf("应该写出快照：%v", err)
	}

	s2 := persistSession(dir, "living")
	s2.loadState()
	v := s2.q.FullView()
	if v.Total != 3 || v.Index != 1 || v.Source != "心动模式·X" {
		t.Fatalf("队列没恢复对：total=%d index=%d source=%q", v.Total, v.Index, v.Source)
	}
	if v.Playing {
		t.Fatal("恢复后不能自动进入播放态（重启后不该自动出声）")
	}
	if v.PositionMs != 9_000 {
		t.Fatalf("进度应恢复成 9000，得到 %d", v.PositionMs)
	}
	on, _, pushed, _ := s2.HotmodeState()
	if !on || pushed != 4 {
		t.Fatalf("心动续杯没恢复：on=%v pushed=%d", on, pushed)
	}
	if s2.hotPID != "454706825" {
		t.Fatalf("心跳 playlist id 没恢复：%q", s2.hotPID)
	}
	from, to, adv := s2.windowBounds()
	if from != 0 || to != 2 || !adv {
		t.Fatalf("窗口边界没恢复：%d/%d/%v", from, to, adv)
	}
	vol, muted := s2.VolumeState()
	if vol != 42 || !muted {
		t.Fatalf("音量没恢复：%d/%v", vol, muted)
	}

	// 文件必须是缩进 JSON 且 0600，方便人肉看/备份
	if fi, err := os.Stat(s2.statePath()); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("快照权限应 0600，得到 %v", fi.Mode())
	}
	b, _ := os.ReadFile(s2.statePath())
	if !strings.Contains(string(b), "\"version\": 1") {
		t.Fatal("快照应是缩进 JSON")
	}
}

// 配置里关掉持久化就一个字节都不该写。
func TestStateDisabledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	s := persistSession(dir, "living")
	s.app.cfg.QueuePersist = false
	s.saveStateIfChanged()
	s.flushState()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("关掉持久化后不该有文件，得到 %d 个", len(entries))
	}
}

// 从头到尾没放过歌的音箱不该被凭空造一个空快照。
func TestStateNoEmptyFile(t *testing.T) {
	dir := t.TempDir()
	s := persistSession(dir, "bedroom")
	s.q.Reset()
	s.saveStateIfChanged()
	if _, err := os.Stat(s.statePath()); err == nil {
		t.Fatal("空队列不该落盘")
	}
}

// 状态没变就别反复写盘（位置只在播放时按分桶前进，这里根本没在播）。
func TestStateDedupesIdenticalWrites(t *testing.T) {
	dir := t.TempDir()
	s := persistSession(dir, "living")
	s.q.Replace(threeTracks(), "测试", 0)
	s.saveStateIfChanged()
	if err := os.Remove(s.statePath()); err != nil {
		t.Fatal(err)
	}
	s.saveStateIfChanged()
	if _, err := os.Stat(s.statePath()); err == nil {
		t.Fatal("签名没变就不该再写一次")
	}
	// 换个下标 → 签名变了 → 立刻写
	s.q.Jump(2)
	s.saveStateIfChanged()
	if _, err := os.Stat(s.statePath()); err != nil {
		t.Fatalf("结构变化应立刻落盘：%v", err)
	}
}

// 版本不认就整个忽略，别把队列灌成半截状态。
func TestStateUnknownVersionIgnored(t *testing.T) {
	dir := t.TempDir()
	s := persistSession(dir, "living")
	if err := util.WriteJSONFile(s.statePath(), map[string]any{
		"version": 99, "speaker": "living",
		"queue": map[string]any{"tracks": threeTracks(), "index": 2},
	}); err != nil {
		t.Fatal(err)
	}
	s.loadState()
	if v := s.q.FullView(); v.Source != "test" || v.Total != 2 {
		t.Fatalf("未知版本不该动队列：total=%d source=%q", v.Total, v.Source)
	}
}

// 续杯被配置关掉时，别把 hotmode.on 恢复回来（否则只会莫名其妙地重推同一窗）。
func TestStateHotmodeNotRestoredWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	s := persistSession(dir, "living")
	s.q.Replace(threeTracks(), "心动模式·X", 0)
	s.StartHotmode("454706825")
	s.saveStateIfChanged()

	s2 := persistSession(dir, "living")
	s2.app.cfg.HotmodeExtendBelow = 0
	s2.loadState()
	if on, _, _, _ := s2.HotmodeState(); on {
		t.Fatal("配置里关了续杯，恢复时不该把它打开")
	}
}

func TestStateFileName(t *testing.T) {
	if got := stateFileName("living"); got != "queue-living.json" {
		t.Fatalf("普通 id 不该被改动，得到 %q", got)
	}
	for _, id := range []string{"../../etc/passwd", "客厅/a b", "", "/", "..", "a/b"} {
		got := stateFileName(id)
		base := filepath.Base(got)
		if got != base || strings.ContainsAny(got, `/\`) {
			t.Fatalf("id %q 生成的文件名 %q 不安全", id, got)
		}
		if strings.HasPrefix(base, "queue-.") || strings.Contains(base, "..") {
			t.Fatalf("id %q 生成的文件名 %q 有穿越风险", id, got)
		}
	}
	// 清洗后可能撞车的两个 id，必须靠短哈希分开
	if stateFileName("客厅") == stateFileName("卧室") {
		t.Fatal("不同中文 id 不该撞成同一个文件")
	}
	if stateFileName("客厅") == stateFileName("客厅/") {
		t.Fatal("清洗结果相同但原 id 不同时仍应分开")
	}
}

// 快照目录跟着配置文件目录走，不受 cwd 影响。
func TestStateDirFollowsConfig(t *testing.T) {
	a := &App{cfg: config.Config{BaseDir: "/srv/xiaoai"}}
	if got := a.stateDir(); got != filepath.Join("/srv/xiaoai", "state") {
		t.Fatalf("stateDir=%q", got)
	}
	a.cfg.BaseDir = ""
	if got := a.stateDir(); got != "state" {
		t.Fatalf("BaseDir 为空时应退回相对路径，得到 %q", got)
	}
}

// 写盘跑在调度器 goroutine，persistInfo 跑在 HTTP/SSE goroutine，
// 两者并发必须干净（stMu 就是为这个加的）。
func TestStateConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	s := persistSession(dir, "living")
	s.q.Replace(threeTracks(), "测试", 0)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			s.q.Jump(i % 3)
			s.saveStateIfChanged()
		}
	}()
	for i := 0; i < 500; i++ {
		if s.persistInfo() == nil {
			t.Fatal("开了持久化时 persistInfo 不该为 nil")
		}
	}
	close(stop)
	// 必须等写盘 goroutine 真的退出：t.TempDir() 的清理会和它抢 state/ 目录
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("写盘 goroutine 没退出")
	}
}
