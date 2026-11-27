package netease

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
	"github.com/go-musicfox/netease-music/service"
	musicutil "github.com/go-musicfox/netease-music/util"
)

// 本文件聚合 WebUI 首页/搜索页要用的只读推荐类接口。
// 全部走 musicfox 的 **weapi** 签名通道（linuxapi 通道已被网易掐断，见
// Toplists/Lyric 注释），不直连已 404 的老 web API。
// 各方法返回归一化结构，不把 musicfox 的原始 JSON 漏给上层。
//
// 解析逻辑抽成 parseXxx 纯函数（*_test.go 用构造 JSON 单测），方法本身只剩
// 「调上游 + 判 code + 调解析」三步。

// flexInt 兼容 JSON 数字的多种写法：上游会把大数打成科学计数法
// （实测 personalized 接口出现过 2.55e10 的 playCount），Go 的 int64
// 解不了这种字面量，先按 float64 收再转。
//
// 也要兼容**带引号的数字**：playmode/intelligence/list 失败时回的是
// `code:"500"`（字符串），按 int 收会直接炸成"解析失败"，把上游的真实
// 错误码吞掉（2026-11 实测）。
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	if len(b) >= 2 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			*f = 0
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*f = flexInt(v)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	v, err := n.Float64()
	if err != nil {
		return err
	}
	*f = flexInt(v)
	return nil
}

// RecPlaylist 推荐歌单条目（登录态推荐 & 全局个性化通用）。
type RecPlaylist struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Cover      string  `json:"pic_url"`
	PlayCount  flexInt `json:"play_count"`
	TrackCount int     `json:"track_count"`
	Copywriter string  `json:"copywriter"`
}

// Toplist 榜单条目（/api/toplist）。
type Toplist struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Cover     string  `json:"cover_url"`
	PlayCount flexInt `json:"play_count"`
	Type      string  `json:"type"` // 上游 ToplistType 分类标记
}

// AlbumBrief 新碟条目。艺术家拼成字符串，与 Track.Artists 的展示习惯一致。
type AlbumBrief struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	PicURL string `json:"pic_url"`
	Artist string `json:"artist"`
}

// SearchPlaylist / SearchArtist / SearchAlbum 是多类型搜索的精简结果，
// 字段与任务书约定的上游字段一一对应，只做 snake_case 改名。
type SearchPlaylist struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Cover      string  `json:"cover_url"`
	PlayCount  flexInt `json:"play_count"`
	TrackCount int     `json:"track_count"`
}

type SearchArtist struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	PicURL    string   `json:"pic_url"`
	Alias     []string `json:"alias"`
	MusicSize int      `json:"music_size"`
	AlbumSize int      `json:"album_size"`
}

type SearchAlbum struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Artist      string `json:"artist"`
	PicURL      string `json:"pic_url"`
	SongsCount  int    `json:"songs_count"`
	PublishTime int64  `json:"publish_time"`
}

// SearchResult 是 SearchType 的归一化容器：type=song 填 Tracks，
// 其余类型各填一个。上层按 type 取对应切片。
type SearchResult struct {
	Tracks    []Track          `json:"tracks,omitempty"`
	Playlists []SearchPlaylist `json:"playlists,omitempty"`
	Artists   []SearchArtist   `json:"artists,omitempty"`
	Albums    []SearchAlbum    `json:"albums,omitempty"`
}

// searchTypeCodes 把对外类型名映射成 musicfox SearchService.Type 的数字。
var searchTypeCodes = map[string]string{
	"song":     "1",
	"playlist": "1000",
	"artist":   "100",
	"album":    "10",
}

// RecommendPlaylists 登录态「每日歌单」个性化推荐（weapi /v1/discovery/recommend/resource，约 8 条）。
func (n *Netease) RecommendPlaylists() ([]RecPlaylist, error) {
	svc := &service.RecommendResourceService{}
	code, body := svc.RecommendResource()
	if code != 200 {
		return nil, fmt.Errorf("推荐歌单失败 code=%v body=%s", code, util.Snippet(body))
	}
	return parseRecommendPlaylists(body)
}

