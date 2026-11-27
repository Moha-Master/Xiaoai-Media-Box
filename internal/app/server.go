package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/auth"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/xiaomi"
)

type urlEntry struct {
	info netease.PlayInfo
	exp  time.Time
}

type App struct {
	cfg config.Config
	jar *auth.Jar
	ne  *netease.Netease

	// 云端 MiNA 客户端：共享一份 token（闹钟音量等“云端独有能力”都用它）
	cloud *xiaomi.Client

	// 一台音箱一个 Session，各自独立队列/播放器/探针/音量
	sessions map[string]*Session
	order    []string

	urls sync.Map // songID(int64) -> urlEntry

	// 封面取色缓存（songID -> palEntry），首页卡片颜色不用每次现取
	palCache sync.Map

	// recHooks 只给测试用：整体替换首页推荐只读端点的上游调用（生产为 nil）。
	recHooks *recUp

	// urlFn 只给测试用（替换 a.ne.SongURL，避免真连网易云）；生产为 nil。
	urlFn func(id int64) (string, int, error)

	// songURLsFn 只给测试用（替换 a.ne.SongURLs 批量接口）；生产为 nil。
	// 没设时 resolveMany 会退化成逐首走 urlFn/resolveInfo。
	songURLsFn func(ids []int64) (map[int64]netease.PlayInfo, error)

	// 注：以前这里有一张 songToDev/devToSong 映射表，用来绕开 musics[].global_id
	// 的 int32 钳位。改用 AIVS payload 分支后 audio_id 是字符串、64 位无压力，
	// 这张表连同 devID 概念一起删掉了（2026-11-20）。

	// 红心状态：全量 id 集合，只在换曲时按需拉一次（见 liked.go）
	likedMu    sync.Mutex
	liked      map[int64]bool
	likedSong  int64
	likedBusy  bool
	likedErrAt time.Time
	uidCache   int64

	// 歌词缓存（按 song id，30 分钟 TTL）
	lyrMu  sync.Mutex
	lyrics map[int64]lyricEntry

	// 「我喜欢的音乐」歌单 id：心动模式的 playlistId，按账号稳定，取到就缓存
	pidMu    sync.Mutex
	pidCache string

	// SSE 订阅者（HA / WebUI 实时消费状态）
	hub eventHub

	// ---- 管理面（配置页）用，见 admin.go ----
	startedAt  time.Time
	logs       *logRing     // 内存日志环形缓冲，nil 表示没装（测试里手工构造的 App）
	restartFn  func() error // 自重启实现，测试可替换；nil 走 selfExec
	onShutdown func()       // 自重启前调用（取消根 ctx，让各 session flushState）
}

func NewApp(cfg config.Config, jar *auth.Jar, ne *netease.Netease) (*App, error) {
	if len(cfg.Speakers) == 0 {
		return nil, fmt.Errorf("没有配置任何音箱")
	}
	a := &App{
		cfg:       cfg,
		jar:       jar,
		ne:        ne,
		sessions:  map[string]*Session{},
		startedAt: time.Now(),
	}
	// 只要有一台音箱配了云端 device_id（cloud 控制或 ssh 控制都可能配），
	// 就建一个共享的 MiNA 客户端——闹钟音量等能力只走云端，不依赖 SSH。
	for _, sp := range cfg.Speakers {
		if sp.DeviceID != "" {
			a.cloud = xiaomi.NewClient(cfg.Xiaomi)
			break
		}
	}
	for _, sp := range cfg.Speakers {
		if _, dup := a.sessions[sp.ID]; dup {
			log.Printf("跳过重复的 speaker id=%q（%s）", sp.ID, sp.Name)
			continue
		}
		s, err := newSession(a, sp)
		if err != nil {
			log.Printf("音箱 %q 初始化失败：%v", sp.Name, err)
			continue
		}
		a.sessions[sp.ID] = s
		a.order = append(a.order, sp.ID)
	}
	if len(a.sessions) == 0 {
		return nil, fmt.Errorf("没有任何音箱初始化成功")
	}
	return a, nil
}

// start 给每个 session 起探针 + 推进器。
func (a *App) Start(ctx context.Context) {
	for _, id := range a.order {
		s := a.sessions[id]
		go s.runPoll(ctx)
		go s.runScheduler(ctx)
		go s.runAlarmPoll(ctx)
	}
}

func (a *App) describe() string {
	parts := make([]string, 0, len(a.order))
	for _, id := range a.order {
		parts = append(parts, a.sessions[id].player.Describe())
	}
	return strings.Join(parts, " | ")
}

// SessionCount 返回成功初始化的音箱数量。
func (a *App) SessionCount() int { return len(a.sessions) }

// SessionDescriptions 返回每台音箱的一行启动描述（给 cmd 打日志用）。
func (a *App) SessionDescriptions() []string {
	out := make([]string, 0, len(a.order))
	for _, id := range a.order {
		s := a.sessions[id]
		if s.probe != nil {
			out = append(out, fmt.Sprintf("%s/%s：%s  通道=%s  回读=%v",
				s.speaker.Name, s.speaker.ID, s.player.Describe(), s.speaker.Media, s.probeEvery))
			continue
		}
		out = append(out, fmt.Sprintf("%s/%s：%s  通道=%s  回读=关闭",
			s.speaker.Name, s.speaker.ID, s.player.Describe(), s.speaker.Media))
	}
	return out
}

