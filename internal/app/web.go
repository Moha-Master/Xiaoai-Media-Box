package app

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// 前端产物。源码在仓库根的 web/（Vue + Vite），构建输出到 internal/app/web/dist，
// 用 `make web`（或 make build 自动带上）生成后内嵌进二进制。
//
// dist 本身不进 git（构建产物），但目录里留了一个 .gitkeep：
// 一来 go:embed 要求目录存在且非空，二来没装 node 的机器也能编译，
// 只是打开页面会看到下面的「前端未构建」提示。
//
//go:embed all:web/dist
var webFS embed.FS

// distFS 是 web/dist 的子文件系统。
var distFS = func() fs.FS {
	sub, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		panic(err) // embed 写错在编译期就该炸
	}
	return sub
}()

// hWeb 提供内置播放器 WebUI：先按静态文件找，找不到就回退到 index.html（SPA 前端路由）。
func (a *App) hWeb(w http.ResponseWriter, r *http.Request) {
	// 后端自己的命名空间绝不能回退成 HTML：那样打错一个 api 路径会拿到 200 + 页面，
	// 前端与 HA 都只能看到“JSON 解析失败”，比 404 难查得多。
	if p := r.URL.Path; p == "/healthz" || strings.HasPrefix(p, "/healthz/") ||
		strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/s/") {
		writeErr(w, 404, fmt.Errorf("未知路径 %s", p))
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	if fi, err := fs.Stat(distFS, name); err == nil && !fi.IsDir() {
		// Vite 给带 hash 的资源起名 assets/index-<hash>.js，可以放心长期缓存；
		// index.html 永远 no-store，否则改版后用户手里还是旧入口。
		switch {
		case name == "index.html":
			w.Header().Set("Cache-Control", "no-store")
		case strings.HasPrefix(name, "assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, distFS, name)
		return
	}
	// 带扩展名却没找到 = 真 404（回退给 SPA 只会让 404 看起来像白屏）
	if ext := path.Ext(name); ext != "" && ext != ".html" {
		http.NotFound(w, r)
		return
	}
	index, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		a.hWebNotBuilt(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(index)
}

const notBuiltPage = `<!doctype html>
<html lang="zh"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>前端未构建 · XiaoAi NCM</title>
<style>
 body{background:#0f1216;color:#e8eef6;font:15px/1.7 system-ui,"Segoe UI",sans-serif;
      margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px}
 .box{max-width:640px}
 code{background:#1e252e;padding:2px 6px;border-radius:5px;color:#7bd88f}
 a{color:#4ea1ff}
</style></head><body><div class="box">
<h1 style="margin:0 0 12px">前端未构建</h1>
<p>这个二进制里没有 WebUI 产物（<code>internal/app/web/dist</code> 是空的）。</p>
<p>构建一次即可，之后 <code>make build</code> 会自动带上：</p>
<pre><code>make web      # 首次会 npm ci
make build</code></pre>
<p>API 本身是可用的，可以直接 <a href="/api/state">/api/state</a> ·
<a href="/api/speakers">/api/speakers</a> · <a href="/healthz">/healthz</a>。</p>
</div></body></html>`

func (a *App) hWebNotBuilt(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(notBuiltPage))
}
