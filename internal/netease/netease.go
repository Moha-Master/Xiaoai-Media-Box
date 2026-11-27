package netease

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/auth"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
	"github.com/go-musicfox/netease-music/service"
	musicutil "github.com/go-musicfox/netease-music/util"
)

// 时间轴约定（全项目统一，改任何一处之前先读这段）：
//
//	服务端内部（queue 的位置、SSE/API 的 position_ms、歌词匹配、前端显示）
//	一律使用「原曲时间轴」——歌词时间戳本来就是原曲轴，副歌切片（start=69s）
//	这样映射后歌词才不会整段错位。
//
//	只有与设备对话的两个边界做换算：
//	  - 收到设备回报位置时 +TrialStartMs（设备播的是截断后的片段文件，
//	    它的 0 秒 = 原曲的 start 处）；
//	  - 向设备下发 seek 时 −TrialStartMs。
//
// Track 是队列里的最小单位。ID 用网易云 songId，同时会作为设备侧的 audio_id。
// Duration 保持**原曲总时长**（歌曲详情的 dt），不要用片段长度改写它——
// 用户认得这首歌的真实长度；可播区间用 TrialStartMs/TrialEndMs 表达。
type Track struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Artists  string `json:"artists"`
	Album    string `json:"album"`
	Cover    string `json:"cover"`
	Duration int    `json:"duration_ms"`

	// 试听区间（原曲时间轴上的毫秒）。0 = 全曲可播。
	// 只能由 resolve/SongURLs 的 freeTrialInfo 写入，其他来源（搜索/歌单详情）
	// 不带这个信息，保持 0。
	TrialStartMs int `json:"trial_start_ms,omitempty"`
	TrialEndMs   int `json:"trial_end_ms,omitempty"`
}

// IsTrial 判断是否试听片段：有终点且短于原曲才算（end == Duration 视为全曲）。
func (t Track) IsTrial() bool { return t.TrialEndMs > 0 && t.TrialEndMs < t.Duration }

// PlayableEndMs 原曲轴上的可播终点：试听曲是片段终点，普通曲是曲尾。
func (t Track) PlayableEndMs() int {
	if t.IsTrial() {
		return t.TrialEndMs
	}
	return t.Duration
}

// ClipMs 可播长度（毫秒）＝设备实际拿到的音频文件长度。
func (t Track) ClipMs() int { return t.PlayableEndMs() - t.TrialStartMs }

func (t Track) Label() string {
	if t.Artists == "" {
		return t.Name
	}
	return t.Artists + " - " + t.Name
}

// PlayInfo 是一首歌的真实可播信息（来自 song/enhance/player/url/v1）。
//
// StartMs/EndMs 是**原曲时间轴**上的可播区间（freeTrialInfo.start/end × 1000）；
// ClipMs 是服务端返回的文件真实长度（time 字段）。实测（132 首零例外）：
//
//	time ≈ (end − start) × 1000 + 30~40ms
//
// 即网易返回的就是截断后的片段文件。普通全曲 StartMs=EndMs=0、ClipMs≈曲长
// （个别响应 time=0，调用方应回退用 Track.Duration）。
type PlayInfo struct {
	URL     string
	Expi    int    // 秒
	ClipMs  int    // 服务端返回的文件真实长度（time 字段）；0 表示响应没给
	StartMs int    // 可播区间起点，原曲时间轴；0 = 从头
	EndMs   int    // 可播区间终点，原曲时间轴；0 = 全曲可播
	Br      int    // 比特率
	Level   string // 实际音质（试听曲会被降成 standard）
}

func (p PlayInfo) ok() bool { return p.URL != "" }

// apiSong 兼容 cloudsearch / v3/song/detail / playlist detail / album / daily 几种返回。
type apiSong struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Ar   []struct {
		Name string `json:"name"`
	} `json:"ar"`
	Al struct {
		Name   string `json:"name"`
		PicURL string `json:"picUrl"`
	} `json:"al"`
	Dt int `json:"dt"`
}

