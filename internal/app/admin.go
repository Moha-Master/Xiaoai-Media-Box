package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
)

// 管理面接口。全部挂在 /api/admin/ 前缀下——现在没有鉴权（局域网内网可信是明确选择），
// 但统一前缀让「以后要加 token / 反代屏蔽 / 只绑 loopback」只需要动一处。
//
// 写接口只碰配置文件本身，不尝试热更新 session：音箱拓扑/ssh_host/media 这些在构造期
// 就被拷进 player 对象与 goroutine，改它们必须重启（见 hAdminRestart）。

// restartGrace 是「先回响应、再取消 ctx、最后 exec」之间的缓冲。
// 给 HTTP 响应留出写出时间；测试里会把它调成 0。
var restartGrace = 600 * time.Millisecond

// diskConfig 读磁盘上的现行配置（已 Normalize，含明文凭据，只在内部用）。
func (a *App) diskConfig() (config.Config, error) {
	path := a.cfg.ConfigPath
	if path == "" {
		return config.Config{}, errors.New("服务启动时没有解析到配置文件路径")
	}
	return config.Load(path)
}

func (a *App) hAdminConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.diskConfig()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	redacted, secrets := cfg.Redacted()
	// 磁盘上已经改过、但还在跑的进程里没生效的项
	pending := config.ChangedKeys(a.cfg, cfg)
	backups, _ := config.ListBackups(cfg)
	writeJSON(w, 200, map[string]any{
		"ok":               true,
		"path":             cfg.ConfigPath,
		"base_dir":         cfg.BaseDir,
		"config":           redacted,
		"secrets_set":      emptyIfNil(secrets),
		"pending_changes":  emptyIfNil(pending),
		"requires_restart": len(pending) > 0,
		"backups":          backupsOrEmpty(backups),
		"runtime": map[string]any{
			"version":     version,
			"api":         apiVer,
			"pid":         os.Getpid(),
			"uptime_sec":  a.uptime(),
			"listen":      a.cfg.Listen,
			"public_base": a.cfg.PublicBase,
			"speakers":    len(a.sessions),
		},
	})
}

// emptyIfNil 让 JSON 里出现 [] 而不是 null——前端不必为两种空值写两套判断。
func emptyIfNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func backupsOrEmpty(in []config.Backup) []config.Backup {
	if in == nil {
		return []config.Backup{}
	}
	return in
}

func (a *App) hAdminConfigPut(w http.ResponseWriter, r *http.Request) {
	cur, err := a.diskConfig()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// 从「磁盘现值」出发再解码：Go 的 json 只会覆盖请求体里出现的键，
	// 没提交的字段就保持现状。要是从空值解码，漏一个 window 就会把 50 变成默认 20。
	// 注意 Speakers 切片必须深拷贝，否则 json.Decode 覆写底层数组时会把 cur 一起改掉，
	// 导致 ChangedKeys(cur, in) 永远查不出 speakers 变动。
	in := cur
	if cur.Speakers != nil {
		in.Speakers = append([]config.SpeakerConfig(nil), cur.Speakers...)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	// 未知键直接报错：配置页里一个拼错的 window 静默无效，比 400 难查得多
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErr(w, 400, fmt.Errorf("请求体不是合法 JSON：%w", err))
		return
	}
	config.MergeSecrets(&in, cur) // 网页拿的是脱敏值，留空 = 没改，不是清空
	if err := config.Normalize(&in, cur.ConfigPath); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := in.Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	changed := config.ChangedKeys(cur, in)
	added, removed, modified := config.SpeakerDiff(cur.Speakers, in.Speakers)
	if len(changed) == 0 {
		writeJSON(w, 200, map[string]any{
			"ok": true, "changed": []string{}, "unchanged": true,
			"requires_restart": false,
		})
		return
	}
	backup, err := config.Save(in)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	log.Printf("⚙️ 配置已保存（%v）：备份 %s", changed, backup)
	writeJSON(w, 200, map[string]any{
		"ok":      true,
		"changed": changed,
		"speakers_changed": map[string]any{
			"added": emptyIfNil(added), "removed": emptyIfNil(removed), "modified": emptyIfNil(modified),
		},
		"requires_restart": true,
		"backup":           backup,
		"path":             in.ConfigPath,
		"note":             "音箱拓扑、监听地址、认证文件路径这些在启动时就定下来了，需要重启才生效",
	})
}

