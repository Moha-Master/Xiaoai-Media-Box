package app

import (
	"fmt"
	"hash/fnv"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
)

// 播放队列持久化：每台音箱一份快照文件，进程重启后恢复「列表 + 当前曲 + 进度 +
// 无限心动 + 音量」。
//
// 恢复时**不会自动出声**：
//   - 只有服务重启、音箱还在播 → 设备回报的 audio_id 就在恢复出来的队列里，
//     syncWithDevice 那条既有路径会把 index/进度认领过去，零静音间隔；
//   - 整机都重启了 → 列表就绪、处于暂停，等人按播放。
const (
	stateVersion = 1
	// 落盘位置按 3 分钟分桶进签名：播放中最多每 3 分钟写一次盘，
	// 而换曲/追加/暂停/改循环/改音量这些结构变化会立刻写。
	statePosBucketMs = 180_000
	stateDirName     = "state"
)

type stateFile struct {
	Version int            `json:"version"`
	Speaker string         `json:"speaker"`
	SavedAt int64          `json:"saved_at"`
	Queue   queue.State    `json:"queue"`
	Hotmode hotSnapshot    `json:"hotmode"`
	Volume  volumeSnapshot `json:"volume"`
}

// hotSnapshot 里带上窗口边界：重启后如果设备还在播旧窗口，
// 这几个下标还是有效的，续杯的“最后一首刚起播/绕回开头”两个时机才判得准。
// StopReason 一并落盘：give-up 的原因重启后 UI 还能说清“为什么没了”。
type hotSnapshot struct {
	On          bool   `json:"on"`
	PID         string `json:"pid"`
	Pushed      int    `json:"pushed"`
	Fails       int    `json:"fails"`
	WinFrom     int    `json:"win_from"`
	PushedTo    int    `json:"pushed_to"`
	WinAdvanced bool   `json:"win_advanced"`
	WinLen      int    `json:"win_len"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// volumeSnapshot：设备没有独立 mute，音量 0 分不清是“静音”还是“用户设 0”，
// 所以 muted / mutePrev 必须跟着一起来。
type volumeSnapshot struct {
	Value int  `json:"value"`
	Muted bool `json:"muted"`
	Prev  int  `json:"prev"`
}

// stateDir 是快照目录。跟着配置文件目录走（认证文件也是这么解析的），
// 这样换个 cwd 跑不会“找不到上次的队列”。
func (a *App) stateDir() string {
	base := a.cfg.BaseDir
	if base == "" {
		base = "."
	}
	return filepath.Join(base, stateDirName)
}

// stateFileName 把 speaker id 变成安全的文件名。
//
// 配置里的 id 没有任何字符集校验（可以是中文、甚至带斜杠），直接拼路径会有
// 目录穿越的风险；非白名单字符一律换成 '-'，真被改动过就追加原 id 的短哈希，
// 免得「客厅」和「客厅2」撞成同一个文件。
func stateFileName(id string) string {
	var b strings.Builder
	changed := false
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
			changed = true
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if len(slug) > 64 {
		slug = slug[:64]
		changed = true
	}
	if slug == "" {
		slug = "speaker"
		changed = true
	}
	if changed {
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		slug += "-" + strconv.FormatUint(uint64(h.Sum32()), 16)
	}
	return "queue-" + slug + ".json"
}

func (s *Session) statePath() string {
	return filepath.Join(s.app.stateDir(), stateFileName(s.speaker.ID))
}

// loadState 在 newSession 里调（此时 player 已就绪、还没起任何 goroutine）。
func (s *Session) loadState() {
	if !s.app.cfg.QueuePersist {
		return
	}
	path := s.statePath()
	var f stateFile
	ok, err := util.ReadJSONFile(path, &f)
	if err != nil {
		log.Printf("[%s] ♻️ 读取播放队列快照失败（%s）：%v", s.speaker.Name, path, err)
		return
	}
	if !ok {
		return
	}
	if f.Version != stateVersion {
		log.Printf("[%s] ♻️ 快照版本 %d 不认（当前 %d），忽略 %s", s.speaker.Name, f.Version, stateVersion, path)
		return
	}
	if len(f.Queue.Tracks) == 0 {
		return
	}
	n := len(f.Queue.Tracks)
	s.q.FromState(f.Queue)
	v := s.q.View(0)
	if v.Current == nil {
		return
	}
	s.stMu.Lock()
	s.stHadQueue = true
	s.stMu.Unlock()

	// 窗口边界：只在还在窗口范围内时才认，否则保持 -1（下次推窗会重算）
	if s.windowMode {
		s.hotMu.Lock()
		if f.Hotmode.WinFrom >= 0 && f.Hotmode.WinFrom < v.Total {
			s.winFrom = f.Hotmode.WinFrom
		}
		if f.Hotmode.PushedTo >= 0 && f.Hotmode.PushedTo < v.Total {
			s.pushedTo = f.Hotmode.PushedTo
		}
		s.winAdvanced = f.Hotmode.WinAdvanced
		s.winLen = f.Hotmode.WinLen
		if f.Hotmode.StopReason != "" {
			s.hotStopReason = f.Hotmode.StopReason
		}
		s.hotMu.Unlock()
	}
	// 心动续杯：配置里关掉续杯（hotmode_extend_below <= 0）就别把 on 恢复回来，
	// 否则只剩“反复重推同一窗”这种莫名其妙的行为。
	if f.Hotmode.On && f.Hotmode.PID != "" && s.app.cfg.HotmodeExtendBelow > 0 {
		s.StartHotmode(f.Hotmode.PID)
		s.hotMu.Lock()
		s.hotPushed = f.Hotmode.Pushed
		s.hotFails = f.Hotmode.Fails
		s.hotMu.Unlock()
	}
	if f.Volume.Value >= 0 && f.Volume.Value <= 100 {
		s.volMu.Lock()
		s.volume, s.muted, s.mutePrev = f.Volume.Value, f.Volume.Muted, f.Volume.Prev
		s.volMu.Unlock()
	}
	log.Printf("[%s] ♻️ 已恢复播放队列：%d 首，当前 [%d/%d] %s @%s（快照 %d 首，%s 存于 %s）",
		s.speaker.Name, v.Total, v.Index+1, v.Total, v.Current.Label(),
		fmt.Sprintf("%d:%02d", v.PositionMs/60000, v.PositionMs/1000%60),
		n, filepath.Base(path),
		time.Unix(f.SavedAt, 0).Format("2006-01-02 15:04"))
}

// persistSig 是“值不值得再写一次盘”的判据：只含权威数据 + 3 分钟分桶的位置。
// （events.go 里那个 stateSig 是给 SSE 判“要不要推帧”的，两者无关。）
func (s *Session) persistSig() string {
	v := s.q.View(0)
	var cur int64
	if v.Current != nil {
		cur = v.Current.ID
	}
	bucket := 0
	if v.Playing {
		bucket = v.PositionMs / statePosBucketMs
	}
	vol, muted := s.VolumeState()

	s.hotMu.Lock()
	on, pushed, pushedTo := s.hotOn, s.hotPushed, s.pushedTo
	s.hotMu.Unlock()
	s.volMu.Lock()
	prev := s.mutePrev
	s.volMu.Unlock()

	return fmt.Sprintf("%d|%d|%d|%d|%s|%s|%v|%v|%v|%d|%d|%d|%v|%d",
		v.Total, v.Index, cur, bucket, v.Repeat, v.Source, v.Shuffle, v.Playing,
		on, pushed, pushedTo, vol, muted, prev)
}

func (s *Session) collectState() stateFile {
	f := stateFile{
		Version: stateVersion,
		Speaker: s.speaker.ID,
		SavedAt: time.Now().Unix(),
		Queue:   s.q.ToState(),
	}
	s.hotMu.Lock()
	f.Hotmode = hotSnapshot{
		On: s.hotOn, PID: s.hotPID, Pushed: s.hotPushed, Fails: s.hotFails,
		WinFrom: s.winFrom, PushedTo: s.pushedTo, WinAdvanced: s.winAdvanced,
		WinLen: s.winLen, StopReason: s.hotStopReason,
	}
	s.hotMu.Unlock()
	s.volMu.Lock()
	f.Volume = volumeSnapshot{Value: s.volume, Muted: s.muted, Prev: s.mutePrev}
	s.volMu.Unlock()
	return f
}

// saveStateIfChanged 由 runScheduler 每拍调一次。
func (s *Session) saveStateIfChanged() {
	if !s.app.cfg.QueuePersist {
		return
	}
	sig := s.persistSig()
	s.stMu.Lock()
	last := s.stSig
	s.stMu.Unlock()
	if sig == last {
		return
	}
	s.writeState()
}

// flushState 在退出（ctx 取消）时补一次，把最后这段进度写下去。
func (s *Session) flushState() {
	if !s.app.cfg.QueuePersist {
		return
	}
	s.stMu.Lock()
	had := s.stHadQueue
	s.stMu.Unlock()
	if len(s.q.Snapshot()) == 0 && !had {
		return
	}
	s.writeState()
}

func (s *Session) writeState() {
	f := s.collectState()
	s.stMu.Lock()
	had := s.stHadQueue
	s.stMu.Unlock()
	// 这台音箱从头到尾没放过东西 → 不制造空文件
	if len(f.Queue.Tracks) == 0 && !had {
		return
	}
	path := s.statePath()
	if err := util.WriteJSONFile(path, f); err != nil {
		log.Printf("[%s] ♻️ 播放队列落盘失败（%s）：%v", s.speaker.Name, path, err)
		return
	}
	sig := s.persistSig()
	s.stMu.Lock()
	s.stSavedAt = time.Now()
	s.stSig = sig
	s.stHadQueue = len(f.Queue.Tracks) > 0 || had
	s.stMu.Unlock()
}

// persistInfo 给 /api/state，排查“重启后为什么没恢复”时直接看这里。
func (s *Session) persistInfo() map[string]any {
	if !s.app.cfg.QueuePersist {
		return nil
	}
	s.stMu.Lock()
	had, saved := s.stHadQueue, s.stSavedAt
	s.stMu.Unlock()
	out := map[string]any{"path": s.statePath(), "had_queue": had}
	if !saved.IsZero() {
		out["saved_at"] = saved.Unix()
	}
	return out
}
