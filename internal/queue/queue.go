package queue

import (
	"math/rand"
	"sync"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
)

type LoopMode string

const (
	LoopNone   LoopMode = "none"   // 不循环：播到队尾就停
	LoopList   LoopMode = "list"   // 列表循环
	LoopSingle LoopMode = "single" // 单曲循环（自然播完重放，手动 next 仍前进）
)

// ParseLoop 解析 repeat 语义。旧值 "shuffle" 仍然认——它等价于
// “列表循环 + 随机”（随机现在是独立开关，见 Queue.SetShuffle）。
func ParseLoop(s string) (LoopMode, bool) {
	switch LoopMode(s) {
	case LoopNone, LoopList, LoopSingle:
		return LoopMode(s), true
	case "shuffle":
		return LoopList, true
	}
	return LoopList, false
}

// WantsShuffle 判断一个旧式 mode 字符串是否要求打开随机。
func WantsShuffle(s string) bool { return LoopMode(s) == "shuffle" }

// Queue 是服务端的“队列真源”。设备侧只是它的一个渲染窗口。
type Queue struct {
	mu      sync.RWMutex
	tracks  []netease.Track
	index   int
	loop    LoopMode
	shuffle bool
	source  string
	playing bool
	started time.Time // 当前曲开始时间（playing 时有效）
	basePos int       // 进入当前曲前已累计的毫秒（暂停/切歌用）
}

func NewQueue(loop LoopMode) *Queue {
	return &Queue{loop: loop}
}

func Wrap(i, n int) int {
	if n == 0 {
		return 0
	}
	return ((i % n) + n) % n
}

// Replace 用新列表顶掉整个队列。
func (q *Queue) Replace(tracks []netease.Track, source string, start int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = tracks
	q.source = source
	q.index = Wrap(start, len(tracks))
	q.playing = false
	q.basePos = 0
	q.started = time.Time{}
}

// Append 把曲目追加到队尾，返回第一首的下标（队列原本为空时返回 0）。
func (q *Queue) Append(tracks []netease.Track) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(tracks) == 0 {
		return -1
	}
	start := len(q.tracks)
	q.tracks = append(q.tracks, tracks...)
	return start
}

// InsertAfterCurrent 把曲目插到当前曲后面（当前为空则等价于 Append），返回插入起点。
func (q *Queue) InsertAfterCurrent(tracks []netease.Track) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(tracks) == 0 {
		return -1
	}
	at := len(q.tracks)
	if q.index >= 0 && q.index < len(q.tracks) {
		at = q.index + 1
	}
	head := make([]netease.Track, 0, len(q.tracks)+len(tracks))
	head = append(head, q.tracks[:at]...)
	head = append(head, tracks...)
	head = append(head, q.tracks[at:]...)
	q.tracks = head
	return at
}

// Current 返回当前曲目与其下标。
func (q *Queue) Current() (netease.Track, int, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if len(q.tracks) == 0 || q.index < 0 || q.index >= len(q.tracks) {
		return netease.Track{}, -1, false
	}
	return q.tracks[q.index], q.index, true
}

func (q *Queue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.tracks)
}

func (q *Queue) Loop() LoopMode {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.loop
}

func (q *Queue) SetLoop(m LoopMode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.loop = m
}

func (q *Queue) Shuffle() bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.shuffle
}

func (q *Queue) SetShuffle(v bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuffle = v
}

// LoopLabel 是给外部看的循环标签：开着随机时统一报 "shuffle"，
// 这样旧版 WebUI（三选一按钮）不用改也能正确显示。
func LoopLabel(loop LoopMode, shuffle bool) string {
	if shuffle {
		return "shuffle"
	}
	return string(loop)
}

// Move 前进 delta 首（delta 可为负），返回新的当前曲目。
// natural=true 表示“这一首自己播完了”，此时单曲循环会原地重放。
// 返回 ok=false 表示队列走到头且不循环了（调用方应当停下播放）。
func (q *Queue) Move(delta int, natural bool) (netease.Track, int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.tracks) == 0 {
		return netease.Track{}, -1, false
	}
	switch {
	case natural && q.loop == LoopSingle:
		// 单曲循环：原地重放（手动 next/prev 不受影响）
		q.index = Wrap(q.index, len(q.tracks))
	case q.shuffle:
		if len(q.tracks) == 1 {
			if q.loop == LoopNone && natural {
				q.playing = false
				q.basePos = 0
				q.started = time.Time{}
				return netease.Track{}, -1, false
			}
			q.index = 0
		} else {
			n := q.index
			for n == q.index {
				n = rand.Intn(len(q.tracks))
			}
			q.index = n
		}
	case q.loop == LoopNone:
		next := q.index + delta
		if next < 0 || next >= len(q.tracks) {
			// 到头了：停在原地，不绕回
			q.playing = false
			q.basePos = 0
			q.started = time.Time{}
			return netease.Track{}, -1, false
		}
		q.index = next
	default:
		q.index = Wrap(q.index+delta, len(q.tracks))
	}
	q.playing = false
	q.basePos = 0
	q.started = time.Time{}
	return q.tracks[q.index], q.index, true
}