// pickSession 从 ?speaker= / body speaker / X-Speaker / X-Device 头里选一台；只有一台时省略也行。
func (a *App) pickSession(r *http.Request, bodySpeaker string) (*Session, error) {
	id := r.URL.Query().Get("speaker")
	if id == "" {
		id = bodySpeaker
	}
	if id == "" && r != nil {
		id = r.Header.Get("X-Speaker")
		if id == "" {
			id = r.Header.Get("X-Device")
		}
	}
	if id != "" {
		s, ok := a.sessions[id]
		if !ok {
			return nil, fmt.Errorf("未知 speaker=%q（可用：%s）", id, strings.Join(a.order, ", "))
		}
		return s, nil
	}
	if len(a.sessions) == 1 {
		return a.sessions[a.order[0]], nil
	}
	return nil, fmt.Errorf("请指定 speaker=（可用：%s）", strings.Join(a.order, ", "))
}

// ---------- 直链缓存 ----------

// resolveInfo 拿一首歌的 PlayInfo（直链 + 可播区间），缓存到 expi 之前 60 秒。
// 缓存命中也带区间——推窗时拿不到区间就没法修 Duration。
func (a *App) resolveInfo(id int64) (netease.PlayInfo, error) {
	if v, ok := a.urls.Load(id); ok {
		e := v.(urlEntry)
		if time.Now().Before(e.exp) {
			return e.info, nil
		}
	}
	var (
		info netease.PlayInfo
		err  error
	)
	if a.urlFn != nil { // 只给测试用：不碰真实网易云
		var url string
		var expi int
		url, expi, err = a.urlFn(id)
		info = netease.PlayInfo{URL: url, Expi: expi}
	} else {
		var m map[int64]netease.PlayInfo
		m, err = a.ne.SongURLs([]int64{id})
		if err == nil {
			var ok bool
			info, ok = m[id]
			if !ok {
				err = fmt.Errorf("无可用直链（版权/VIP 限制）")
			}
		}
	}
	if err != nil {
		a.urls.Delete(id)
		return netease.PlayInfo{}, err
	}
	a.urls.Store(id, urlEntry{info: info, exp: time.Now().Add(time.Duration(info.Expi)*time.Second - 60*time.Second)})
	return info, nil
}

// resolve 拿一条新鲜的网易云直链（resolveInfo 的 URL-only 包装）。
func (a *App) resolve(id int64) (string, error) {
	info, err := a.resolveInfo(id)
	if err != nil {
		return "", err
	}
	return info.URL, nil
}

// resolveMany 批量取直链与可播区间，并顺手写进缓存（推窗前预热，
// 设备首次取流不用卡在现取现跳上）。单个拿不到不影响其他：返回 map 只含成功的。
func (a *App) resolveMany(ids []int64) map[int64]netease.PlayInfo {
	out := map[int64]netease.PlayInfo{}
	if len(ids) == 0 {
		return out
	}
	now := time.Now()
	var pending []int64
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		if v, ok := a.urls.Load(id); ok {
			e := v.(urlEntry)
			if now.Before(e.exp) {
				out[id] = e.info
				continue
			}
		}
		pending = append(pending, id)
	}
	if len(pending) == 0 {
		return out
	}
	var (
		m   map[int64]netease.PlayInfo
		err error
	)
	switch {
	case a.songURLsFn != nil: // 测试钩子
		m, err = a.songURLsFn(pending)
	case a.urlFn != nil: // 测试钩子只给了单首：逐个退化
		m = map[int64]netease.PlayInfo{}
		for _, id := range pending {
			info, e := a.resolveInfo(id)
			if e == nil {
				m[id] = info
			}
		}
	default:
		m, err = a.ne.SongURLs(pending)
	}
	if err != nil {
		// 批量失败不炸整窗：退化成逐首（旧的 filterPlayable 行为）
		for _, id := range pending {
			if info, e := a.resolveInfo(id); e == nil {
				out[id] = info
			}
		}
		return out
	}
	for id, info := range m {
		out[id] = info
		a.urls.Store(id, urlEntry{info: info, exp: time.Now().Add(time.Duration(info.Expi)*time.Second - 60*time.Second)})
	}
	return out
}

// deviceURL 是我们给设备的地址：永远指向本服务，由 /s/<songID> 现取现跳。
//
// 这层 locator 躲不掉——网易云直链只有 20 分钟有效期（签名在 URL 路径里），
// 而一窗曲目要放几小时。小米自己也是同样的设计（cp_resource_locator）。
func (a *App) deviceURL(songID int64) string {
	return strings.TrimRight(a.cfg.PublicBase, "/") + "/s/" + strconv.FormatInt(songID, 10)
}

