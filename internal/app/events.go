package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SSE 事件通道：**让 xiaoai-mb 成为唯一的权威状态源**。
//
// 为什么要它：设备端不会主动上报（实测见 docs/MEDIA-ARCH.md §8.4.3），
// 但 xiaoai-mb 手里已经有真值 + 500ms 的调度循环。以前 HA 每 60 秒来读一次缓存，
// 于是“机身按键/米家操作”这类外部变更要等最后一跳才显现。
// 现在 xiaoai-mb 把有意义的变化直接推给订阅者，HA 那一跳的延迟归零。
//
// 为什么用 SSE 而不是 WebSocket：这条通道是单向的（服务端→客户端），
// SSE 就是普通 HTTP，不用握手、不用保活协议、穿过 nginx 反代只要不缓冲；
// 断线重连的逻辑客户端一行就够。
//
// 事件只有两种业务 topic，都是**完整快照**（不做 diff），所以丢一帧也不会错：
//
//	event: state   data: {"ok":true,"speaker":"living","queue":{...},"device":{...},"volume":19,...}
//	event: lyric   data: {"speaker":"living","lyric":{"line":"...","index":5,...}}
//
// 位置（position_ms）故意不参与“变化判定”——它每秒都在变，推它会变成每秒一次的
// 状态写入；进度仍由客户端按锚点插值。

const (
	sseHeartbeat = 20 * time.Second
	sseQueue     = 8 // 慢消费者只保留最新一帧，不能把服务端内存吃满
)

type sseEvent struct {
	Topic   string
	Speaker string
	Payload map[string]any
}

type eventHub struct {
	mu   sync.Mutex
	subs map[chan sseEvent]struct{}
}

