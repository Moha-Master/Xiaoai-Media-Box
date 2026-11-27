package app

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
)

// 本文件是 WebUI 首页/搜索页的只读推荐端点。全部 GET、不写任何账号状态。

// recUp 汇总这些端点的上游调用。生产直接绑 a.ne；测试通过 recHooks 整体替换，
// 保证单测不联网、又能精确构造 502。
type recUp struct {
	playlists    func() ([]netease.RecPlaylist, error)
	personalized func() ([]netease.RecPlaylist, error)
	toplist      func() ([]netease.Toplist, error)
	newAlbums    func() ([]netease.AlbumBrief, error)
	hotSearch    func() ([]string, error)
	fm           func() ([]netease.Track, error)
	searchType   func(q, typ string, limit int) (*netease.SearchResult, error)
	songDetail   func(ids []int64) ([]netease.Track, error)
	palette      func(cover string) (netease.Palette, error)
}

func (a *App) upstream() *recUp {
	if a.recHooks != nil {
		return a.recHooks
	}
	n := a.ne
	return &recUp{
		playlists:    n.RecommendPlaylists,
		personalized: n.PersonalizedPlaylists,
		toplist:      n.Toplists,
		newAlbums:    n.NewAlbums,
		hotSearch:    n.HotSearches,
		fm:           n.FM,
		searchType:   n.SearchType,
		songDetail:   n.SongDetail,
		palette:      netease.CoverPalette,
	}
}

// callUp 把库调用包进整体超时：musicfox 的接口不收 context，一旦上游卡住
// 只能靠这层 select 兜底，避免首页某个端点把 WebUI 拖死。
// 超时后残留的 goroutine 自然结束、结果被丢弃，无共享状态，race 安全。
func callUp[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type out struct {
		v   T
		err error
	}
	ch := make(chan out, 1)
	go func() {
		v, err := fn()
		ch <- out{v, err}
	}()
	select {
	case o := <-ch:
		return o.v, o.err
	case <-ctx.Done():
		var zero T
		return zero, fmt.Errorf("上游请求超时")
	}
}

// defaultPalette 取色失败时的兜底色（网易云主题蓝）。
var defaultPalette = netease.Palette{Vibrant: "#4ea1ff", Dark: "#1a2433", Muted: "#2a3542"}

// hRecommendPlaylists 首页歌单推荐：登录态推荐优先，未登录或返回空时
// 降级到无需登录的全局个性化歌单（这也是 PersonalizedPlaylists 的存在意义），
// 所以这里不需要显式先调 Account()——推荐接口对失效登录态自然会报错。
func (a *App) hRecommendPlaylists(w http.ResponseWriter, r *http.Request) {
	up := a.upstream()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	pls, err := callUp(ctx, up.playlists)
	source := "recommend"
	if err != nil || len(pls) == 0 {
		pls, err = callUp(ctx, up.personalized)
		source = "personalized"
		if err != nil {
			writeErr(w, 502, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "playlists": pls, "source": source})
}

// hToplist 官方榜单列表。
func (a *App) hToplist(w http.ResponseWriter, r *http.Request) {
	up := a.upstream()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := callUp(ctx, up.toplist)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "toplist": list})
}

// hNewAlbums 新碟上架。
func (a *App) hNewAlbums(w http.ResponseWriter, r *http.Request) {
	up := a.upstream()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	albums, err := callUp(ctx, up.newAlbums)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "albums": albums})
}

// hHotSearches 搜索页热搜词。
func (a *App) hHotSearches(w http.ResponseWriter, r *http.Request) {
	up := a.upstream()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	hots, err := callUp(ctx, up.hotSearch)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "searches": hots})
}

// hFM 私人 FM 试听列表。FM() 对未登录/上游异常返回空切片，不会 502。
func (a *App) hFM(w http.ResponseWriter, r *http.Request) {
	up := a.upstream()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tracks, err := callUp(ctx, up.fm)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "tracks": tracks})
}

// palEntry 是 palCache 的值：调色板连带封面一起存，命中时响应不缺字段。
type palEntry struct {
	pal   netease.Palette
	cover string
}

// hPalette 按歌曲 id 取封面主色。缓存按 songID 放在 App.palCache（sync.Map）。
// 取色失败（网络/解码/无封面）不报 500：返回默认调色板 + fallback=true，
// 前端永远有颜色可用。
func (a *App) hPalette(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, fmt.Errorf("songId 不合法"))
		return
	}
	if v, ok := a.palCache.Load(id); ok {
		e := v.(palEntry)
		writeJSON(w, 200, paletteResp(id, e.cover, e.pal, false))
		return
	}
	up := a.upstream()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cover := ""
	if ts, err := callUp(ctx, func() ([]netease.Track, error) { return up.songDetail([]int64{id}) }); err == nil && len(ts) > 0 {
		cover = ts[0].Cover
	}
	pal, err := callUp(ctx, func() (netease.Palette, error) { return up.palette(cover) })
	fallback := err != nil || cover == ""
	if fallback {
		pal = defaultPalette
	}
	a.palCache.Store(id, palEntry{pal: pal, cover: cover})
	writeJSON(w, 200, paletteResp(id, cover, pal, fallback))
}

func paletteResp(id int64, cover string, p netease.Palette, fallback bool) map[string]any {
	out := map[string]any{
		"ok":      true,
		"song_id": id,
		"cover":   cover,
		"vibrant": p.Vibrant,
		"dark":    p.Dark,
		"muted":   p.Muted,
	}
	if fallback {
		out["fallback"] = true
	}
	return out
}