// parseRecommendPlaylists 拆 {recommend:[{id,name,picUrl,playcount,...}]}。
// 注意这个接口的 playcount 是全小写（personalized 的是 playCount），
// json 反序列化大小写不敏感，两个写法都能对上。
func parseRecommendPlaylists(body []byte) ([]RecPlaylist, error) {
	var r struct {
		Recommend []struct {
			ID         int64   `json:"id"`
			Name       string  `json:"name"`
			PicURL     string  `json:"picUrl"`
			PlayCount  flexInt `json:"playcount"`
			TrackCount int     `json:"trackCount"`
			Copywriter string  `json:"copywriter"`
		} `json:"recommend"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := make([]RecPlaylist, 0, len(r.Recommend))
	for _, p := range r.Recommend {
		out = append(out, RecPlaylist{
			ID: p.ID, Name: p.Name, Cover: p.PicURL,
			PlayCount: p.PlayCount, TrackCount: p.TrackCount, Copywriter: p.Copywriter,
		})
	}
	return out, nil
}

// PersonalizedPlaylists 全局个性化歌单（weapi /personalized/playlist，约 30 条）。
// 不依赖登录态，作为未登录时的降级数据源。
func (n *Netease) PersonalizedPlaylists() ([]RecPlaylist, error) {
	svc := &service.PersonalizedService{}
	code, body := svc.Personalized()
	if code != 200 {
		return nil, fmt.Errorf("个性化歌单失败 code=%v body=%s", code, util.Snippet(body))
	}
	return parsePersonalizedPlaylists(body)
}

func parsePersonalizedPlaylists(body []byte) ([]RecPlaylist, error) {
	var r struct {
		Result []struct {
			ID         int64   `json:"id"`
			Name       string  `json:"name"`
			PicURL     string  `json:"picUrl"`
			PlayCount  flexInt `json:"playCount"`
			TrackCount int     `json:"trackCount"`
			Copywriter string  `json:"copywriter"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := make([]RecPlaylist, 0, len(r.Result))
	for _, p := range r.Result {
		out = append(out, RecPlaylist{
			ID: p.ID, Name: p.Name, Cover: p.PicURL,
			PlayCount: p.PlayCount, TrackCount: p.TrackCount, Copywriter: p.Copywriter,
		})
	}
	return out, nil
}

// Toplists 官方榜单列表（linuxapi /api/toplist）。
// Toplists 拉官方排行榜列表。
//
// 不走 service.ToplistService：它用 linuxapi 签名，与 LyricService 同一条已被
// 网易掐断的通道（2026-11 实测 6/6 全挂，返回 502 HTML）。同一 URL 换 weapi
// 签名正常返回（探针 /tmp/opencode/lyricprobe），响应结构与 linuxapi 版一致，
// parseToplists 不变。
func (n *Netease) Toplists() ([]Toplist, error) {
	code, body, _ := musicutil.CreateRequest("POST", "https://music.163.com/api/toplist",
		map[string]string{}, &musicutil.Options{Crypto: "weapi"})
	if code != 200 {
		return nil, fmt.Errorf("榜单失败 code=%v body=%s", code, util.Snippet(body))
	}
	return parseToplists(body)
}

func parseToplists(body []byte) ([]Toplist, error) {
	var r struct {
		List []struct {
			ID        int64   `json:"id"`
			Name      string  `json:"name"`
			CoverURL  string  `json:"coverImgUrl"`
			PlayCount flexInt `json:"playCount"`
			Type      string  `json:"ToplistType"`
		} `json:"list"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := make([]Toplist, 0, len(r.List))
	for _, t := range r.List {
		out = append(out, Toplist{ID: t.ID, Name: t.Name, Cover: t.CoverURL, PlayCount: t.PlayCount, Type: t.Type})
	}
	return out, nil
}

// NewAlbums 最新上架专辑（weapi /api/discovery/newAlbum）。
// artist 字段新老接口两种形态都见过：单对象 artist 或数组 artists，都兜住。
func (n *Netease) NewAlbums() ([]AlbumBrief, error) {
	svc := &service.AlbumNewestService{}
	code, body := svc.AlbumNewest()
	if code != 200 {
		return nil, fmt.Errorf("新碟上架失败 code=%v body=%s", code, util.Snippet(body))
	}
	return parseNewAlbums(body)
}

func parseNewAlbums(body []byte) ([]AlbumBrief, error) {
	var r struct {
		Albums []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			PicURL string `json:"picUrl"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
			Artists []struct {
				Name string `json:"name"`
			} `json:"artists"`
		} `json:"albums"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := make([]AlbumBrief, 0, len(r.Albums))
	for _, a := range r.Albums {
		artist := a.Artist.Name
		if artist == "" {
			for _, ar := range a.Artists {
				if ar.Name != "" {
					if artist != "" {
						artist += "/"
					}
					artist += ar.Name
				}
			}
		}
		out = append(out, AlbumBrief{ID: a.ID, Name: a.Name, PicURL: a.PicURL, Artist: artist})
	}
	return out, nil
}

// HotSearches 热搜词（weapi /search/hot，result.hots[].first）。
func (n *Netease) HotSearches() ([]string, error) {
	svc := &service.SearchHotService{}
	code, body := svc.SearchHot()
	if code != 200 {
		return nil, fmt.Errorf("热搜失败 code=%v body=%s", code, util.Snippet(body))
	}
	return parseHotSearches(body)
}

func parseHotSearches(body []byte) ([]string, error) {
	var r struct {
		Result struct {
			Hots []struct {
				First string `json:"first"`
			} `json:"hots"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(r.Result.Hots))
	for _, h := range r.Result.Hots {
		if h.First != "" {
			out = append(out, h.First)
		}
	}
	return out, nil
}

