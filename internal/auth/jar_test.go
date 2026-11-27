package auth

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func musicURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse("https://music.163.com")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// MUSIC_U 等于账号凭据，落盘必须是密文。
func TestFileJarSealed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookie.json")
	key := filepath.Join(dir, "cookie.json.key")

	j := NewJar(path, key)
	j.SetCookies(musicURL(t), []*http.Cookie{{Name: "MUSIC_U", Value: "secret-token-123", Path: "/"}})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-token-123") {
		t.Fatal("cookie 文件里还有明文 MUSIC_U")
	}
	if !strings.HasPrefix(string(raw), Magic) {
		t.Fatalf("缺少 magic 头：%q", string(raw[:12]))
	}

	j2 := NewJar(path, key)
	found := false
	for _, c := range j2.Cookies(musicURL(t)) {
		if c.Name == "MUSIC_U" && c.Value == "secret-token-123" {
			found = true
		}
	}
	if !found {
		t.Fatal("重新加载后读不到 MUSIC_U")
	}
}

// 旧的明文 cookie.json 必须能读，并在下次保存时自动升级为密文。
func TestFileJarMigratesPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookie.json")
	key := filepath.Join(dir, "cookie.json.key")
	legacy := `{"music.163.com":{"MUSIC_U":"legacy-token","os":"pc"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	j := NewJar(path, key)
	if !j.Has("music.163.com", "MUSIC_U") {
		t.Fatal("旧明文 cookie 没读出来")
	}
	j.Inject("__csrf=abc", "music.163.com") // 触发一次保存
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), Magic) {
		t.Fatal("旧格式没有被升级成密文")
	}
	if strings.Contains(string(raw), "legacy-token") {
		t.Fatal("升级后仍有明文")
	}
}

func TestJarClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookie.json")
	key := filepath.Join(dir, "cookie.json.key")

	j := NewJar(path, key)
	j.SetCookies(musicURL(t), []*http.Cookie{{Name: "MUSIC_U", Value: "token-abc", Path: "/"}})
	if len(j.Cookies(musicURL(t))) == 0 {
		t.Fatal("初始 cookie 未设置成功")
	}

	j.Clear()
	if len(j.Cookies(musicURL(t))) != 0 {
		t.Fatal("Clear 后内存仍有 cookie")
	}

	j2 := NewJar(path, key)
	if len(j2.Cookies(musicURL(t))) != 0 {
		t.Fatal("Clear 后持久化文件重新加载仍有 cookie")
	}
}