// RemoveAt 删除第 i 首，返回被删曲目、删除后的当前下标，以及是否删掉了当前曲。
//
// 下标语义对齐常见播放器：删的是当前曲之前的，当前曲不变、下标左移；删的就是当前曲，
// 当前曲变成原来的“下一首”（删的是最后一首时退到前一首、并把播放态清掉，等调用方决定停还是重推）。
func (q *Queue) RemoveAt(i int) (netease.Track, int, bool, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if i < 0 || i >= len(q.tracks) {
		return netease.Track{}, -1, false, false
	}
	removed := q.tracks[i]
	q.tracks = append(q.tracks[:i], q.tracks[i+1:]...)
	if len(q.tracks) == 0 {
		q.index = 0
		q.playing = false
		q.basePos = 0
		q.started = time.Time{}
		return removed, -1, true, true
	}
	switch {
	case i < q.index:
		q.index--
		return removed, q.index, false, true
	case i == q.index:
		// 当前曲被删：下标不用动，指向原来的下一首；删的是末尾则退到前一首。
		if q.index >= len(q.tracks) {
			q.index = len(q.tracks) - 1
			q.playing = false // 后面没歌了，别假装还在播
		}
		q.basePos = 0
		if q.playing {
			q.started = time.Now()
		} else {
			q.started = time.Time{}
		}
		return removed, q.index, true, true
	}
	return removed, q.index, false, true
}

// MoveAt 把第 from 首移到第 to 首（to 是移动后的目标下标）。
// 当前曲目的身份保持不变（动到它前后的下标会相应平移），播放位置也不变。
func (q *Queue) MoveAt(from, to int) (int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.tracks)
	if from < 0 || from >= n || to < 0 || to >= n || from == to {
		return q.index, false
	}
	t := q.tracks[from]
	q.tracks = append(q.tracks[:from], q.tracks[from+1:]...)
	q.tracks = append(q.tracks, netease.Track{})
	copy(q.tracks[to+1:], q.tracks[to:])
	q.tracks[to] = t

	switch {
	case from == q.index:
		q.index = to
	case from < q.index && to >= q.index:
		q.index--
	case from > q.index && to <= q.index:
		q.index++
	}
	return q.index, true
}

// Jump 直接跳到某首。
func (q *Queue) Jump(index int) (netease.Track, int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.tracks) == 0 {
		return netease.Track{}, -1, false
	}
	q.index = Wrap(index, len(q.tracks))
	q.playing = false
	q.basePos = 0
	q.started = time.Time{}
	return q.tracks[q.index], q.index, true
}

func (q *Queue) MarkPlaying() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.playing = true
	q.started = time.Now()
}

func (q *Queue) MarkPaused() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.playing {
		q.basePos += int(time.Since(q.started).Milliseconds())
	}
	q.playing = false
	q.started = time.Time{}
}

func (q *Queue) Reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = nil
	q.index = 0
	q.playing = false
	q.basePos = 0
	q.started = time.Time{}
	q.source = ""
}

// PositionMs 估算当前播放位置：设备侧没有可靠的回读通道时，这是唯一来源。
func (q *Queue) PositionMs() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	pos := q.basePos
	if q.playing && !q.started.IsZero() {
		pos += int(time.Since(q.started).Milliseconds())
	}
	return pos
}

// SyncPosition 用设备回报的位置纠偏。设备的 position 是权威值，
// 我们只是把它变成“从这个位置开始以本地时钟继续走”。
func (q *Queue) SyncPosition(posMs int, playing bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if posMs < 0 {
		posMs = 0
	}
	q.basePos = posMs
	q.playing = playing
	if playing {
		q.started = time.Now()
	} else {
		q.started = time.Time{}
	}
}

// FindByID 按曲目 id 找下标：设备回报的 audio_id 就是它。
func (q *Queue) FindByID(id int64) (netease.Track, int, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	for i, t := range q.tracks {
		if t.ID == id {
			return t, i, true
		}
	}
	return netease.Track{}, -1, false
}

// At 取第 i 首（越界返回 false）。
func (q *Queue) At(i int) (netease.Track, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if i < 0 || i >= len(q.tracks) {
		return netease.Track{}, false
	}
	return q.tracks[i], true
}

// Snapshot 复制一份队列，供推窗时使用。
func (q *Queue) Snapshot() []netease.Track {
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := make([]netease.Track, len(q.tracks))
	copy(out, q.tracks)
	return out
}

// Index 返回当前下标。
func (q *Queue) Index() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.index
}

