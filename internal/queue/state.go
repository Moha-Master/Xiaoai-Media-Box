package queue

import (
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
)

// StateTrackCap 是一次落盘的曲目上限。无限心动让队列只增不减，几个月就是几千首，
// 落盘时只保留当前曲前后一段（对使用没有影响：那之后的歌本来也要重新推荐）。
const StateTrackCap = 1000

// State 是队列的落盘快照，只装权威数据。
//
// `started` 是墙上时钟，跨进程复用只会让进度条乱跳，所以位置落成 `position_ms`；
// 红心、直链、设备侧 audio_id、窗口切片都是派生态，随时能重算，不落盘。
type State struct {
	Tracks     []netease.Track `json:"tracks"`
	Index      int             `json:"index"`
	Loop       string          `json:"loop"`
	Shuffle    bool            `json:"shuffle"`
	Source     string          `json:"source"`
	Playing    bool            `json:"playing"`
	PositionMs int             `json:"position_ms"`
}

// ToState 导出可落盘的快照（超长时裁中段，Index 跟着平移）。
func (q *Queue) ToState() State {
	q.mu.RLock()
	defer q.mu.RUnlock()
	pos := q.basePos
	if q.playing && !q.started.IsZero() {
		pos += int(time.Since(q.started).Milliseconds())
	}
	st := State{
		Index:      q.index,
		Loop:       string(q.loop),
		Shuffle:    q.shuffle,
		Source:     q.source,
		Playing:    q.playing,
		PositionMs: pos,
	}
	n := len(q.tracks)
	if n == 0 {
		st.Index = 0
		return st
	}
	start := 0
	if n > StateTrackCap {
		start = q.index - 100 // 前面留 100 首历史，prev 还有得退
		if start < 0 {
			start = 0
		}
		if m := n - StateTrackCap; start > m {
			start = m
		}
	}
	end := start + StateTrackCap
	if end > n {
		end = n
	}
	st.Tracks = make([]netease.Track, end-start)
	copy(st.Tracks, q.tracks[start:end])
	st.Index = q.index - start
	return st
}

// FromState 把快照灌回队列。
//
// `playing` 一律置 false：重启后不该自动出声。位置写进 basePos，于是
//   - 设备端还在播（只有进程重启）→ syncWithDevice 按 audio_id 认领并纠偏；
//   - 设备端也停了（整机重启）→ 列表就绪、暂停，等用户按播放。
func (q *Queue) FromState(st State) {
	tracks := make([]netease.Track, 0, len(st.Tracks))
	for _, t := range st.Tracks {
		if t.ID != 0 {
			tracks = append(tracks, t)
		}
	}
	if len(tracks) == 0 {
		return
	}
	idx := st.Index
	if idx < 0 || idx >= len(tracks) {
		idx = 0
	}
	loop := q.loop
	if l, ok := ParseLoop(st.Loop); ok {
		loop = l
	}
	pos := st.PositionMs
	if dur := tracks[idx].Duration; pos < 0 || (dur > 0 && pos > dur) {
		pos = 0 // 位置比歌还长说明快照过期了，从 0 开始比假装在中间更诚实
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = tracks
	q.index = idx
	q.loop = loop
	q.shuffle = st.Shuffle
	q.source = st.Source
	q.playing = false
	q.started = time.Time{}
	q.basePos = pos
}
