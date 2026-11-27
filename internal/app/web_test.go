package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getWeb(t *testing.T, h http.Handler, p string) (int, string, http.Header) {
	t.Helper()
	r := httptest.NewRequest("GET", p, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String(), w.Header()
}

// 前端没构建时也要能用：给一张说明页，而不是 500 或白屏。
func TestWebServesIndex(t *testing.T) {
	a, _ := adminApp(t)
	code, body, head := getWeb(t, a.Routes(), "/")
	// 测试环境里 dist 只有 .gitkeep（没跑过 vite build）→ 说明页；
	// 真跑过构建 → SPA 入口。两者都必须是 200 的 HTML。
	if code != 200 || !strings.Contains(body, "<html") {
		t.Fatalf("GET / 应返回 HTML：code=%d body=%.120s", code, body)
	}
	if !strings.HasPrefix(head.Get("Content-Type"), "text/html") {
		t.Fatalf("Content-Type=%q", head.Get("Content-Type"))
	}
	if head.Get("Cache-Control") != "no-store" {
		t.Fatalf("入口页不该被缓存：%q", head.Get("Cache-Control"))
	}
}

// SPA 回退：未知的前端路由给入口页；后端命名空间必须是 JSON 404。
func TestWebRouting(t *testing.T) {
	a, _ := adminApp(t)
	h := a.Routes()

	for _, p := range []string{"/api/nonexistent", "/api/admin/nope", "/healthz/x"} {
		code, body, _ := getWeb(t, h, p)
		if code != 404 {
			t.Fatalf("%s 应 404，得到 %d", p, code)
		}
		if !strings.Contains(body, "未知路径") {
			t.Fatalf("%s 不该回退成 HTML：%.80s", p, body)
		}
	}

	// 带扩展名却没有这个文件 = 真 404，不能悄悄回退到 index.html
	if code, _, _ := getWeb(t, h, "/assets/nope-deadbeef.js"); code != 404 {
		t.Fatalf("缺失资源应 404，得到 %d", code)
	}
	// 前端路由回退
	if code, _, _ := getWeb(t, h, "/whatever/route"); code != 200 {
		t.Fatalf("前端路由应回退到入口页，得到 %d", code)
	}
	// 路径穿越：mux 会先把 URL 规范化（301/307 重定向）， handler 里也不会拼磁盘路径，
	// 总之绝不能把 dist 之外的文件内容吐出来
	code, body, _ := getWeb(t, h, "/../../etc/passwd")
	if strings.Contains(body, "root:") {
		t.Fatalf("读到了系统文件：code=%d body=%.80s", code, body)
	}
	if code != 200 && code != 301 && code != 307 && code != 404 {
		t.Fatalf("意外的响应码 %d", code)
	}
}
