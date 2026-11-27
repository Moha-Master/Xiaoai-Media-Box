package app

import (
	"path/filepath"
	"testing"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/auth"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/xiaomi"
)

func TestNeteaseLogout(t *testing.T) {
	a, _ := adminApp(t)
	dir := t.TempDir()
	jar := auth.NewJar(filepath.Join(dir, "cookie.json"), filepath.Join(dir, "cookie.json.key"))
	jar.Inject("MUSIC_U=test-token-123", "music.163.com")
	a.jar = jar
	a.liked = map[int64]bool{123: true}
	a.uidCache = 999

	code, resp := doJSON(t, a.Routes(), "POST", "/api/logout", "")
	if code != 200 || resp["ok"] != true {
		t.Fatalf("POST /api/logout 失败：%d %v", code, resp)
	}
	if a.jar.Has("music.163.com", "MUSIC_U") {
		t.Fatal("登出后 jar 里仍有 MUSIC_U")
	}
	if len(a.liked) != 0 || a.uidCache != 0 {
		t.Fatalf("登出后红心/账号缓存未清空：liked=%v uid=%v", a.liked, a.uidCache)
	}
}

func TestXiaomiAuthStatusAndLogout(t *testing.T) {
	a, _ := adminApp(t)
	dir := t.TempDir()
	statePath := filepath.Join(dir, ".mina_token.json")
	keyPath := filepath.Join(dir, ".mina_token.json.key")
	a.cfg.Xiaomi.TokenFile = statePath
	a.cfg.Xiaomi.KeyFile = keyPath

	// 初始状态：未登录
	code, resp := doJSON(t, a.Routes(), "GET", "/api/admin/auth/xiaomi/status", "")
	if code != 200 || resp["ok"] != true || resp["logged_in"] != false {
		t.Fatalf("初始状态应为未登录：%d %v", code, resp)
	}

	// 写入模拟凭据
	st := xiaomi.State{
		DeviceID:     "dev123",
		UserID:       "user456",
		PassToken:    "pass-abc",
		ServiceToken: "serv-xyz",
	}
	if err := auth.SaveSealedJSON(statePath, keyPath, st); err != nil {
		t.Fatal(err)
	}

	code, resp = doJSON(t, a.Routes(), "GET", "/api/admin/auth/xiaomi/status", "")
	if code != 200 || resp["logged_in"] != true || resp["user_id"] != "user456" {
		t.Fatalf("写入后应显示已登录：%d %v", code, resp)
	}

	// 退出登录
	code, resp = doJSON(t, a.Routes(), "POST", "/api/admin/auth/xiaomi/logout", "")
	if code != 200 || resp["ok"] != true {
		t.Fatalf("登出失败：%d %v", code, resp)
	}

	code, resp = doJSON(t, a.Routes(), "GET", "/api/admin/auth/xiaomi/status", "")
	if code != 200 || resp["logged_in"] != false {
		t.Fatalf("登出后应为未登录：%d %v", code, resp)
	}
}

func TestXiaomiQRCheckNotFound(t *testing.T) {
	a, _ := adminApp(t)
	code, resp := doJSON(t, a.Routes(), "GET", "/api/admin/auth/xiaomi/qr/check?session_id=nonexistent", "")
	if code != 404 || resp["ok"] != false {
		t.Fatalf("不存在的 session_id 应 404：%d %v", code, resp)
	}
}