// ---------- HTTP ----------

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.hWeb)
	mux.HandleFunc("GET /healthz", a.hHealth)
	mux.HandleFunc("GET /api/state", a.hState)
	mux.HandleFunc("GET /api/device", a.hDevice)
	mux.HandleFunc("POST /api/device/state", a.hDeviceState)
	mux.HandleFunc("POST /api/device-state", a.hDeviceState)
	mux.HandleFunc("GET /api/queue", a.hQueue)
	mux.HandleFunc("POST /api/queue/remove", a.hQueueRemove)
	mux.HandleFunc("POST /api/queue/move", a.hQueueMove)
	mux.HandleFunc("GET /api/search", a.hSearch)
	mux.HandleFunc("GET /api/search/hot", a.hHotSearches)
	mux.HandleFunc("GET /api/recommend/playlists", a.hRecommendPlaylists)
	mux.HandleFunc("GET /api/toplist", a.hToplist)
	mux.HandleFunc("GET /api/newalbums", a.hNewAlbums)
	mux.HandleFunc("GET /api/fm", a.hFM)
	mux.HandleFunc("GET /api/palette/{id}", a.hPalette)
	mux.HandleFunc("GET /api/playlist/{id}", a.hPlaylist)
	mux.HandleFunc("GET /api/daily", a.hDaily)
	mux.HandleFunc("GET /api/user/playlists", a.hUserPlaylists)
	mux.HandleFunc("GET /api/account", a.hAccount)
	mux.HandleFunc("GET /api/speakers", a.hSpeakers)
	mux.HandleFunc("POST /api/jump", a.hJump)
	mux.HandleFunc("POST /api/play", a.hPlay)
	mux.HandleFunc("POST /api/next", a.hNext)
	mux.HandleFunc("POST /api/prev", a.hPrev)
	mux.HandleFunc("POST /api/pause", a.hPause)
	mux.HandleFunc("POST /api/resume", a.hResume)
	mux.HandleFunc("POST /api/stop", a.hStop)
	mux.HandleFunc("POST /api/loop", a.hLoop)
	mux.HandleFunc("POST /api/mode", a.hMode)
	mux.HandleFunc("POST /api/volume", a.hVolume)
	mux.HandleFunc("POST /api/mute", a.hMute)
	mux.HandleFunc("GET /api/alarm_volume", a.hAlarmVolume)
	mux.HandleFunc("POST /api/alarm_volume", a.hAlarmVolumeSet)
	mux.HandleFunc("POST /api/seek", a.hSeek)
	mux.HandleFunc("POST /api/like", a.hLike)
	mux.HandleFunc("POST /api/enqueue", a.hEnqueue)
	mux.HandleFunc("POST /api/hotmode", a.hHotmode)
	mux.HandleFunc("POST /api/hotmode/stop", a.hHotmodeStop)
	mux.HandleFunc("GET /api/lyric", a.hLyric)
	mux.HandleFunc("GET /api/events", a.hEvents)
	mux.HandleFunc("POST /api/login/cookie", a.hLoginCookie)
	mux.HandleFunc("GET /api/login/qr", a.hLoginQR)
	mux.HandleFunc("GET /api/login/qr/check", a.hLoginQRCheck)
	mux.HandleFunc("POST /api/login/logout", a.hLogout)
	mux.HandleFunc("POST /api/logout", a.hLogout)
	mux.HandleFunc("GET /s/{id}", a.hStream)
	a.registerAdmin(mux)
	return withLog(mux)
}

func withLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		// /healthz 和 /api/lyric（HA 歌词实体每 3 秒一次）不进日志，
		// 不然正经的播放/刷写日志会被冲走
		if p := r.URL.Path; p != "/healthz" && p != "/api/lyric" {
			log.Printf("%s %s %s %s", r.RemoteAddr, r.Method, p, time.Since(start).Round(time.Millisecond))
		}
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"ok": false, "error": err.Error()})
}

// version / api 是给外部控制器（HA 集成）做兼容判断用的，改动契约时同步 +1。
const (
	version = "0.2.0"
	apiVer  = 1
)

func (a *App) hHealth(w http.ResponseWriter, r *http.Request) {
	uid, nick, accErr := a.ne.Account()
	resp := map[string]any{
		"ok":       true,
		"version":  version,
		"api":      apiVer,
		"player":   a.describe(),
		"speakers": len(a.sessions),
		"stream":   a.cfg.StreamMode,
		"account": map[string]any{
			"logged_in": accErr == nil,
			"uid":       uid,
			"nickname":  nick,
		},
	}
	writeJSON(w, 200, resp)
}

func (a *App) hState(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	fresh := r.URL.Query().Get("fresh") == "1"
	writeJSON(w, 200, s.statePayload(r.Context(), fresh))
}

func (a *App) hDevice(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	note := ""
	if r.URL.Query().Get("fresh") == "1" {
		if s.pushMode() {
			// 推送模式没有拉取通道，"重新回读"只能是"等下一帧上报"：
			// 等一个采样周期（数据代次变了就走），并在响应里说明来源，
			// 别让设备调试页那个按钮看起来像"我们主动问了设备"。
			sample := s.pushTiming().sample
			note = fmt.Sprintf("推送模式：等下一帧上报（最多 %s）", sample)
			gen := s.device.generation()
			deadline := time.After(sample + 2*time.Second)
			for s.device.generation() == gen {
				select {
				case <-deadline:
				case <-r.Context().Done():
				default:
					time.Sleep(100 * time.Millisecond)
					continue
				}
				break
			}
		} else {
			s.triggerProbeWait(r.Context())
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "speaker": s.speaker.ID,
		"device": s.deviceView(), "note": note})
}

// hDeviceState 接收音箱端 daemon 主动推送的设备状态（POST /api/device/state）。
// Payload 可以是原始 ubus 回复（{"code":0,"info":"{...}"}）或提取后的 JSON。
func (a *App) hDeviceState(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeErr(w, 400, fmt.Errorf("读取请求体失败：%w", err))
		return
	}
	st, err := player.ParseStatus(body)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("解析设备状态失败：%w", err))
		return
	}
	s.updateDeviceStatus(st)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// hQueue 给 WebUI / HA 看完整队列（含全部曲目，不只是 upcoming 5 首）。
func (a *App) hQueue(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "speaker": s.speaker.ID, "queue": s.fullView()})
}

