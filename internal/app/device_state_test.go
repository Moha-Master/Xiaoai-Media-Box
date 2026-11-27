package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
)

func newTestPushApp() (*App, *Session) {
	s := &Session{
		speaker: config.SpeakerConfig{
			ID:         "living",
			Name:       "客厅",
			Control:    "ssh",
			StatusMode: "push",
			StatusPoll: -1,
		},
		player:     player.NewNoop(),
		q:          queue.NewQueue(queue.LoopList),
		probeEvery: 15 * time.Second,
	}
	app := &App{
		sessions: map[string]*Session{"living": s},
		order:    []string{"living"},
	}
	s.app = app
	return app, s
}

func TestDeviceStatePush(t *testing.T) {
	app, s := newTestPushApp()
	handler := app.Routes()

	// 模拟 ubus 原始输出
	ubusPayload := `{
		"code": 0,
		"info": "{ \"status\": 1, \"volume\": 25, \"loop_type\": 1, \"media_type\": 3, \"play_song_detail\": { \"audio_id\": \"123456\", \"position\": 15000, \"duration\": 200000 }, \"track_list\": [ \"123456\" ] }"
	}`

	// 1. 通过 URL 参数 POST /api/device/state?speaker=living
	req := httptest.NewRequest("POST", "/api/device/state?speaker=living", bytes.NewBufferString(ubusPayload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/device/state 返回 %d: %s", rec.Code, rec.Body.String())
	}

	st, fresh := s.deviceFresh()
	if !fresh {
		t.Fatal("push 之后 deviceFresh 应为 true")
	}
	if st.Status != 1 || st.Volume != 25 || st.AudioID != "123456" || st.Position != 15000 {
		t.Fatalf("状态解析不符合预期: %+v", st)
	}
	vol, _ := s.VolumeState()
	if vol != 25 {
		t.Fatalf("设备音量应同步到 session，期望 25，实际 %d", vol)
	}

	// 2. 通过 X-Speaker 标头 POST /api/device-state
	ubusPayload2 := `{
		"code": 0,
		"info": "{ \"status\": 2, \"volume\": 30, \"play_song_detail\": { \"audio_id\": \"123456\", \"position\": 16000, \"duration\": 200000 } }"
	}`
	req2 := httptest.NewRequest("POST", "/api/device-state", bytes.NewBufferString(ubusPayload2))
	req2.Header.Set("X-Speaker", "living")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("POST /api/device-state 返回 %d: %s", rec2.Code, rec2.Body.String())
	}
	st2, _ := s.deviceFresh()
	if st2.Status != 2 || st2.Volume != 30 || st2.Position != 16000 {
		t.Fatalf("状态二次更新失败: %+v", st2)
	}

	// 3. 检查 pollDebug
	dbg := s.pollDebug()
	if dbg["mode"] != "push" || dbg["healthy"] != true {
		t.Fatalf("pollDebug 结果不符: %+v", dbg)
	}
}

func TestDeviceStateInvalidJSON(t *testing.T) {
	app, _ := newTestPushApp()
	handler := app.Routes()

	req := httptest.NewRequest("POST", "/api/device/state?speaker=living", bytes.NewBufferString("not json"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，实际 %d", rec.Code)
	}
}
