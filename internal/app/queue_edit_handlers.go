package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// 队列编辑接口（WebUI 播放器页用）：删除一首、把一首拖到别处。
//
// 这两个操作改的是服务端的“权威队列”，所以每次都要让设备侧跟上，见 queue_edit.go。
// 注意响应恒为 200：队列已经改了，设备侧同步失败只是“听起来还是旧列表”，
// 返回 4xx 会让前端以为整个操作没发生。失败信息放在 warning 里。

// queueEditResponse 统一两处编辑接口的返回体。
func (a *App) queueEditResponse(w http.ResponseWriter, s *Session, warning error, extra map[string]any) {
	v := s.view(5)
	out := map[string]any{
		"ok":      true,
		"speaker": s.speaker.ID,
		"queue":   v,
		"index":   v.Index,
		"total":   v.Total,
		"playing": v.Playing,
	}
	for k, val := range extra {
		out[k] = val
	}
	if warning != nil {
		out["warning"] = warning.Error()
	}
	writeJSON(w, 200, out)
}

// hQueueRemove 删除队列第 index 首。
func (a *App) hQueueRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index   int  `json:"index"`
		All     bool `json:"all"` // true = 清空队列（停止播放）
		Speaker string
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.All {
		if err := s.player.Stop(r.Context()); err != nil {
			writeErr(w, 502, err)
			return
		}
		s.q.Reset()
		s.clearPendingEdit()
		s.probeSoon("清空队列")
		a.queueEditResponse(w, s, nil, map[string]any{"removed": 0, "cleared": true})
		return
	}

	before, _, _ := s.q.Current()
	curBefore := before.ID
	posBefore := s.q.PositionMs()
	wasPlaying := s.q.View(0).Playing
	removed, _, currentRemoved, ok := s.q.RemoveAt(req.Index)
	if !ok {
		writeErr(w, 400, fmt.Errorf("队列为空或下标越界（index=%d）", req.Index))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var warning error
	if err := s.applyQueueEdit(ctx, curBefore, posBefore, wasPlaying); err != nil {
		warning = fmt.Errorf("队列已删除，但设备侧同步失败：%w", err)
		log.Printf("[%s] %v", s.speaker.Name, warning)
	}
	a.queueEditResponse(w, s, warning, map[string]any{
		"removed_id": removed.ID,
		"current":    currentRemoved,
	})
}

// hQueueMove 把第 from 首移到第 to 首。
func (a *App) hQueueMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From    int `json:"from"`
		To      int `json:"to"`
		Speaker string
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
	}
	if req.From == req.To {
		a.queueEditResponse(w, s, nil, map[string]any{"moved": false})
		return
	}
	if req.From < 0 || req.From >= s.q.Len() || req.To < 0 || req.To >= s.q.Len() {
		writeErr(w, 400, fmt.Errorf("下标越界（from=%d to=%d，队列 %d 首）", req.From, req.To, s.q.Len()))
		return
	}

	before, _, _ := s.q.Current()
	curBefore := before.ID
	posBefore := s.q.PositionMs()
	wasPlaying := s.q.View(0).Playing
	if _, ok := s.q.MoveAt(req.From, req.To); !ok {
		writeErr(w, 400, fmt.Errorf("移动失败（from=%d to=%d）", req.From, req.To))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var warning error
	if err := s.applyQueueEdit(ctx, curBefore, posBefore, wasPlaying); err != nil {
		warning = fmt.Errorf("队列已重排，但设备侧同步失败：%w", err)
		log.Printf("[%s] %v", s.speaker.Name, warning)
	}
	a.queueEditResponse(w, s, warning, map[string]any{"moved": true, "from": req.From, "to": req.To})
}