// hDaily 只返回每日推荐曲目，不触发播放（浏览用）。
func (a *App) hDaily(w http.ResponseWriter, r *http.Request) {
	tracks, err := a.ne.DailySongs()
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "tracks": tracks})
}

// hUserPlaylists 返回当前账号的歌单列表（WebUI 侧栏 / HA 浏览树用）。
func (a *App) hUserPlaylists(w http.ResponseWriter, r *http.Request) {
	uid, _, err := a.ne.Account()
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	list, err := a.ne.UserPlaylists(strconv.FormatInt(uid, 10))
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "playlists": list})
}

// hJump 跳到队列里的第 index 首（WebUI 点列表用）。
func (a *App) hJump(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index   int    `json:"index"`
		Speaker string `json:"speaker"`
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
	if _, _, ok := s.q.Jump(req.Index); !ok {
		writeErr(w, 400, fmt.Errorf("队列为空或下标越界"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.startCurrent(ctx); err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "queue": s.view(5)})
}

// hSearch 多类型搜索。type=song（默认）的响应结构与旧版逐字节兼容——
// HA 集成在用，只认 {"ok":true,"tracks":[...]}；其它类型走各自的精简结构。
func (a *App) hSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeErr(w, 400, fmt.Errorf("缺少 q 参数"))
		return
	}
	typ := r.URL.Query().Get("type")
	if typ == "" {
		typ = "song"
	}
	switch typ {
	case "song", "playlist", "artist", "album":
	default:
		writeErr(w, 400, fmt.Errorf("type 只能是 song / playlist / artist / album"))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := a.upstream().searchType(q, typ, limit)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if typ == "song" {
		writeJSON(w, 200, map[string]any{"ok": true, "tracks": res.Tracks})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "type": typ, "playlists": res.Playlists, "artists": res.Artists, "albums": res.Albums})
}

func (a *App) hPlaylist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tracks, err := a.ne.PlaylistTracks(id)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "tracks": tracks})
}

// hSpeakers 给 WebUI / HA 配置流用：列出所有 session 及其状态。
func (a *App) hSpeakers(w http.ResponseWriter, r *http.Request) {
	list := make([]map[string]any, 0, len(a.order))
	for _, id := range a.order {
		s := a.sessions[id]
		item := map[string]any{
			"id": s.speaker.ID, "name": s.speaker.Name, "control": s.speaker.Control, "status_mode": s.speaker.StatusMode,
		}
		// 侧栏要能区分"空闲"和"问不到"：设备不可达时不要把最后已知的播放态当现状报出去
		if off, _ := s.unreachableView(); off {
			item["unreachable"] = true
		}
		vol, muted := s.VolumeState()
		item["volume"] = vol
		item["muted"] = muted
		if st, fresh := s.deviceFresh(); fresh {
			item["playing"] = st.Playing()
			item["status"] = st.Status
		}
		if v := s.view(0); v.Current != nil {
			item["current"] = v.Current.Label()
			item["queue_index"] = v.Index
			item["queue_total"] = v.Total
		}
		list = append(list, item)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "speakers": list})
}

func (a *App) hAccount(w http.ResponseWriter, r *http.Request) {
	uid, nick, err := a.ne.Account()
	if err != nil {
		writeErr(w, 401, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "uid": uid, "nickname": nick})
}

type playReq struct {
	Query      string  `json:"query"`
	PlaylistID string  `json:"playlist_id"`
	AlbumID    string  `json:"album_id"`
	SongIDs    []int64 `json:"song_ids"`
	Daily      bool    `json:"daily"`
	Index      int     `json:"index"`
	Speaker    string  `json:"speaker"`
}

func (a *App) hPlay(w http.ResponseWriter, r *http.Request) {
	req, err := decodePlayReq(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	tracks, source, err := a.resolveTracks(req)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if len(tracks) == 0 {
		writeErr(w, 404, fmt.Errorf("没有找到可播放的歌曲"))
		return
	}

	s.q.Replace(tracks, source, req.Index)
	// 手动换成普通歌单/搜索播放，退出无限心动（原因推给 UI，不静默消失）
	s.StopHotmode("已切换到其他播放来源")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.startCurrent(ctx); err != nil {
		writeErr(w, 502, err)
		return
	}
	s.probeSoon("建队列播放")
	writeJSON(w, 200, map[string]any{"ok": true, "queue": s.view(5)})
}

// decodePlayReq 读请求体并合并 query 参数（方便 rest_command 直接拼 URL）。
func decodePlayReq(r *http.Request) (playReq, error) {
	var req playReq
	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				return req, fmt.Errorf("请求体不是合法 JSON: %w", err)
			}
		}
	}
	qs := r.URL.Query()
	if v := qs.Get("q"); v != "" && req.Query == "" {
		req.Query = v
	}
	if v := qs.Get("playlist_id"); v != "" && req.PlaylistID == "" {
		req.PlaylistID = v
	}
	if v := qs.Get("album_id"); v != "" && req.AlbumID == "" {
		req.AlbumID = v
	}
	if v := qs.Get("daily"); v == "1" || v == "true" {
		req.Daily = true
	}
	return req, nil
}

