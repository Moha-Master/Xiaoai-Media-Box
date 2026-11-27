package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
)

const adminTestConfig = `{
  "listen": "0.0.0.0:8088",
  "public_base": "http://192.168.1.10:8088",
  "cookie_file": "cookie.json",
  "quality": "exhigh",
  "window": 20,
  "loop": "list",
  "stream_mode": "redirect",
  "queue_persist": true,
  "xiaomi": {"user": "u1", "password": "secret", "token_file": ".mina_token.json"},
  "speakers": [
    {"id": "living", "name": "客厅", "control": "ssh", "ssh_host": "root@192.168.1.20"}
  ]
}`

// adminApp 造一个只够管理面用的 App：不建 session、不连网络。
func adminApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(adminTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return &App{cfg: cfg, startedAt: time.Now()}, path
}

func doJSON(t *testing.T, h http.Handler, method, path string, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	out := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s 返回的不是 JSON（%d）：%s", method, path, w.Code, w.Body.String())
	}
	return w.Code, out
}

func TestAdminConfigGetRedactsSecrets(t *testing.T) {
	a, path := adminApp(t)
	code, body := doJSON(t, a.Routes(), "GET", "/api/admin/config", "")
	if code != 200 {
		t.Fatalf("GET 配置返回 %d：%v", code, body)
	}
	if body["path"] != path {
		t.Fatalf("path 不对：%v", body["path"])
	}
	cfg, _ := body["config"].(map[string]any)
	xiaomi, _ := cfg["xiaomi"].(map[string]any)
	if xiaomi["password"] != "" {
		t.Fatalf("密码必须脱敏，得到 %v", xiaomi["password"])
	}
	secrets, _ := body["secrets_set"].([]any)
	if len(secrets) != 1 || secrets[0] != "xiaomi.password" {
		t.Fatalf("secrets_set 不对：%v", body["secrets_set"])
	}
	if body["requires_restart"] != false {
		t.Fatalf("磁盘与运行态一致时不该要求重启：%v", body["requires_restart"])
	}
	runtime, _ := body["runtime"].(map[string]any)
	if runtime["version"] != version {
		t.Fatalf("runtime.version 不对：%v", runtime["version"])
	}
}

func TestAdminConfigPutPersistsAndReports(t *testing.T) {
	a, path := adminApp(t)
	// 只提交 window 和一个新音箱：没提交的字段必须保持磁盘现值
	body := `{"window": 42, "speakers": [
		{"id": "living", "name": "客厅", "control": "ssh", "ssh_host": "root@192.168.1.20"},
		{"id": "bedroom", "name": "卧室", "control": "noop"}
	]}`
	code, resp := doJSON(t, a.Routes(), "PUT", "/api/admin/config", body)
	if code != 200 {
		t.Fatalf("PUT 返回 %d：%v", code, resp)
	}
	if resp["requires_restart"] != true {
		t.Fatalf("改了配置就必须提示重启：%v", resp)
	}
	changed, _ := resp["changed"].([]any)
	want := map[string]bool{"window": false, "speakers": false}
	for _, k := range changed {
		if _, ok := want[k.(string)]; ok {
			want[k.(string)] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Fatalf("changed 里缺 %s：%v", k, changed)
		}
	}
	if resp["backup"] == "" || resp["backup"] == nil {
		t.Fatal("应返回备份路径")
	}
	sc, _ := resp["speakers_changed"].(map[string]any)
	if added, _ := sc["added"].([]any); len(added) != 1 {
		t.Fatalf("speakers_changed.added 应有一台：%v", sc)
	}

	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("重新 Load: %v", err)
	}
	if back.Window != 42 || len(back.Speakers) != 2 {
		t.Fatalf("落盘不对：window=%d speakers=%d", back.Window, len(back.Speakers))
	}
	if back.Xiaomi.Password != "secret" {
		t.Fatal("网页留空的密码应保留原值")
	}
	if !back.QueuePersist {
		t.Fatal("没提交的 queue_persist 应保持原值")
	}
}

