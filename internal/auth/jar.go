package auth

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
)

// Jar 是一个带持久化的 http.CookieJar。
//
// 为什么不用 telanflow/cookiejar：那个库自 2018 年没再更新，而这个 jar 只需要
// 支撑网易云那十来个 cookie（MUSIC_U / __csrf / NMTID / os …），自己写更可控。
//
// 落盘用 authfile.go 的加密容器（AES-256-GCM + 独立密钥文件）：
// 里面是 MUSIC_U —— 网易云的长期凭据，泄露等于账号被接管。
//
// 旧的明文 JSON 会被自动识别、并在下一次保存时升级为密文。
type Jar struct {
	mu      sync.Mutex
	path    string
	keyPath string
	data    map[string]map[string]string
}

func NewJar(path, keyPath string) *Jar {
	if keyPath == "" && path != "" {
		keyPath = path + ".key"
	}
	j := &Jar{path: path, keyPath: keyPath, data: map[string]map[string]string{}}
	j.load()
	return j
}

func (j *Jar) load() {
	if j.path == "" {
		return
	}
	// 旧明文：读完立刻按密文重写，别让 MUSIC_U 在盘上多躺一秒
	if raw, err := os.ReadFile(j.path); err == nil && !strings.HasPrefix(string(raw), Magic) {
		defer j.save()
	}
	var d map[string]map[string]string
	if ok, err := LoadSealedJSON(j.path, j.keyPath, &d); err != nil {
		log.Printf("读取 cookie 文件失败（当作未登录）：%v", err)
		return
	} else if !ok {
		return
	}
	if d != nil {
		j.data = d
	}
}

func (j *Jar) save() {
	if j.path == "" {
		return
	}
	if err := SaveSealedJSON(j.path, j.keyPath, j.data); err != nil {
		log.Printf("保存 cookie 文件失败：%v", err)
	}
}

// Clear 清空全部 cookie 并立即持久化（登出用）。
func (j *Jar) Clear() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.data = map[string]map[string]string{}
	j.save()
}

func (j *Jar) SetCookies(u *url.URL, cookies []*http.Cookie) {

	if u == nil {
		return
	}
	host := u.Hostname()
	j.mu.Lock()
	defer j.mu.Unlock()
	m := j.data[host]
	if m == nil {
		m = map[string]string{}
		j.data[host] = m
	}
	changed := false
	for _, c := range cookies {
		if c.MaxAge < 0 {
			delete(m, c.Name)
			changed = true
			continue
		}
		if c.Value == "" {
			continue
		}
		if m[c.Name] != c.Value {
			m[c.Name] = c.Value
			changed = true
		}
	}
	if changed {
		j.save()
	}
}

func (j *Jar) Cookies(u *url.URL) []*http.Cookie {
	if u == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	// 由具体到泛化：a.b.com → b.com → com
	host := u.Hostname()
	parts := strings.Split(host, ".")
	var out []*http.Cookie
	seen := map[string]bool{}
	for i := 0; i < len(parts); i++ {
		h := strings.Join(parts[i:], ".")
		m, ok := j.data[h]
		if !ok {
			continue
		}
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, &http.Cookie{Name: n, Value: m[n], Path: "/"})
		}
	}
	return out
}

// Inject 把 "a=b; c=d" 形式的 cookie 串塞进 jar（手动登录用）。
func (j *Jar) Inject(cookieStr, host string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	m := j.data[host]
	if m == nil {
		m = map[string]string{}
		j.data[host] = m
	}
	n := 0
	for _, part := range strings.Split(cookieStr, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		n++
	}
	if n > 0 {
		j.save()
	}
	return n
}

func (j *Jar) Has(host, name string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.data[host][name] != ""
}