// resolveTracks 把 playReq 解析成曲目列表（play / enqueue 共用）。
func (a *App) resolveTracks(req playReq) ([]netease.Track, string, error) {
	var (
		tracks []netease.Track
		source string
		err    error
	)
	switch {
	case len(req.SongIDs) > 0:
		tracks, err = a.ne.SongDetail(req.SongIDs)
		source = fmt.Sprintf("songs:%d", len(req.SongIDs))
	case req.PlaylistID != "":
		tracks, err = a.ne.PlaylistTracks(req.PlaylistID)
		source = "playlist:" + req.PlaylistID
	case req.AlbumID != "":
		tracks, err = a.ne.AlbumTracks(req.AlbumID)
		source = "album:" + req.AlbumID
	case req.Daily:
		tracks, err = a.ne.DailySongs()
		source = "daily"
	case req.Query != "":
		tracks, err = a.ne.Search(req.Query, 20)
		source = "search:" + req.Query
	default:
		return nil, "", fmt.Errorf("需要 query / playlist_id / album_id / song_ids / daily 之一")
	}
	return tracks, source, err
}

// hEnqueue 往当前队列里加歌，不影响正在播的这首。
//
//	{"song_ids":[…]|"query":…|…, "next": true}   next=true 插到当前曲后面，否则加到队尾
func (a *App) hEnqueue(w http.ResponseWriter, r *http.Request) {
	var req playReq
	var extra struct {
		Next bool `json:"next"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeErr(w, 400, fmt.Errorf("请求体不是合法 JSON: %w", err))
			return
		}
		_ = json.Unmarshal(body, &extra)
	}
	qs := r.URL.Query()
	if v := qs.Get("next"); v == "1" || v == "true" {
		extra.Next = true
	}
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	tracks, _, err := a.resolveTracks(req)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if len(tracks) == 0 {
		writeErr(w, 404, fmt.Errorf("没有找到可加入的歌曲"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err := s.Enqueue(ctx, tracks, extra.Next); err != nil {
		writeErr(w, 502, err)
		return
	}
	s.probeSoon("加入队列")
	writeJSON(w, 200, map[string]any{"ok": true, "added": len(tracks), "queue": s.view(3)})
}

// filterPlayable 验证一批歌能不能真的取到直链（VIP / 版权限制在这台机器上很常见），
// 只留下能放的。走批量接口（一次网络请求替代 N 次），并把试听区间写进返回的
// Track；resolve 的缓存顺带预热，窗口里前几首起播更快。
func (a *App) filterPlayable(tracks []netease.Track, workers int) []netease.Track {
	if len(tracks) == 0 {
		return tracks
	}
	_ = workers // 批量化后不再需要并发 worker，参数保留兼容旧调用
	ids := make([]int64, 0, len(tracks))
	for _, t := range tracks {
		if t.ID != 0 {
			ids = append(ids, t.ID)
		}
	}
	infos := a.resolveMany(ids)

	out := tracks[:0]
	for _, t := range tracks {
		if info, ok := infos[t.ID]; ok && info.URL != "" {
			if info.EndMs > 0 && t.TrialEndMs == 0 {
				t.TrialStartMs, t.TrialEndMs = info.StartMs, info.EndMs
			}
			out = append(out, t)
		}
	}
	return out
}

// likedPlaylistID 取「我喜欢的音乐」歌单 id，成功一次就缓存（对同一账号稳定）。
func (a *App) likedPlaylistID() (string, error) {
	a.pidMu.Lock()
	defer a.pidMu.Unlock()
	if a.pidCache != "" {
		return a.pidCache, nil
	}
	uid := a.uid()
	if uid == 0 {
		return "", fmt.Errorf("网易云未登录，无法确定「我喜欢的音乐」")
	}
	pid, err := a.ne.LikedPlaylistID(uid)
	if err != nil {
		return "", err
	}
	a.pidCache = pid
	return pid, nil
}

// hHotmode 是「心动模式」：拿一首种子歌，向网易云要一批基于它的推荐，
// **替换**当前队列并从种子开始播。
//
//	POST /api/hotmode?speaker=living
//	{"seed": "current"}   ← 用当前在放的歌当种子
//	{"seed": "liked"}     ← 从红心歌单里随机挑一首当种子
//	{"seed": "186016"}    ← 指定歌曲 id
//
// 设计取舍：
//   - 种子歌排第一：这样「从当前音乐开始心动」时先把手头这首放完，
//     再自然过渡到推荐，不会一刀切断；
//   - 若种子正是刚在放的那首，播完窗口后用 Seek 把进度拉回原处；
//   - 强制 queue.LoopList + 关闭 shuffle：推荐列表的顺序是算法给的，随机化就没意义了；
//   - 候选不截断（服务端推多少收多少），滤重/兜底交给续杯逻辑。
func (a *App) hHotmode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Seed    string `json:"seed"`
		Speaker string `json:"speaker"`
	}
	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeErr(w, 400, fmt.Errorf("请求体不是合法 JSON: %w", err))
				return
			}
		}
	}
	if v := r.URL.Query().Get("seed"); v != "" {
		req.Seed = v
	}
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}

	v := s.q.View(0)
	var (
		seedID       int64
		keepPos      int
		wasPlaying   bool
		seedPlayable bool
	)
	// 种子可能因为 VIP / 版权放不了。实测：周杰伦《晴天》原版就是 VIP-only，
	// 先排第一的结果是一按下去第一个 404、设备直接播报"播放失败"。
	// 所以这里统一先验一次（resolve 会顺带把直链缓存热起来）。
	playable := func(id int64) bool {
		_, err := a.resolve(id)
		return err == nil
	}
	seed := strings.ToLower(strings.TrimSpace(req.Seed))
	switch seed {
	case "", "current", "now":
		if v.Current == nil {
			writeErr(w, 409, fmt.Errorf("当前没有在放的歌，没法当心动种子（可以改用 seed=liked）"))
			return
		}
		seedID = v.Current.ID
		keepPos = v.PositionMs
		wasPlaying = v.Playing
		seedPlayable = playable(seedID)
	case "liked", "heart", "favorite":
		ids, err := a.ne.LikedIDs(a.uid())
		if err != nil {
			writeErr(w, 502, err)
			return
		}
		// 别抽到刚在放的那首，否则按下去看不出变化
		pool := make([]int64, 0, len(ids))
		for _, id := range ids {
			if v.Current == nil || id != v.Current.ID {
				pool = append(pool, id)
			}
		}
		if len(pool) == 0 {
			pool = ids
		}
		if len(pool) == 0 {
			writeErr(w, 409, fmt.Errorf("「我喜欢的音乐」是空的，先收藏几首再试"))
			return
		}
		// 红心歌单里可能混着 VIP/版权已失效的歌，随机挑一首**放得了**的
		// （最多试 10 首，别为了运气连撞 41 次接口）
		rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		tries := 10
		if tries > len(pool) {
			tries = len(pool)
		}
		for i := 0; i < tries; i++ {
			if playable(pool[i]) {
				seedID, seedPlayable = pool[i], true
				break
			}
		}
		if !seedPlayable {
			writeErr(w, 409, fmt.Errorf("红心歌单里没挑到能放的歌（可能都是 VIP/版权受限）"))
			return
		}
	default:
		n, err := strconv.ParseInt(seed, 10, 64)
		if err != nil || n <= 0 {
			writeErr(w, 400, fmt.Errorf("seed 只能是 current / liked / 歌曲 id"))
			return
		}
		seedID = n
		if !playable(seedID) {
			writeErr(w, 409, fmt.Errorf("歌曲 %d 放不了（VIP/版权受限）", seedID))
			return
		}
		seedPlayable = true
	}

	pid, err := a.likedPlaylistID()
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	// 服务端推多少收多少：不再按 window/count 截断（截断过早曾把 119 首候选
	// 扔掉 83%，是续杯必然失败的根因）。滤重交给续杯逻辑。
	recs, err := a.ne.Hotmode(seedID, pid)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if len(recs) == 0 {
		writeErr(w, 404, fmt.Errorf("心动模式没返回可播放的推荐"))
		return
	}

	// 种子歌排第一；放不了（或查不到详情）就只放推荐
	tracks := make([]netease.Track, 0, len(recs)+1)
	seedLabel := ""
	seedAdded := false
	if seedPlayable {
		if ts, err := a.ne.SongDetail([]int64{seedID}); err != nil {
			log.Printf("[%s] 心动种子详情查询失败（只放推荐）：%v", s.speaker.Name, err)
		} else if len(ts) > 0 {
			tracks = append(tracks, ts[0])
			seedLabel = ts[0].Label()
			seedAdded = true
		}
	} else {
		log.Printf("[%s] 心动种子 %d 放不了（VIP/版权），只放推荐", s.speaker.Name, seedID)
	}
	tracks = append(tracks, recs...)

	// 推荐里会混进 VIP / 版权失效的歌，设备播到那首会 404 跳过、还可能播报
	// "播放失败"。并发验一遍只留能放的（resolve 顺带预热直链，起播也更快）。
	if before := len(tracks); before > 0 {
		tracks = a.filterPlayable(tracks, 5)
		if dropped := before - len(tracks); dropped > 0 {
			log.Printf("[%s] 心动模式：%d 首不可播（VIP/版权）已剔除", s.speaker.Name, dropped)
		}
	}
	if len(tracks) == 0 {
		writeErr(w, 404, fmt.Errorf("推荐里没有可播放的歌"))
		return
	}
	// 种子被筛掉的话，就没有"恢复进度"这回事了
	if !seedAdded || tracks[0].ID != seedID {
		keepPos = 0
		seedLabel = ""
	}
	if seedLabel == "" {
		seedLabel = "仅推荐"
	}
	source := "心动模式·" + seedLabel
	s.q.SetLoop(queue.LoopList)
	s.q.SetShuffle(false)
	s.q.Replace(tracks, source, 0)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.startCurrent(ctx); err != nil {
		writeErr(w, 502, err)
		return
	}
	// 种子就是刚才在放的那首：整窗重推会从 0 开始，这里把进度拉回去
	if seedLabel != "仅推荐" && keepPos > 2000 {
		if err := s.Seek(ctx, keepPos); err != nil {
			log.Printf("[%s] 心动模式恢复进度失败（从 0 开始放）：%v", s.speaker.Name, err)
		}
		if !wasPlaying {
			if err := s.player.Pause(ctx); err == nil {
				s.q.MarkPaused()
			}
		}
	}
	s.probeSoon("心动模式")
	// 窗口已经推下去，从这里开始无限续杯：播到窗口末尾会自动再取一批
	// （hotmode_extend_below 设为负数可关掉，退回“取一次、循环这一窗”）
	s.StartHotmode(pid)
	log.Printf("[%s] 💗 心动模式：种子 %s，入队 %d 首（pid=%s）",
		s.speaker.Name, seedLabel, len(tracks), pid)
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"seed":       seedID,
		"seed_label": seedLabel,
		"source":     source,
		"count":      len(tracks),
		"infinite":   a.cfg.HotmodeExtendBelow > 0,
		"queue":      s.view(5),
	})
}

// hHotmodeStop 手动退出无限心动（UI 的 ♾ 开关）。
// 队列与播放不受影响：只是停止续杯，剩下的歌照常播完。
func (a *App) hHotmodeStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Speaker string `json:"speaker"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	_, _, pushed, _ := s.HotmodeState()
	s.StopHotmode("手动停止")
	s.probeSoon("心动停止")
	log.Printf("[%s] ♾ 手动退出心动模式（累计续杯 %d 次）", s.speaker.Name, pushed)
	writeJSON(w, 200, map[string]any{"ok": true, "extended": pushed})
}

func (a *App) hNext(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.next(ctx); err != nil {
		writeErr(w, 502, err)
		return
	}
	s.probeSoon("下一首")
	writeJSON(w, 200, map[string]any{"ok": true, "queue": s.view(3)})
}

func (a *App) hPrev(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.prev(ctx); err != nil {
		writeErr(w, 502, err)
		return
	}
	s.probeSoon("上一首")
	writeJSON(w, 200, map[string]any{"ok": true, "queue": s.view(3)})
}

func (a *App) hPause(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.player.Pause(r.Context()); err != nil {
		writeErr(w, 502, err)
		return
	}
	s.q.MarkPaused()
	s.probeSoon("暂停")
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) hResume(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	// 暂停期间改过队列：设备手里那一窗已经过时，得重推 + 跳回原进度
	handled, err := s.resumePending(ctx)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	if !handled {
		if err := s.player.Resume(r.Context()); err != nil {
			writeErr(w, 502, err)
			return
		}
		s.q.MarkPlaying()
	}
	s.probeSoon("继续播放")
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) hStop(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.player.Stop(r.Context()); err != nil {
		writeErr(w, 502, err)
		return
	}
	s.q.Reset()
	s.clearPendingEdit()
	s.probeSoon("停止")
	writeJSON(w, 200, map[string]any{"ok": true})
}

// hLoop 是旧接口：mode=list|single|shuffle|none。
// 传 shuffle 等价于「列表循环 + 随机」，其它值会把随机关掉。
func (a *App) hLoop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode    string `json:"mode"`
		Speaker string `json:"speaker"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Mode == "" {
		req.Mode = r.URL.Query().Get("mode")
	}
	m, ok := queue.ParseLoop(req.Mode)
	if !ok {
		writeErr(w, 400, fmt.Errorf("mode 只能是 none / list / single / shuffle"))
		return
	}
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	s.q.SetLoop(m)
	s.q.SetShuffle(queue.WantsShuffle(req.Mode))
	writeJSON(w, 200, map[string]any{
		"ok": true, "loop": queue.LoopLabel(m, s.q.Shuffle()), "shuffle": s.q.Shuffle(),
	})
}

// hMode 是给 HA 用的：repeat 与 shuffle 正交，可以分别设置。
//
//	{"repeat": "off"|"all"|"one", "shuffle": true|false}
//
// 两个字段都可省略，只改传了的那个。
func (a *App) hMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repeat  string `json:"repeat"`
		Mode    string `json:"mode"` // 兼容：直接传 loop 名
		Shuffle *bool  `json:"shuffle"`
		Speaker string `json:"speaker"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, 400, fmt.Errorf("请求体不是合法 JSON: %w", err))
		return
	}
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	repeat := strings.ToLower(strings.TrimSpace(req.Repeat))
	if repeat == "" {
		repeat = strings.ToLower(strings.TrimSpace(req.Mode))
	}
	switch repeat {
	case "off", "none":
		s.q.SetLoop(queue.LoopNone)
	case "all", "list":
		s.q.SetLoop(queue.LoopList)
	case "one", "single":
		s.q.SetLoop(queue.LoopSingle)
	case "shuffle":
		s.q.SetLoop(queue.LoopList)
		s.q.SetShuffle(true)
	case "":
		// 只改 shuffle
	default:
		writeErr(w, 400, fmt.Errorf("repeat 只能是 off / all / one"))
		return
	}
	if req.Shuffle != nil {
		s.q.SetShuffle(*req.Shuffle)
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "loop": queue.LoopLabel(s.q.Loop(), s.q.Shuffle()), "shuffle": s.q.Shuffle(),
	})
}