func toTracks(in []apiSong) []Track {
	out := make([]Track, 0, len(in))
	for _, s := range in {
		names := make([]string, 0, len(s.Ar))
		for _, a := range s.Ar {
			if a.Name != "" {
				names = append(names, a.Name)
			}
		}
		out = append(out, Track{
			ID:       s.ID,
			Name:     s.Name,
			Artists:  strings.Join(names, "/"),
			Album:    s.Al.Name,
			Cover:    s.Al.PicURL,
			Duration: s.Dt,
		})
	}
	return out
}

// Netease 是 netease-music 库的薄封装：只暴露本服务要用的那几个 API。
type Netease struct {
	Quality string
}

// Search 单曲搜索（type=1）。HA 集成在用，签名与响应解析保持不变；
// 实现统一委托给多类型的 SearchType。
func (n *Netease) Search(q string, limit int) ([]Track, error) {
	res, err := n.SearchType(q, "song", limit)
	if err != nil {
		return nil, err
	}
	return res.Tracks, nil
}

func (n *Netease) PlaylistTracks(id string) ([]Track, error) {
	svc := &service.PlaylistTrackAllService{Id: id}
	code, body := svc.AllTracks()
	if code != 200 {
		return nil, fmt.Errorf("playlist 失败 code=%v body=%s", code, util.Snippet(body))
	}
	var r struct {
		Playlist struct {
			Name   string    `json:"name"`
			Tracks []apiSong `json:"tracks"`
		} `json:"playlist"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	return toTracks(r.Playlist.Tracks), nil
}

func (n *Netease) AlbumTracks(id string) ([]Track, error) {
	svc := &service.AlbumService{ID: id}
	code, body := svc.Album()
	if code != 200 {
		return nil, fmt.Errorf("album 失败 code=%v body=%s", code, util.Snippet(body))
	}
	var r struct {
		Songs []apiSong `json:"songs"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	return toTracks(r.Songs), nil
}

func (n *Netease) DailySongs() ([]Track, error) {
	svc := &service.RecommendSongsService{}
	code, body := svc.RecommendSongs()
	if code != 200 {
		return nil, fmt.Errorf("每日推荐失败 code=%v body=%s", code, util.Snippet(body))
	}
	var r struct {
		Data struct {
			DailySongs []apiSong `json:"dailySongs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	return toTracks(r.Data.DailySongs), nil
}

// PlaylistInfo 是「我的歌单」列表项，WebUI / HA 浏览用。
type PlaylistInfo struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Cover      string `json:"cover"`
	TrackCount int    `json:"track_count"`
	PlayCount  int64  `json:"play_count"`
}

// UserPlaylists 拉当前登录用户的歌单列表（含自己创建和收藏的）。
func (n *Netease) UserPlaylists(uid string) ([]PlaylistInfo, error) {
	svc := &service.UserPlaylistService{Uid: uid, Limit: "100"}
	code, body := svc.UserPlaylist()
	if code != 200 {
		return nil, fmt.Errorf("user playlist 失败 code=%v body=%s", code, util.Snippet(body))
	}
	var r struct {
		Playlist []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			CoverImgURL string `json:"coverImgUrl"`
			TrackCount  int    `json:"trackCount"`
			PlayCount   int64  `json:"playCount"`
		} `json:"playlist"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := make([]PlaylistInfo, 0, len(r.Playlist))
	for _, p := range r.Playlist {
		out = append(out, PlaylistInfo{
			ID: p.ID, Name: p.Name, Cover: p.CoverImgURL,
			TrackCount: p.TrackCount, PlayCount: p.PlayCount,
		})
	}
	return out, nil
}

func (n *Netease) SongDetail(ids []int64) ([]Track, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	svc := &service.SongDetailService{Ids: strings.Join(parts, ",")}
	code, body := svc.SongDetail()
	if code != 200 {
		return nil, fmt.Errorf("song detail 失败 code=%v body=%s", code, util.Snippet(body))
	}
	var r struct {
		Songs []apiSong `json:"songs"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	return toTracks(r.Songs), nil
}

// songURLLevel 返回配置对应的音质档与编码格式。
func songURLLevel(quality string) (service.SongQualityLevel, string) {
	level := service.SongQualityLevel(quality)
	if !level.IsValid() {
		level = service.Exhigh
	}
	encode := "mp3"
	switch level {
	case service.Lossless, service.Hires, service.JYMaster, service.Sky, service.JYEffect:
		encode = "flac"
	}
	return level, encode
}

// trialRange 是 freeTrialInfo：start/end 是**原曲时间轴上的秒**。
type trialRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// songURLItem 是 song/enhance/player/url/v1 单条响应里我们关心的字段。
type songURLItem struct {
	ID    int64       `json:"id"`
	URL   string      `json:"url"`
	Code  int         `json:"code"`
	Expi  int         `json:"expi"`
	Time  int         `json:"time"`
	Br    int         `json:"br"`
	Level string      `json:"level"`
	Trial *trialRange `json:"freeTrialInfo"`
}

// parsePlayInfo 把单条 songURLItem 转成 PlayInfo。url 为空（版权/VIP 且无试听）
// 返回零值 + false。
func parsePlayInfo(d songURLItem) (PlayInfo, bool) {
	if d.URL == "" {
		return PlayInfo{}, false
	}
	expi := d.Expi
	if expi <= 0 {
		expi = 600
	}
	info := PlayInfo{
		URL:    d.URL,
		Expi:   expi,
		ClipMs: d.Time,
		Br:     d.Br,
		Level:  d.Level,
	}
	if d.Trial != nil && d.Trial.End > 0 {
		info.StartMs = d.Trial.Start * 1000
		info.EndMs = d.Trial.End * 1000
	}
	return info, true
}

// songURLRequest 发一次 song/enhance/player/url/v1（ids 支持逗号分隔批量，实测 20 首全回）。
func songURLRequest(ids []int64, level service.SongQualityLevel, encode string) ([]byte, error) {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	svc := &service.SongUrlV1Service{
		ID:         strings.Join(parts, ","),
		Level:      level,
		EncodeType: encode,
		SkipUNM:    true,
	}
	code, body, err := svc.SongUrl()
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("song url 失败 code=%v body=%s", code, util.Snippet(body))
	}
	return body, nil
}

// SongURL 返回可播放直链与有效期（网易云给的 expi，通常 1200 秒）。
// 兼容旧调用方：只关心 URL 的地方继续用这个；需要试听区间的走 SongURLs/resolveInfo。
func (n *Netease) SongURL(id int64) (string, int, error) {
	info, err := n.SongURLs([]int64{id})
	if err != nil {
		return "", 0, err
	}
	p, ok := info[id]
	if !ok {
		return "", 0, fmt.Errorf("无可用直链（版权/VIP 限制）")
	}
	return p.URL, p.Expi, nil
}

// songURLBatchSize 是一次批量请求的最大曲目数。实测 20 首一次全回（2026-11），
// 取 20 保守一点：热河池 119 首也就 6 个请求。
const songURLBatchSize = 20

// SongURLs 批量取直链与可播区间：一次网络请求替代 N 次逐首 resolve。
// 返回的 map 只包含**有直链**的 id；拿不到的（code=404 / url 空）不在 map 里。
func (n *Netease) SongURLs(ids []int64) (map[int64]PlayInfo, error) {
	out := map[int64]PlayInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	level, encode := songURLLevel(n.Quality)
	for start := 0; start < len(ids); start += songURLBatchSize {
		end := start + songURLBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		body, err := songURLRequest(batch, level, encode)
		if err != nil {
			return nil, err
		}
		var r struct {
			Data []songURLItem `json:"data"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		for _, d := range r.Data {
			if info, ok := parsePlayInfo(d); ok {
				out[d.ID] = info
			}
		}
	}
	return out, nil
}

// QRKey 申请一个登录二维码：返回 unikey 与可直接编码成二维码的 URL。
func (n *Netease) QRKey() (string, string, error) {
	svc := &service.LoginQRService{}
	code, body, qrURL, err := svc.GetKey()
	if err != nil {
		return "", "", err
	}
	if code != 200 || svc.UniKey == "" {
		return "", "", fmt.Errorf("二维码申请失败 code=%v body=%s", code, util.Snippet(body))
	}
	return svc.UniKey, qrURL, nil
}

// QRCheck 轮询扫码状态：800 过期 / 801 待扫 / 802 待确认 / 803 成功。
//
// 注意：网易云把 800~803 放在响应体的 code 字段里，HTTP 和库的返回值都不是 200，
// 不能按"非 200 即错误"处理。
func (n *Netease) QRCheck(key string) (int, string, string, error) {
	svc := &service.LoginQRService{UniKey: key}
	code, body, err := svc.CheckQR()
	if err != nil {
		return 0, "", "", err
	}
	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Cookie  string `json:"cookie"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return int(code), "", "", fmt.Errorf("扫码响应解析失败 code=%v body=%s", code, util.Snippet(body))
	}
	if r.Code == 0 {
		r.Code = int(code)
	}
	switch r.Code {
	case 800, 801, 802, 803:
		return r.Code, r.Message, r.Cookie, nil
	}
	return r.Code, r.Message, "", fmt.Errorf("扫码轮询失败 code=%d %s", r.Code, r.Message)
}

func (n *Netease) Account() (int64, string, error) {
	svc := &service.UserAccountService{}
	code, body := svc.AccountInfo()
	if code != 200 {
		return 0, "", fmt.Errorf("未登录或登录失效（code=%v）", code)
	}
	var r struct {
		Profile struct {
			UserID   int64  `json:"userId"`
			Nickname string `json:"nickname"`
			VipType  int    `json:"vipType"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, "", err
	}
	if r.Profile.UserID == 0 {
		return 0, "", fmt.Errorf("未登录")
	}
	return r.Profile.UserID, r.Profile.Nickname, nil
}

// Like 收藏 / 取消收藏一首歌。
//
// 走的是 app.App 端老接口 /api/radio/like（需要 os=pc cookie，库已带），
// 返回体里的 code 才是真实结果；HTTP 层面往往是 200。
func (n *Netease) Like(id int64, like bool) error {
	svc := &service.LikeService{ID: strconv.FormatInt(id, 10), L: strconv.FormatBool(like)}
	code, body := svc.Like()
	if code != 200 {
		return fmt.Errorf("收藏请求失败 code=%v body=%s", code, util.Snippet(body))
	}
	var r struct {
		Code flexInt `json:"code"` // 上游失败态可能回字符串 code，用 flexInt 兜住
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("收藏响应解析失败：%v body=%s", err, util.Snippet(body))
	}
	if int(r.Code) != 200 && int(r.Code) != 0 {
		return fmt.Errorf("收藏失败 code=%d body=%s", int(r.Code), util.Snippet(body))
	}
	return nil
}

// LikedIDs 拉“我喜欢的音乐”全量 song id（新版 /weapi/song/like/get）。
func (n *Netease) LikedIDs(uid int64) ([]int64, error) {
	svc := &service.LikeListService{UID: strconv.FormatInt(uid, 10)}
	code, body := svc.LikeList()
	if code != 200 {
		return nil, fmt.Errorf("拉取喜欢列表失败 code=%v body=%s", code, util.Snippet(body))
	}
	var r struct {
		Code int     `json:"code"`
		IDs  []int64 `json:"ids"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("喜欢列表解析失败：%v body=%s", err, util.Snippet(body))
	}
	if r.Code != 200 && r.Code != 0 {
		return nil, fmt.Errorf("喜欢列表失败 code=%d", r.Code)
	}
	return r.IDs, nil
}

// LyricLine 是一行歌词：T 是这一行开始的时间（毫秒）。
type LyricLine struct {
	T    int    `json:"t_ms"`
	Text string `json:"text"`
}

// Lyric 拉一首歌的 LRC 歌词并解析成按时间排序的行（无歌词返回空切片）。
//
// 不走 service.LyricService：它用 linuxapi 签名，网易已把那条通道下线
// （2026-11 实测返回 502 HTML 错误页）。同一个端点换 **weapi** 签名仍是活的
// （与 NeteaseCloudMusicApi 现行实现一致），返回标准 LRC、解析逻辑不变。
// 探针程序见 /tmp/opencode/lyricprobe（三端点对照，v1/weapi 的 lrc.lyric
// 是结构化 JSON 而非 LRC，故不采用）。
func (n *Netease) Lyric(id int64) ([]LyricLine, error) {
	code, body, _ := musicutil.CreateRequest("POST", "https://music.163.com/api/song/lyric",
		map[string]string{
			"id": strconv.FormatInt(id, 10),
			"lv": "-1",
			"kv": "-1",
			"tv": "-1",
		},
		&musicutil.Options{
			Crypto:  "weapi",
			Cookies: []*http.Cookie{{Name: "os", Value: "pc"}},
		})
	if code != 200 {
		return nil, fmt.Errorf("歌词请求失败 code=%v body=%s", code, util.Snippet(body))
	}
	var r struct {
		Code flexInt `json:"code"` // 失败态可能回字符串 code；这里不判它，但不能因为类型不对就整包解析失败
		Lrc  struct {
			Lyric string `json:"lyric"`
		} `json:"lrc"`
		Tlyric struct {
			Lyric string `json:"lyric"`
		} `json:"tlyric"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("歌词解析失败：%v body=%s", err, util.Snippet(body))
	}
	lines := parseLRC(r.Lrc.Lyric)
	if len(lines) == 0 {
		return nil, nil
	}
	// 有中文翻译时叠加到原文后面（不替换原文，避免丢掉歌手名字之类的信息）
	tr := parseLRC(r.Tlyric.Lyric)
	if len(tr) > 0 {
		byT := make(map[int]string, len(tr))
		for _, l := range tr {
			byT[l.T] = l.Text
		}
		for i := range lines {
			if t, ok := byT[lines[i].T]; ok && t != "" && t != lines[i].Text {
				lines[i].Text = lines[i].Text + " / " + t
			}
		}
	}
	return lines, nil
}

// parseLRC 解析 LRC：[mm:ss.xx] 或 [mm:ss:xx]，一行可以有多个时间戳。
func parseLRC(s string) []LyricLine {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := make([]LyricLine, 0, 64)
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimRight(raw, "\r")
		var stamps []int
		for len(line) > 0 && line[0] == '[' {
			j := strings.IndexByte(line, ']')
			if j < 0 {
				break
			}
			ms, ok := parseStamp(line[1:j])
			if !ok {
				break
			}
			stamps = append(stamps, ms)
			line = line[j+1:]
		}
		text := strings.TrimSpace(line)
		if len(stamps) == 0 || text == "" {
			continue
		}
		for _, ms := range stamps {
			out = append(out, LyricLine{T: ms, Text: text})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].T < out[b].T })
	return out
}