func (h *eventHub) subscribe() (<-chan sseEvent, func()) {
	ch := make(chan sseEvent, sseQueue)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[chan sseEvent]struct{}{}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// publish 非阻塞：订阅者来不及收就丢帧（下一帧是完整快照，丢一帧无害）。
func (h *eventHub) publish(ev sseEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// 变化签名：只包含“有意义”的字段，位置/时间戳不在内。
func (s *Session) stateSig() string {
	v := s.view(0)
	var id int64
	if v.Current != nil {
		id = v.Current.ID
	}
	st, _, ok, errMsg := s.device.get()
	vol, muted := s.VolumeState()
	alarmVol, alarmKnown := s.AlarmVolumeState()
	// 在线/离线也要能推出去（HA 的 available 依赖它）。
	// v.Unreachable 已经通过 v.Playing 的冻结间接变了，但这里显式带上：
	// 判离线的那一刻必须保证签名变，否则 HA 收不到 available=false 那一帧。
	return fmt.Sprintf("%d|%d|%d|%v|%v|%s|%v|%s|%d|%v|%d|%d|%v|%s|%v|%d|%v",
		id, v.Index, v.Total, v.Playing, v.Liked, v.Repeat, v.Shuffle,
		v.Source, vol, muted, st.Status, st.MediaType, ok, errMsg, alarmKnown, alarmVol, v.Unreachable)
}

// emitStateIfChanged 由调度循环每 tick 调一次；签名变了就推一帧全量快照。
func (s *Session) emitStateIfChanged() {
	sig := s.stateSig()
	if sig == s.lastSig {
		return
	}
	first := s.lastSig == ""
	s.lastSig = sig
	if first { // 启动后的第一帧不算“变化”，别刷屏
		return
	}
	log.Printf("[%s] 📡 状态变化已推送：%s", s.speaker.Name, sigBrief(sig))
	s.app.hub.publish(sseEvent{Topic: "state", Speaker: s.speaker.ID, Payload: s.statePayload(context.Background(), false)})
}

// statePayload 是 /api/state 与 SSE 共用的快照。
func (s *Session) statePayload(ctx context.Context, freshProbe bool) map[string]any {
	if freshProbe {
		s.triggerProbeWait(ctx)
	}
	out := map[string]any{"ok": true, "speaker": s.speaker.ID, "queue": s.view(5), "poll": s.pollDebug()}
	// 顶层 unreachable：设备不可达（推送静默超时）时为 true。
	// 单独放顶层是因为 device 块在数据过期后会整块消失，HA 需要一个
	// 不依赖 device 块存在的、稳定的“这台音箱现在问不到”的信号。
	if off, _ := s.unreachableView(); off {
		out["unreachable"] = true
	}
	// 心动状态：开着、或曾经开过（pushed>0 / 有停止原因）都要推——
	// give-up 后 on=false，chip 不能直接消失，UI 要能说清“为什么没了”。
	if on, fails, pushed, stopReason := s.HotmodeState(); on || pushed > 0 || stopReason != "" {
		hot := map[string]any{
			"on":          on,
			"fails":       fails,
			"extended":    pushed,
			"below":       s.app.cfg.HotmodeExtendBelow,
			"stop_reason": stopReason,
		}
		if stopReason != "" {
			hot["stopped"] = true
		}
		out["hotmode"] = hot
	}
	vol, muted := s.VolumeState()
	out["volume"] = vol
	out["muted"] = muted
	if p := s.persistInfo(); p != nil {
		out["persist"] = p
	}
	if alarmVol, ok := s.AlarmVolumeState(); ok {
		out["alarm_volume"] = alarmVol
	}
	if st, ok := s.deviceFresh(); ok {
		poll := int(s.probeEvery / time.Second)
		if poll <= 0 {
			poll = s.speaker.StatusPoll
		}
		out["device"] = map[string]any{
			"audio_id":    st.AudioID,
			"status":      st.Status,
			"playing":     st.Playing(),
			"position_ms": st.Position,
			"duration_ms": st.Duration,
			"volume":      st.Volume,
			"loop_type":   st.LoopType,
			"media_type":  st.MediaType,
			"global_id":   st.GlobalID,
			"track_id":    st.TrackID(),
			"last_ok":     true,
			"poll_sec":    poll,
		}
	}
	return out
}

// lyricSnapshot 是当前曲正在唱的那一行（无曲/无词返回空视图）。
func (s *Session) lyricSnapshot() lyricSnapshot {
	v := s.q.View(0)
	if v.Current == nil {
		return lyricSnapshot{}
	}
	lines := s.app.LyricFor(v.Current.ID)
	idx, cur, next := lyricAt(lines, v.PositionMs)
	return lyricSnapshot{SongID: v.Current.ID, Line: cur, NextLine: next,
		Index: idx, Total: len(lines), PositionMs: v.PositionMs}
}

// emitLyricIfChanged：歌词行号变了才推（换曲也算变）。
func (s *Session) emitLyricIfChanged() {
	snap := s.lyricSnapshot()
	key := fmt.Sprintf("%d|%d", snap.SongID, snap.Index)
	if key == s.lastLyric {
		return
	}
	s.lastLyric = key
	s.app.hub.publish(sseEvent{Topic: "lyric", Speaker: s.speaker.ID,
		Payload: map[string]any{"ok": true, "speaker": s.speaker.ID, "lyric": snap}})
}

// hEvents 是 SSE 端点：GET /api/events[?speaker=living|all]
func (a *App) hEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, fmt.Errorf("当前响应不支持流式输出"))
		return
	}
	want := r.URL.Query().Get("speaker")
	if want == "" {
		want = "all"
	}
	ch, unsubscribe := a.hub.subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx：别缓冲
	w.WriteHeader(http.StatusOK)

	// 连上先给每台一帧当前态，客户端不用等第一次变化
	for _, id := range a.order {
		if want != "all" && want != id {
			continue
		}
		s := a.sessions[id]
		writeSSE(w, flusher, sseEvent{Topic: "state", Speaker: id, Payload: s.statePayload(context.Background(), false)})
		writeSSE(w, flusher, sseEvent{Topic: "lyric", Speaker: id,
			Payload: map[string]any{"ok": true, "speaker": id, "lyric": s.lyricSnapshot()}})
	}

	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-ch:
			if want != "all" && ev.Speaker != want {
				continue
			}
			writeSSE(w, flusher, ev)
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, ev sseEvent) {
	raw, err := json.Marshal(ev.Payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Topic, raw)
	flusher.Flush()
}

// sigBrief 把签名前几段转成一句人话，日志里够看就行。
func sigBrief(sig string) string {
	f := strings.SplitN(sig, "|", 8)
	if len(f) < 7 {
		return sig
	}
	return fmt.Sprintf("song=%s idx=%s/%s playing=%s liked=%s repeat=%s shuffle=%s",
		f[0], f[1], f[2], f[3], f[4], f[5], f[6])
}