func (a *App) hVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value   *int   `json:"value"`
		Delta   int    `json:"delta"`
		Speaker string `json:"speaker"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	cur, _ := s.VolumeState()
	v := cur + req.Delta
	if req.Value != nil {
		v = *req.Value
	}
	if err := s.SetVolume(r.Context(), v); err != nil {
		writeErr(w, 502, err)
		return
	}
	vol, muted := s.VolumeState()
	writeJSON(w, 200, map[string]any{"ok": true, "volume": vol, "muted": muted})
}

// hMute 静音开关：设备没有独立 mute 接口，用音量 0 + 记住原值实现。
func (a *App) hMute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value   *bool  `json:"value"`
		Speaker string `json:"speaker"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	_, muted := s.VolumeState()
	on := !muted
	if req.Value != nil {
		on = *req.Value
	}
	if err := s.SetMute(r.Context(), on); err != nil {
		writeErr(w, 502, err)
		return
	}
	vol, m := s.VolumeState()
	writeJSON(w, 200, map[string]any{"ok": true, "muted": m, "volume": vol})
}

// hAlarmVolume 读闹钟音量（0-100）。闹钟音量独立于播放音量。
func (a *App) hAlarmVolume(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	v, err := s.GetAlarmVolume(r.Context())
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "speaker": s.speaker.ID, "alarm_volume": v})
}

