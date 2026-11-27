package xiaomi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 小米的 STS 链路：serviceToken 往往在前几跳就下发，最后一跳反而 401。
// finishLocation 必须能在任意一跳收下它，而不是只认最后一跳。
func TestFinishLocationCapturesTokenMidChain(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "serviceToken", Value: "TOKEN-FROM-HOP-1", Path: "/"})
		w.Header().Set("Location", "/mid")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/mid", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/sts")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/sts", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // 模拟"已完成的登录被 STS 拒绝"
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := newAuthSession()
	tok, err := s.finishLocation(context.Background(), srv.URL+"/start")
	if err != nil {
		t.Fatalf("应能在中途拿到 serviceToken：%v", err)
	}
	if tok != "TOKEN-FROM-HOP-1" {
		t.Fatalf("token=%q", tok)
	}
}

// 整条链都没给 serviceToken 时要报错，并且错误里带链路（不含敏感 query）。
func TestFinishLocationFailsWithChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	s := newAuthSession()
	_, err := s.finishLocation(context.Background(), srv.URL+"/sts?ticket=SECRET")
	if err == nil {
		t.Fatal("没有 serviceToken 应该报错")
	}
	if got := err.Error(); contains(got, "SECRET") {
		t.Fatalf("错误信息里不该带出 ticket：%s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