func (a *App) hAdminBackups(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.diskConfig()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	items, err := config.ListBackups(cfg)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	dir := filepath.Join(cfg.BaseDir, config.BackupsDir)
	writeJSON(w, 200, map[string]any{"ok": true, "dir": dir, "backups": backupsOrEmpty(items)})
}

// hAdminRestore 回滚到某份备份。同样是「写了磁盘、要重启才生效」。
func (a *App) hAdminRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, fmt.Errorf("请求体不是合法 JSON：%w", err))
		return
	}
	backup, err := config.RestoreBackup(a.cfg, req.Name)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	cfg, err := a.diskConfig()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	changed := config.ChangedKeys(a.cfg, cfg)
	log.Printf("⚙️ 已从备份 %s 回滚配置（回滚前的版本备份在 %s）", req.Name, backup)
	writeJSON(w, 200, map[string]any{
		"ok":               true,
		"restored":         req.Name,
		"backup":           backup,
		"changed":          emptyIfNil(changed),
		"requires_restart": len(changed) > 0,
	})
}

func (a *App) hAdminLogs(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("n"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	if a.logs == nil {
		writeJSON(w, 200, map[string]any{"ok": true, "lines": []string{}, "total": 0})
		return
	}
	lines, total := a.logs.Tail(n)
	writeJSON(w, 200, map[string]any{"ok": true, "lines": lines, "total": total})
}

// hAdminRestart 原地重启进程（syscall.Exec），pid 不变、systemd 无感。
//
// 顺序很关键：先取消根 ctx → 各 session 在退出前自己 flushState（队列快照先落盘），
// 再 exec。这里不能替 session 调 flushState——那会和调度器同时写同一个快照文件。
func (a *App) hAdminRestart(w http.ResponseWriter, r *http.Request) {
	fn := a.restartFn
	if fn == nil {
		fn = selfExec
	}
	writeJSON(w, 200, map[string]any{"ok": true, "restarting": true})
	log.Printf("🔄 收到重启请求：落盘队列快照后自重启")
	go func() {
		if a.onShutdown != nil {
			a.onShutdown()
		}
		time.Sleep(restartGrace)
		if err := fn(); err != nil {
			log.Printf("自重启失败（请手工执行 systemctl restart xiaoai-mb）：%v", err)
		}
	}()
}

func (a *App) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/config", a.hAdminConfigGet)
	mux.HandleFunc("PUT /api/admin/config", a.hAdminConfigPut)
	mux.HandleFunc("GET /api/admin/backups", a.hAdminBackups)
	mux.HandleFunc("POST /api/admin/config/restore", a.hAdminRestore)
	mux.HandleFunc("POST /api/admin/restart", a.hAdminRestart)
	mux.HandleFunc("GET /api/admin/logs", a.hAdminLogs)

	// 小米账号与设备管理
	mux.HandleFunc("GET /api/admin/auth/xiaomi/status", a.hAdminXiaomiAuthStatus)
	mux.HandleFunc("POST /api/admin/auth/xiaomi/logout", a.hAdminXiaomiAuthLogout)
	mux.HandleFunc("POST /api/admin/auth/xiaomi/qr", a.hAdminXiaomiQRStart)
	mux.HandleFunc("GET /api/admin/auth/xiaomi/qr/check", a.hAdminXiaomiQRCheck)
	mux.HandleFunc("GET /api/admin/auth/xiaomi/devices", a.hAdminXiaomiDevices)
	mux.HandleFunc("POST /api/admin/auth/xiaomi/check", a.hAdminXiaomiCheck)
}

// SetShutdownHook 登记自重启前的收尾动作（main 里传 cancel）。
// 自重启走 syscall.Exec，不经过信号处理，所以必须显式取消根 ctx，
// 否则各 session 来不及 flushState，最后一段播放进度就丢了。
func (a *App) SetShutdownHook(fn func()) { a.onShutdown = fn }

// uptime 给管理面显示（测试里手工构造的 App 没有 startedAt，报 0 而不是天文数字）。
func (a *App) uptime() int64 {
	if a.startedAt.IsZero() {
		return 0
	}
	return int64(time.Since(a.startedAt).Seconds())
}