// hAlarmVolumeSet 设置闹钟音量（0-100）。
func (a *App) hAlarmVolumeSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value   *int   `json:"value"`
		Volume  *int   `json:"volume"`
		Speaker string `json:"speaker"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	v := -1
	if req.Value != nil {
		v = *req.Value
	} else if req.Volume != nil {
		v = *req.Volume
	}
	if v < 0 {
		writeErr(w, 400, fmt.Errorf("缺少 value（0-100）"))
		return
	}
	if err := s.SetAlarmVolume(r.Context(), v); err != nil {
		writeErr(w, 502, err)
		return
	}
	vol, _ := s.AlarmVolumeState()
	writeJSON(w, 200, map[string]any{"ok": true, "speaker": s.speaker.ID, "alarm_volume": vol})
}

// hSeek 拖进度条。
func (a *App) hSeek(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PositionMs *int   `json:"position_ms"`
		Position   *int   `json:"position"`
		Speaker    string `json:"speaker"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	pos := -1
	if req.PositionMs != nil {
		pos = *req.PositionMs
	} else if req.Position != nil {
		pos = *req.Position
	}
	if pos < 0 {
		writeErr(w, 400, fmt.Errorf("需要 position_ms"))
		return
	}
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.Seek(ctx, pos); err != nil {
		writeErr(w, 502, err)
		return
	}
	s.probeSoon("进度跳转")
	writeJSON(w, 200, map[string]any{"ok": true, "position_ms": pos})
}