// SetPlayable 把 resolve 学到的试听区间写进队列曲目。
//
// 返回是否真的改了什么（幂等：重复写入同样的区间返回 false）。
// 若改的是**当前曲**，必须重新锚定位置——设备此时播的是截断后的片段文件，
// 它的 0 秒 = 原曲的 start 处，所以队列里记的位置要从「片段轴」换算回
// 「原曲轴」：position += start。幂等性靠 start 保护：换算后位置必然 ≥ start，
// 再次调用时不再叠加（否则每轮 resolve 都会把进度条 +69 秒）。
func (q *Queue) SetPlayable(id int64, startMs, endMs int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, t := range q.tracks {
		if t.ID != id {
			continue
		}
		if t.TrialStartMs == startMs && t.TrialEndMs == endMs {
			return false // 已经写过，别重复锚定
		}
		if i == q.index {
			// 当前曲第一次学到区间：此刻位置还是「片段轴」（设备在片段的 0 秒 =
			// 原曲的 start 处），换算回原曲轴。幂等性由上面的等值早退保证：
			// 只有第一次写入才会走到这里，不会重复叠加。
			q.basePos += startMs
		}
		q.tracks[i].TrialStartMs = startMs
		q.tracks[i].TrialEndMs = endMs
		return true
	}
	return false
}

// TrimHistory 裁掉当前曲之前更早的历史（只保留 keepBack 首），返回从队首移除的条数。
//
// 无限心动每轮追加百余首、队列只增不减，不裁几个月就是几千首。
// 裁剪后 index 由本方法平移；调用方（session）必须把自己记的窗口下标
// （winFrom/pushedTo）同步平移——这里不知道它们的存在。
func (q *Queue) TrimHistory(keepBack int) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.tracks)
	if n == 0 || q.index < 0 || q.index >= n {
		return 0
	}
	if keepBack < 0 {
		keepBack = 0
	}
	removed := q.index - keepBack
	if removed <= 0 {
		return 0
	}
	q.tracks = append([]netease.Track(nil), q.tracks[removed:]...)
	q.index -= removed
	return removed
}

type QueueView struct {
	Source     string `json:"source"`
	Loop       string `json:"loop"`   // 展示用标签：开随机时报 "shuffle"
	Repeat     string `json:"repeat"` // 真实循环语义：none / list / single（与随机正交）
	Shuffle    bool   `json:"shuffle"`
	Playing    bool   `json:"playing"`
	Index      int    `json:"index"`
	Total      int    `json:"total"`
	Liked      bool   `json:"liked"` // 当前曲是否已红心（由 app.Session 填）
	PositionMs int    `json:"position_ms"`
	DurationMs int    `json:"duration_ms"`
	// Unreachable=true 表示设备此刻不可达（推送静默超时），此时
	// Playing/PositionMs 都是**冻结的最后真值**，客户端应当显示「未知」而不是
	// 「暂停 + 某个进度」——把冻结值当成真值会让进度条停在错误位置一直不动。
	Unreachable bool            `json:"unreachable,omitempty"`
	Current     *netease.Track  `json:"current,omitempty"`
	Upcoming    []netease.Track `json:"upcoming,omitempty"`
}

// QueueFullView 是给 WebUI / HA 用的完整队列：在 QueueView 之上多带一份全部曲目。
type QueueFullView struct {
	QueueView
	Tracks []netease.Track `json:"tracks"`
}

// FullView 返回完整队列（含当前下标）。曲目列表是拷贝，调用方随便改。
func (q *Queue) FullView() QueueFullView {
	q.mu.RLock()
	defer q.mu.RUnlock()
	v := QueueFullView{QueueView: QueueView{
		Source:  q.source,
		Loop:    LoopLabel(q.loop, q.shuffle),
		Repeat:  string(q.loop),
		Shuffle: q.shuffle,
		Playing: q.playing,
		Index:   q.index,
		Total:   len(q.tracks),
	}}
	v.PositionMs = q.basePos
	if q.playing && !q.started.IsZero() {
		v.PositionMs += int(time.Since(q.started).Milliseconds())
	}
	if len(q.tracks) > 0 && q.index >= 0 && q.index < len(q.tracks) {
		cur := q.tracks[q.index]
		v.Current = &cur
		v.DurationMs = cur.Duration
	}
	v.Tracks = make([]netease.Track, len(q.tracks))
	copy(v.Tracks, q.tracks)
	return v
}

func (q *Queue) View(window int) QueueView {
	q.mu.RLock()
	defer q.mu.RUnlock()
	v := QueueView{
		Source:  q.source,
		Loop:    LoopLabel(q.loop, q.shuffle),
		Repeat:  string(q.loop),
		Shuffle: q.shuffle,
		Playing: q.playing,
		Index:   q.index,
		Total:   len(q.tracks),
	}
	v.PositionMs = q.basePos
	if q.playing && !q.started.IsZero() {
		v.PositionMs += int(time.Since(q.started).Milliseconds())
	}
	if len(q.tracks) > 0 && q.index >= 0 && q.index < len(q.tracks) {
		cur := q.tracks[q.index]
		v.Current = &cur
		v.DurationMs = cur.Duration
		if window > 0 {
			for i := 1; i <= window && i < len(q.tracks); i++ {
				v.Upcoming = append(v.Upcoming, q.tracks[Wrap(q.index+i, len(q.tracks))])
			}
		}
	}
	return v
}
