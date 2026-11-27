package app

import (
	"errors"
	"testing"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
)

// 首页推荐只读端点的 httptest 测试：通过 recHooks 替换上游调用，全程不联网。

func newRecApp(hooks *recUp) *App {
	a := &App{}
	a.recHooks = hooks
	return a
}

func recHooksOK() *recUp {
	return &recUp{
		playlists: func() ([]netease.RecPlaylist, error) {
			return []netease.RecPlaylist{{ID: 1, Name: "登录态推荐", Copywriter: "每日"}}, nil
		},
		personalized: func() ([]netease.RecPlaylist, error) {
			return []netease.RecPlaylist{{ID: 2, Name: "全局个性化"}}, nil
		},
		toplist: func() ([]netease.Toplist, error) {
			return []netease.Toplist{{ID: 19723756, Name: "飙升榜"}}, nil
		},
		newAlbums: func() ([]netease.AlbumBrief, error) {
			return []netease.AlbumBrief{{ID: 3, Name: "新专辑", Artist: "歌手"}}, nil
		},
		hotSearch: func() ([]string, error) { return []string{"热词"}, nil },
		fm: func() ([]netease.Track, error) {
			return []netease.Track{{ID: 4, Name: "FM 歌"}}, nil
		},
		searchType: func(q, typ string, limit int) (*netease.SearchResult, error) {
			switch typ {
			case "song":
				return &netease.SearchResult{Tracks: []netease.Track{{ID: 9, Name: q}}}, nil
			case "playlist":
				return &netease.SearchResult{Playlists: []netease.SearchPlaylist{{ID: 10, Name: q}}}, nil
			}
			return &netease.SearchResult{}, nil
		},
		songDetail: func(ids []int64) ([]netease.Track, error) {
			return []netease.Track{{ID: ids[0], Cover: "http://img.example/1.jpg"}}, nil
		},
		palette: func(cover string) (netease.Palette, error) {
			return netease.Palette{Vibrant: "#123456", Dark: "#223344", Muted: "#334455"}, nil
		},
	}
}

// 各端点正常路径：200 + ok=true + 数据在约定字段里。
func TestRecommendEndpointsHappy(t *testing.T) {
	h := newRecApp(recHooksOK()).Routes()
	code, resp := doJSON(t, h, "GET", "/api/recommend/playlists", "")
	if code != 200 || resp["ok"] != true {
		t.Fatalf("code=%d resp=%v", code, resp)
	}
	if resp["source"] != "recommend" {
		t.Fatalf("登录态下 source 应为 recommend：%v", resp["source"])
	}

	code, resp = doJSON(t, h, "GET", "/api/toplist", "")
	if code != 200 || resp["toplist"] == nil {
		t.Fatalf("code=%d resp=%v", code, resp)
	}

	code, resp = doJSON(t, h, "GET", "/api/newalbums", "")
	if code != 200 || resp["albums"] == nil {
		t.Fatalf("code=%d resp=%v", code, resp)
	}

	code, resp = doJSON(t, h, "GET", "/api/search/hot", "")
	if code != 200 || resp["searches"] == nil {
		t.Fatalf("code=%d resp=%v", code, resp)
	}

	code, resp = doJSON(t, h, "GET", "/api/fm", "")
	if code != 200 || resp["tracks"] == nil {
		t.Fatalf("code=%d resp=%v", code, resp)
	}
}

// 未登录/推荐为空时降级到全局个性化歌单，source=personalized。
func TestRecommendDegradesToPersonalized(t *testing.T) {
	hooks := recHooksOK()
	hooks.playlists = func() ([]netease.RecPlaylist, error) { return nil, errors.New("未登录") }
	h := newRecApp(hooks).Routes()
	code, resp := doJSON(t, h, "GET", "/api/recommend/playlists", "")
	if code != 200 || resp["source"] != "personalized" {
		t.Fatalf("应降级 personalized：code=%d resp=%v", code, resp)
	}

	// 登录态返回空列表同样降级
	hooks = recHooksOK()
	hooks.playlists = func() ([]netease.RecPlaylist, error) { return []netease.RecPlaylist{}, nil }
	code, resp = doJSON(t, newRecApp(hooks).Routes(), "GET", "/api/recommend/playlists", "")
	if code != 200 || resp["source"] != "personalized" {
		t.Fatalf("空列表应降级：code=%d resp=%v", code, resp)
	}
}