// FM 私人 FM 下一批曲目（weapi /v1/radio/get）。
// data[] 每项主体歌曲在 mainSong 里，个别老响应直接平铺在项上，两种都兼容。
// 未登录或上游异常时按任务书约定返回空切片、不报错——FM 只是首页填充，
// 失败不值得让整个端点 502。
func (n *Netease) FM() ([]Track, error) {
	svc := &service.PersonalFmService{}
	code, body := svc.PersonalFm()
	if code != 200 {
		return []Track{}, nil
	}
	return parseFM(body)
}

// parseFM 兼容两种 data 项形态：平铺（字段直接挂项上）或 mainSong 包一层。
func parseFM(body []byte) ([]Track, error) {
	var r struct {
		Data []struct {
			apiSong
			MainSong *apiSong `json:"mainSong"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("私人 FM 解析失败：%v body=%s", err, util.Snippet(body))
	}
	out := make([]Track, 0, len(r.Data))
	for _, d := range r.Data {
		s := d.apiSong
		if d.MainSong != nil {
			s = *d.MainSong
		}
		if s.ID == 0 {
			continue
		}
		out = append(out, toTracks([]apiSong{s})[0])
	}
	return out, nil
}

// SearchType 多类型搜索：typ ∈ song|playlist|artist|album。
// song 复用现有 apiSong 归一化；其余返回各自精简结构。
func (n *Netease) SearchType(q, typ string, limit int) (*SearchResult, error) {
	code, ok := searchTypeCodes[typ]
	if !ok {
		return nil, fmt.Errorf("不支持的搜索类型 %q", typ)
	}
	if limit <= 0 {
		limit = 10
	}
	svc := &service.SearchService{S: q, Type: code, Limit: strconv.Itoa(limit), Offset: "0"}
	httpCode, body := svc.Search()
	if httpCode != 200 {
		return nil, fmt.Errorf("搜索失败 code=%v body=%s", httpCode, util.Snippet(body))
	}
	return parseSearchResult(body, typ)
}

// parseSearchResult 按 typ 拆 cloudsearch/pc 的 result.*。
func parseSearchResult(body []byte, typ string) (*SearchResult, error) {
	switch typ {
	case "song":
		var r struct {
			Result struct {
				Songs []apiSong `json:"songs"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		return &SearchResult{Tracks: toTracks(r.Result.Songs)}, nil
	case "playlist":
		var r struct {
			Result struct {
				Playlists []struct {
					ID         int64   `json:"id"`
					Name       string  `json:"name"`
					CoverURL   string  `json:"coverImgUrl"`
					PlayCount  flexInt `json:"playCount"`
					TrackCount int     `json:"trackCount"`
				} `json:"playlists"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		res := &SearchResult{Playlists: make([]SearchPlaylist, 0, len(r.Result.Playlists))}
		for _, p := range r.Result.Playlists {
			res.Playlists = append(res.Playlists, SearchPlaylist{
				ID: p.ID, Name: p.Name, Cover: p.CoverURL, PlayCount: p.PlayCount, TrackCount: p.TrackCount,
			})
		}
		return res, nil
	case "artist":
		var r struct {
			Result struct {
				Artists []struct {
					ID        int64    `json:"id"`
					Name      string   `json:"name"`
					PicURL    string   `json:"picUrl"`
					Alias     []string `json:"alias"`
					MusicSize int      `json:"musicSize"`
					AlbumSize int      `json:"albumSize"`
				} `json:"artists"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		res := &SearchResult{Artists: make([]SearchArtist, 0, len(r.Result.Artists))}
		for _, a := range r.Result.Artists {
			res.Artists = append(res.Artists, SearchArtist{
				ID: a.ID, Name: a.Name, PicURL: a.PicURL, Alias: a.Alias,
				MusicSize: a.MusicSize, AlbumSize: a.AlbumSize,
			})
		}
		return res, nil
	default: // album
		var r struct {
			Result struct {
				Albums []struct {
					ID          int64  `json:"id"`
					Name        string `json:"name"`
					PicURL      string `json:"picUrl"`
					SongsCount  int    `json:"songsCount"`
					PublishTime int64  `json:"publishTime"`
					Artist      struct {
						Name string `json:"name"`
					} `json:"artist"`
				} `json:"albums"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		res := &SearchResult{Albums: make([]SearchAlbum, 0, len(r.Result.Albums))}
		for _, a := range r.Result.Albums {
			res.Albums = append(res.Albums, SearchAlbum{
				ID: a.ID, Name: a.Name, Artist: a.Artist.Name, PicURL: a.PicURL,
				SongsCount: a.SongsCount, PublishTime: a.PublishTime,
			})
		}
		return res, nil
	}
}