func parseStamp(s string) (int, bool) {
	sep := ":"
	if !strings.Contains(s, sep) {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, false
	}
	mm, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err1 != nil {
		return 0, false
	}
	rest := strings.ReplaceAll(parts[1], ",", ".")
	sec := 0.0
	if _, err := fmt.Sscanf(rest, "%f", &sec); err != nil {
		return 0, false
	}
	return mm*60000 + int(sec*1000), true
}

// hotSong 是「心动模式」返回项的 songInfo：apiSong 的字段 + 版权标记。
type hotSong struct {
	apiSong
	Privilege struct {
		St int `json:"st"` // <0 = 无版权
	} `json:"privilege"`
}

// Hotmode 拉「心动模式」推荐列表：以 seed 为种子、在 playlistID 的语境下算推荐。
//
// 用的是 /weapi/playmode/intelligence/list。库里虽然有对口的
// PlaymodeIntelligenceListService，但它有个 bug——`data["count"]` 从未被赋值，
// 于是永远走 `"1"` 分支，只会拿回 1 首。所以这里直接发原始请求。
//
// 实测服务端**会忽略 count 的取值**（1 / 50 / 200 都回 119 条），但**这个键必须
// 存在**：缺键上游回 `{"success":false,"code":"500"}`（字符串 code），空串回 400
// 参数错误（2026-11 对照实测）。所以照发一个足够大的值，同时**不做任何截断**：
// 服务端推多少收多少，滤重交给调用方（截断过早曾经把候选扔掉 83%，
// 是“心动续杯必然失败”事故的根因之一）。
func (n *Netease) Hotmode(seed int64, playlistID string) ([]Track, error) {
	sid := strconv.FormatInt(seed, 10)
	data := map[string]string{
		"songId":       sid,
		"type":         "fromPlayOne",
		"playlistId":   playlistID,
		"startMusicId": sid,
		"count":        "200",
	}
	code, body, _ := musicutil.CreateRequest("POST",
		"http://music.163.com/weapi/playmode/intelligence/list", data,
		&musicutil.Options{Crypto: "weapi"})
	if int(code) != 200 {
		return nil, fmt.Errorf("心动模式请求失败 code=%v body=%s", code, util.Snippet(body))
	}
	return parseHotmode(body)
}