// 上游失败统一 502：断言 {ok:false, error:"..."} 结构。
func TestRecommendUpstreamFailIs502(t *testing.T) {
	hooks := recHooksOK()
	hooks.toplist = func() ([]netease.Toplist, error) { return nil, errors.New("上游炸了") }
	hooks.playlists = func() ([]netease.RecPlaylist, error) { return nil, errors.New("推荐炸了") }
	hooks.personalized = func() ([]netease.RecPlaylist, error) { return nil, errors.New("降级也炸了") }
	hooks.newAlbums = func() ([]netease.AlbumBrief, error) { return nil, errors.New("新碟炸了") }
	hooks.hotSearch = func() ([]string, error) { return nil, errors.New("热搜炸了") }
	hooks.fm = func() ([]netease.Track, error) { return nil, errors.New("FM 炸了") }
	h := newRecApp(hooks).Routes()

	code, resp := doJSON(t, h, "GET", "/api/toplist", "")
	if code != 502 {
		t.Fatalf("上游失败应 502，得到 %d", code)
	}
	if resp["ok"] != false || resp["error"] == "" {
		t.Fatalf("502 响应结构不对：%v", resp)
	}

	// 推荐端点：主路径和降级路径都挂 → 502
	code, resp = doJSON(t, h, "GET", "/api/recommend/playlists", "")
	if code != 502 || resp["ok"] != false {
		t.Fatalf("双路径失败应 502：code=%d resp=%v", code, resp)
	}

	for _, path := range []string{"/api/newalbums", "/api/search/hot", "/api/fm"} {
		code, _ = doJSON(t, h, "GET", path, "")
		if code != 502 {
			t.Fatalf("%s 上游失败应 502，得到 %d", path, code)
		}
	}
}

// hSearch 向后兼容：type=song（含缺省）响应只有 ok+tracks，结构与旧版一致。
func TestSearchSongBackwardCompat(t *testing.T) {
	h := newRecApp(recHooksOK()).Routes()
	for _, path := range []string{"/api/search?q=hello", "/api/search?q=hello&type=song"} {
		code, resp := doJSON(t, h, "GET", path, "")
		if code != 200 {
			t.Fatalf("%s code=%d", path, code)
		}
		// 旧契约只有 ok 和 tracks 两个键
		if len(resp) != 2 || resp["ok"] != true || resp["tracks"] == nil {
			t.Fatalf("%s 响应结构变了：%v", path, resp)
		}
	}
}

// hSearch 扩展类型：playlist/artist/album 返回 {ok,type,...}。
func TestSearchOtherTypes(t *testing.T) {
	h := newRecApp(recHooksOK()).Routes()
	code, resp := doJSON(t, h, "GET", "/api/search?q=x&type=playlist", "")
	if code != 200 || resp["type"] != "playlist" || resp["playlists"] == nil {
		t.Fatalf("type=playlist 不对：code=%d resp=%v", code, resp)
	}
}

// 参数校验：缺 q 400；type 非法 400；palette id 非法 400。
func TestSearchAndPaletteValidation(t *testing.T) {
	h := newRecApp(recHooksOK()).Routes()
	code, resp := doJSON(t, h, "GET", "/api/search", "")
	if code != 400 || resp["ok"] != false {
		t.Fatalf("缺 q 应 400：code=%d resp=%v", code, resp)
	}
	code, _ = doJSON(t, h, "GET", "/api/search?q=x&type=mv", "")
	if code != 400 {
		t.Fatalf("type=mv 应 400，得到 %d", code)
	}
	code, _ = doJSON(t, h, "GET", "/api/palette/abc", "")
	if code != 400 {
		t.Fatalf("palette id=abc 应 400，得到 %d", code)
	}
	code, _ = doJSON(t, h, "GET", "/api/palette/0", "")
	if code != 400 {
		t.Fatalf("palette id=0 应 400，得到 %d", code)
	}
}

// 取色成功 / 失败兜底。
func TestPalette(t *testing.T) {
	a := newRecApp(recHooksOK())
	h := a.Routes()
	code, resp := doJSON(t, h, "GET", "/api/palette/123", "")
	if code != 200 || resp["ok"] != true {
		t.Fatalf("code=%d resp=%v", code, resp)
	}
	if resp["song_id"] != float64(123) || resp["cover"] != "http://img.example/1.jpg" {
		t.Fatalf("字段不对：%v", resp)
	}
	if resp["vibrant"] != "#123456" || resp["dark"] != "#223344" || resp["muted"] != "#334455" {
		t.Fatalf("颜色不对：%v", resp)
	}
	if _, has := resp["fallback"]; has {
		t.Fatalf("成功时不应有 fallback：%v", resp)
	}

	// 命中缓存：palette 钩子改成报错也不影响（且不再调 songDetail）
	a.recHooks.palette = func(string) (netease.Palette, error) { return netease.Palette{}, errors.New("net down") }
	code, resp = doJSON(t, h, "GET", "/api/palette/123", "")
	if code != 200 || resp["fallback"] != nil || resp["vibrant"] != "#123456" {
		t.Fatalf("缓存命中应返回原色：code=%d resp=%v", code, resp)
	}

	// 取色失败 → 默认调色板 + fallback=true
	code, resp = doJSON(t, a.Routes(), "GET", "/api/palette/456", "")
	if code != 200 {
		t.Fatalf("取色失败不该 500/502，得到 %d", code)
	}
	if resp["fallback"] != true || resp["vibrant"] != "#4ea1ff" || resp["dark"] != "#1a2433" || resp["muted"] != "#2a3542" {
		t.Fatalf("兜底调色板不对：%v", resp)
	}
}