func TestAdminConfigPutRejectsBadInput(t *testing.T) {
	a, _ := adminApp(t)

	code, resp := doJSON(t, a.Routes(), "PUT", "/api/admin/config", `{"speakers":[]}`)
	if code != 400 || !strings.Contains(resp["error"].(string), "至少要有音箱") {
		t.Fatalf("删光音箱应 400，得到 %d：%v", code, resp)
	}

	dup := `{"speakers":[{"id":"same","name":"a","control":"noop"},{"id":"same","name":"b","control":"noop"}]}`
	code, resp = doJSON(t, a.Routes(), "PUT", "/api/admin/config", dup)
	if code != 400 || !strings.Contains(resp["error"].(string), "重复") {
		t.Fatalf("重复 id 应 400，得到 %d：%v", code, resp)
	}

	code, resp = doJSON(t, a.Routes(), "PUT", "/api/admin/config", `{bad json`)
	if code != 400 {
		t.Fatalf("坏 JSON 应 400，得到 %d：%v", code, resp)
	}

	// 拼错的键要报错而不是被静默忽略
	code, resp = doJSON(t, a.Routes(), "PUT", "/api/admin/config", `{"widnow": 50}`)
	if code != 400 || !strings.Contains(resp["error"].(string), "widnow") {
		t.Fatalf("未知字段应 400，得到 %d：%v", code, resp)
	}
}

func TestAdminUnchangedPutDoesNotWrite(t *testing.T) {
	a, path := adminApp(t)
	before, _ := os.Stat(path)
	// 把 GET 拿到的（脱敏）配置原样 PUT 回去：应当判为「无改动」
	_, got := doJSON(t, a.Routes(), "GET", "/api/admin/config", "")
	raw, err := json.Marshal(got["config"])
	if err != nil {
		t.Fatal(err)
	}
	code, resp := doJSON(t, a.Routes(), "PUT", "/api/admin/config", string(raw))
	if code != 200 {
		t.Fatalf("PUT 返回 %d：%v", code, resp)
	}
	if resp["requires_restart"] != false || resp["unchanged"] != true {
		t.Fatalf("没改动时不该要求重启：%v", resp)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("没有改动时不应重写配置文件")
	}
}

func TestAdminRestartCallsShutdownThenExec(t *testing.T) {
	a, _ := adminApp(t)
	oldGrace := restartGrace
	restartGrace = 0
	defer func() { restartGrace = oldGrace }()

	shutdown := make(chan struct{})
	exec := make(chan struct{})
	a.SetShutdownHook(func() { close(shutdown) })
	a.restartFn = func() error { close(exec); return nil }

	code, resp := doJSON(t, a.Routes(), "POST", "/api/admin/restart", "")
	if code != 200 || resp["restarting"] != true {
		t.Fatalf("重启接口返回 %d：%v", code, resp)
	}
	for name, ch := range map[string]chan struct{}{"shutdown": shutdown, "exec": exec} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s 没被调用", name)
		}
	}
}

func TestAdminLogs(t *testing.T) {
	a, _ := adminApp(t)
	ring := newLogRing(3)
	a.logs = ring
	for _, s := range []string{"one\n", "two\n", "three\n", "four\n"} {
		_, _ = ring.Write([]byte(s))
	}
	code, resp := doJSON(t, a.Routes(), "GET", "/api/admin/logs?n=2", "")
	if code != 200 {
		t.Fatalf("logs 返回 %d", code)
	}
	lines, _ := resp["lines"].([]any)
	if len(lines) != 2 || lines[0] != "three" || lines[1] != "four" {
		t.Fatalf("日志尾部不对：%v", lines)
	}
	if resp["total"] != float64(4) {
		t.Fatalf("total 不对：%v", resp["total"])
	}
}