// parseHotmode 拆 {code,data:[{songInfo:{...}}]}，去重、丢掉无版权的（st<0）。
func parseHotmode(body []byte) ([]Track, error) {
	var r struct {
		Code flexInt `json:"code"` // 可能是数字也可能是字符串（失败态回 "500"）
		Data []struct {
			SongInfo hotSong `json:"songInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("心动模式解析失败：%v body=%s", err, util.Snippet(body))
	}
	if int(r.Code) != 200 && int(r.Code) != 0 {
		// 成功响应里 success 是 false、code 才是 200，所以只能看 code
		return nil, fmt.Errorf("心动模式失败 code=%d body=%s", int(r.Code), util.Snippet(body))
	}
	out := make([]Track, 0, len(r.Data))
	seen := make(map[int64]bool, len(r.Data))
	for _, d := range r.Data {
		s := d.SongInfo
		if s.ID == 0 || seen[s.ID] || s.Privilege.St < 0 {
			continue
		}
		seen[s.ID] = true
		out = append(out, toTracks([]apiSong{s.apiSong})[0])
	}
	return out, nil
}

// LikedPlaylistID 返回「我喜欢的音乐」歌单 id（specialType=5）。
// 心动模式的 playlistId 参数用它；对同一账号稳定，调用方自行缓存。
func (n *Netease) LikedPlaylistID(uid int64) (string, error) {
	code, body, _ := musicutil.CreateRequest("POST", "http://music.163.com/weapi/user/playlist",
		map[string]string{
			"uid":    strconv.FormatInt(uid, 10),
			"limit":  "1000",
			"offset": "0",
		}, &musicutil.Options{Crypto: "weapi"})
	if int(code) != 200 {
		return "", fmt.Errorf("歌单列表请求失败 code=%v body=%s", code, util.Snippet(body))
	}
	var r struct {
		Code     int `json:"code"`
		Playlist []struct {
			ID          int64 `json:"id"`
			SpecialType int   `json:"specialType"`
		} `json:"playlist"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("歌单列表解析失败：%v body=%s", err, util.Snippet(body))
	}
	for _, p := range r.Playlist {
		if p.SpecialType == 5 {
			return strconv.FormatInt(p.ID, 10), nil
		}
	}
	return "", fmt.Errorf("没找到「我喜欢的音乐」歌单（specialType=5）")
}

// Init 把持久化 jar 装进库的全局变量（库的 util 只认全局 jar）。
func Init(jar *auth.Jar) *Netease {
	musicutil.SetGlobalCookieJar(jar)
	musicutil.HTTPClientTimeout = 15 * time.Second
	return &Netease{}
}