// hLike 收藏 / 取消收藏：{"song_id":123,"like":true}；省略 song_id = 当前曲，
// 省略 like = 取反。
func (a *App) hLike(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SongID  int64  `json:"song_id"`
		Like    *bool  `json:"like"`
		Speaker string `json:"speaker"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s, err := a.pickSession(r, req.Speaker)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	id := req.SongID
	if id == 0 {
		cur, _, ok := s.q.Current()
		if !ok {
			writeErr(w, 409, fmt.Errorf("当前没有在播的歌"))
			return
		}
		id = cur.ID
	}
	like := !a.Liked(id)
	if req.Like != nil {
		like = *req.Like
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_ = ctx
	if err := a.SetLiked(id, like); err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "song_id": id, "liked": like})
}

// hLyric 返回当前曲正在唱的那一行。
//
// 时间轴用服务端已有的进度（探针每 status_poll 秒校正一次，两次之间本地插值），
// 所以不需要设备支持歌词。
func (a *App) hLyric(w http.ResponseWriter, r *http.Request) {
	s, err := a.pickSession(r, "")
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	v := s.q.View(0)
	if v.Current == nil {
		writeJSON(w, 200, map[string]any{"ok": true, "lyric": lyricSnapshot{}})
		return
	}
	lines := a.LyricFor(v.Current.ID)
	idx, cur, next := lyricAt(lines, v.PositionMs)
	snap := lyricSnapshot{
		SongID: v.Current.ID, Line: cur, NextLine: next,
		Index: idx, Total: len(lines), PositionMs: v.PositionMs,
	}
	out := map[string]any{"ok": true, "lyric": snap}
	if r.URL.Query().Get("full") == "1" {
		out["lines"] = lines
	}
	writeJSON(w, 200, out)
}

func (a *App) hLoginCookie(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cookie string `json:"cookie"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Cookie == "" {
		req.Cookie = r.URL.Query().Get("cookie")
	}
	if req.Cookie == "" {
		writeErr(w, 400, fmt.Errorf("缺少 cookie"))
		return
	}
	n := a.jar.Inject(req.Cookie, "music.163.com")
	uid, nick, err := a.ne.Account()
	if err != nil {
		writeErr(w, 400, fmt.Errorf("已写入 %d 条 cookie，但校验失败: %w", n, err))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "uid": uid, "nickname": nick})
}

func (a *App) hLoginQR(w http.ResponseWriter, r *http.Request) {
	key, url, err := a.ne.QRKey()
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	png, err := qrPNG(url)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// unikey 用 header 带出去，方便轮询
	w.Header().Set("X-Unikey", key)
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func (a *App) hLoginQRCheck(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeErr(w, 400, fmt.Errorf("缺少 key"))
		return
	}
	code, msg, cookie, err := a.ne.QRCheck(key)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	out := map[string]any{"ok": true, "code": code, "message": msg}
	if code == 803 {
		if cookie != "" {
			a.jar.Inject(cookie, "music.163.com")
		}
		if uid, nick, err := a.ne.Account(); err == nil {
			out["uid"] = uid
			out["nickname"] = nick
		}
	}
	writeJSON(w, 200, out)
}

func (a *App) hLogout(w http.ResponseWriter, r *http.Request) {
	if a.jar != nil {
		a.jar.Clear()
	}
	a.likedMu.Lock()
	a.liked = nil
	a.likedSong = 0
	a.uidCache = 0
	a.pidCache = ""
	a.likedMu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// hStream 是设备取流入口：/s/<songId> 用网易云 id，现取现跳。
func (a *App) hStream(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("songId 不合法"))
		return
	}
	a.serveSong(w, r, id)
}

func (a *App) serveSong(w http.ResponseWriter, r *http.Request, id int64) {
	url, err := a.resolve(id)
	if err != nil {
		log.Printf("/s/%d 解析失败：%v", id, err)
		writeErr(w, 404, err)
		return
	}
	if a.cfg.StreamMode != "proxy" {
		http.Redirect(w, r, url, http.StatusFound)
		return
	}
	a.proxyStream(w, r, url)
}

func (a *App) proxyStream(w http.ResponseWriter, r *http.Request, target string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}
	if ua := r.Header.Get("User-Agent"); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("proxy 中断：%v", err)
	}
}
