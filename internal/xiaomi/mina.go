package xiaomi

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
)

// MiNA 云端 transport：不依赖 SSH / 不修改固件，走小米官方的 remote/ubus 代理。
// 协议原型与验证见 work_tmp/mina.py 和 docs/MEDIA-ARCH.md。

const (
	minaAPI     = "https://api2.mina.mi.com"
	minaAccount = "https://account.xiaomi.com"
	uaLogin     = "APP/com.xiaomi.mihome APPV/11.3.203 iosPassportSDK/4.2.50 iOS/26.3.1 MK/aVBob25lMTcsMg== DEVT/aVBob25l DEVS/aU9T BRA/QXBwbGU= L/zh_CN"
	uaMina      = "MiHome/6.0.103 (com.xiaomi.mihome; build:6.0.103.1; iOS 14.4.0) Alamofire/6.0.103 MICO/iOSApp/appStore/6.0.103"
)

type Client struct {
	mu           sync.Mutex
	userID       string
	loginDevID   string
	stateFile    string
	keyFile      string
	serviceToken string
	passToken    string
	noReload     bool // invalidateToken 置位：下次必须真的续期，不许从磁盘把死 token 读回来
	http         *http.Client
}

func NewClient(x config.XiaomiConfig) *Client {
	keyPath := x.KeyFile
	if keyPath == "" {
		keyPath = x.TokenFile + ".key"
	}
	return &Client{
		userID: x.User, loginDevID: x.DeviceID,
		stateFile: x.TokenFile, keyFile: keyPath,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

// ensureToken 只在内存里没有 serviceToken 时才动作。
//
// 顺序有讲究：**认证文件是权威**（config 只当首次种子），且 invalidateToken 之后
// 绝不允许再从文件回读——老代码正是栽在这里：清了内存又把同一个死 token 读回来，
// 于是"重登一次"其实一次都没登，只留下一句误导人的报错。
func (c *Client) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.serviceToken != "" {
		return nil
	}
	if !c.noReload {
		st, ok, err := loadXiaomiState(c.stateFile, c.keyFile)
		if err != nil {
			return err
		}
		if ok {
			if c.userID == "" {
				c.userID = st.UserID
			}
			if c.loginDevID == "" {
				c.loginDevID = st.DeviceID
			}
			c.passToken = st.PassToken
			if st.ServiceToken != "" {
				c.serviceToken = st.ServiceToken
				return nil
			}
		}
	}
	if c.passToken == "" {
		return fmt.Errorf("小米账号未登录：请先运行 xiaoai-mb -login xiaomi")
	}
	return c.renewLocked(ctx)
}

func (c *Client) invalidateToken() {
	c.mu.Lock()
	c.serviceToken = ""
	c.noReload = true
	c.mu.Unlock()
}

// fetchServiceToken 用 passToken 换 serviceToken（不需要密码、不需要验证码）。
//
// 这是**唯一被实机反复验证过的换 token 路径**，所以：
//   - 运行时续期用它；
//   - 扫码/密码登录拿到 passToken 之后，也用它（不要依赖 loginUrl 返回的 location：
//     小米对 micoapi 的 `api2.mina.mi.com/sts` 经常直接 401）。
//
// 返回 (serviceToken, 轮换后的 passToken, userId)。
func fetchServiceToken(ctx context.Context, userID, deviceID, passToken string) (string, string, string, error) {
	if passToken == "" {
		return "", "", "", fmt.Errorf("没有 passToken，无法换 serviceToken")
	}
	cookie := "sdkVersion=3.9; deviceId=" + deviceID
	if userID != "" {
		cookie += "; userId=" + userID
	}
	cookie += "; passToken=" + passToken

	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		minaAccount+"/pass/serviceLogin?sid="+micoapiSID+"&_json=true", nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", uaLogin)
	req.Header.Set("Cookie", cookie)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	auth, err := parseXiaomiJSON(body)
	if err != nil {
		return "", "", "", err
	}
	if codeOf(auth) != 0 {
		return "", "", "", fmt.Errorf("passToken 已失效（code=%v）：请运行 xiaoai-mb -login xiaomi", auth["code"])
	}
	nonce, ssecurity, location := strOf(auth["nonce"]), strOf(auth["ssecurity"]), strOf(auth["location"])
	if nonce == "" || ssecurity == "" || location == "" {
		return "", "", "", fmt.Errorf("serviceLogin 响应缺少 nonce/ssecurity/location（code=%v keys=%v）",
			auth["code"], keysOf(auth))
	}
	sum := sha1.Sum([]byte("nonce=" + nonce + "&" + ssecurity))
	sign := base64.StdEncoding.EncodeToString(sum[:])
	req, err = http.NewRequestWithContext(ctx, http.MethodGet,
		location+"&clientSign="+url.QueryEscape(sign), nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", uaLogin)
	req.Header.Set("Cookie", cookie)
	hr, err := client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(hr.Body, 1<<20))
	hr.Body.Close()

	token := ""
	for _, ck := range hr.Cookies() {
		if ck.Name == "serviceToken" {
			token = ck.Value
		}
	}
	if token == "" {
		for _, v := range hr.Header.Values("Set-Cookie") {
			for _, part := range strings.Split(v, ";") {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "serviceToken=") {
					token = strings.TrimPrefix(part, "serviceToken=")
				}
			}
		}
	}
	if token == "" {
		return "", "", "", fmt.Errorf("换 token 没有返回 serviceToken（HTTP %d）", hr.StatusCode)
	}
	return token, strOf(auth["passToken"]), strOf(auth["userId"]), nil
}

// renewLocked 用 passToken 静默续期。
//
// 失败就原样返回错误——不重试、不回退密码登录，避免把账号撞进小米风控。
func (c *Client) renewLocked(ctx context.Context) error {
	token, passToken, userID, err := fetchServiceToken(ctx, c.userID, c.loginDevID, c.passToken)
	if err != nil {
		return err
	}
	c.serviceToken = token
	if passToken != "" {
		c.passToken = passToken // passToken 会轮换，必须接住
	}
	if userID != "" {
		c.userID = userID
	}
	c.noReload = false
	// 每次成功都落盘：轮换后的 passToken 丢了就得重新登录。
	if err := saveXiaomiState(c.stateFile, c.keyFile, xiaomiState{
		DeviceID: c.loginDevID, UserID: c.userID,
		PassToken: c.passToken, ServiceToken: c.serviceToken,
	}); err != nil {
		log.Printf("保存小米认证文件失败：%v", err)
	}
	log.Printf("小米 token 续期成功")
	return nil
}

func parseXiaomiJSON(raw []byte) (map[string]any, error) {
	s := strings.TrimSpace(string(raw))
	s = strings.TrimPrefix(s, "&&&START&&&")
	if s == "" {
		return map[string]any{}, nil
	}
	// 必须 UseNumber：nonce 是 18 位整数，默认 float64 会丢精度，
	// 拿它算 clientSign 会直接失败（表现为"续期响应缺少 nonce/ssecurity/location"）。
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("小米响应不是 JSON：%s", util.Snippet([]byte(s)))
	}
	return m, nil
}

func codeOf(m map[string]any) int { return util.AsInt(m["code"]) }

func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return util.AsString(v)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randString(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(b)
}

// ubusCall 走 https://api2.mina.mi.com/remote/ubus 通用代理。
// 返回 data.info（通常是一个 JSON 字符串）。
func (c *Client) ubusCall(ctx context.Context, deviceID, path, method string, msg any) (string, error) {
	if err := c.ensureToken(ctx); err != nil {
		return "", err
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"deviceId":  {deviceID},
		"path":      {path},
		"method":    {method},
		"message":   {string(payload)},
		"requestId": {"app_ios_" + randString(30)},
	}

	// 注意循环结构：ensureToken 必须放在每轮开头，不能"空了就 continue 等下一轮"——
	// 那样会白吃一个重试名额，续期成功后反而没有机会发第二次请求
	// （症状：日志里"续期成功"紧接着"鉴权失败"，看起来像 token 无效）。
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.ensureToken(ctx); err != nil {
			return "", err
		}
		c.mu.Lock()
		token, uid := c.serviceToken, c.userID
		c.mu.Unlock()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			minaAPI+"/remote/ubus", strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", uaMina)
		req.Header.Set("Cookie", fmt.Sprintf("userId=%s; serviceToken=%s", uid, token))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := c.http.Do(req)
		if err != nil {
			return "", err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			c.invalidateToken()
			continue
		}

		var top struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Code int    `json:"code"`
				Info string `json:"info"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &top); err != nil {
			return "", fmt.Errorf("MiNA 响应解析失败：%w: %s", err, util.Snippet(body))
		}
		if top.Code == 401 || top.Data.Code == 401 ||
			isAuthMessage(top.Message) || isAuthMessage(top.Data.Info) {
			c.invalidateToken()
			continue
		}
		if top.Code != 0 || top.Data.Code != 0 {
			return "", fmt.Errorf("MiNA %s.%s 失败：code=%d data.code=%d msg=%s",
				path, method, top.Code, top.Data.Code, top.Message)
		}
		return top.Data.Info, nil
	}
	// 走到这里说明：已经用 passToken 续过期，还是鉴权失败。
	// 按"登录失败直接报错"的原则，不再重试——反复撞小米登录接口只会把账号撞进风控。
	return "", fmt.Errorf("MiNA %s.%s 鉴权失败：serviceToken 无效且续期未成功，请运行 xiaoai-mb -login xiaomi", path, method)
}

// isAuthMessage 判断小米返回的错误文本是不是"凭证失效"类。
//
// 除了 HTTP 401，小米还有相当一部分失效是 200 + code!=0 + 带这些字样；
// 只认 401 的话会当成普通业务错误返回，永远不会触发续期。
func isAuthMessage(msg string) bool {
	m := strings.ToLower(msg)
	for _, kw := range []string{"auth err", "invalid signature", "servicetoken_expired",
		"servicetoken expired", "token expired", "unauthorized"} {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

type minaDevice struct {
	DeviceID string `json:"deviceID"`
	Name     string `json:"name"`
	Hardware string `json:"hardware"`
	Presence string `json:"presence"`
	MiotDID  string `json:"miotDID"`
}

// Device 描述小米账号名下的一台音箱设备。
type Device struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Hardware string `json:"hardware"`
	Presence string `json:"presence"`
	MiotDID  string `json:"miot_did"`
}

// DeviceList 返回当前账号下所有小爱音箱（按在线状态排序或官方原序）。
func (c *Client) DeviceList(ctx context.Context) ([]Device, error) {
	raw, err := c.deviceList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Device, len(raw))
	for i, d := range raw {
		out[i] = Device{
			DeviceID: d.DeviceID,
			Name:     d.Name,
			Hardware: d.Hardware,
			Presence: d.Presence,
			MiotDID:  d.MiotDID,
		}
	}
	return out, nil
}

func (c *Client) deviceList(ctx context.Context) ([]minaDevice, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.ensureToken(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		token, uid := c.serviceToken, c.userID
		c.mu.Unlock()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			minaAPI+"/admin/v2/device_list?master=0", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", uaMina)
		req.Header.Set("Cookie", fmt.Sprintf("userId=%s; serviceToken=%s", uid, token))
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			c.invalidateToken()
			continue
		}
		var r struct {
			Code    int          `json:"code"`
			Message string       `json:"message"`
			Data    []minaDevice `json:"data"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("device_list 解析失败：%w: %s", err, util.Snippet(body))
		}
		if r.Code == 401 || isAuthMessage(r.Message) {
			c.invalidateToken()
			continue
		}
		if r.Code != 0 {
			return nil, fmt.Errorf("device_list 失败 code=%d %s", r.Code, r.Message)
		}
		return r.Data, nil
	}
	return nil, fmt.Errorf("device_list 鉴权失败，请运行 xiaoai-mb -login xiaomi")
}

// ---------- player.Player / player.StatusProbe / player.WindowPlayer ----------

type minaPlayer struct {
	client   *Client
	deviceID string
	media    string
	playType int
}

func NewPlayer(sp config.SpeakerConfig, x config.XiaomiConfig) (player.Player, error) {
	c := NewClient(x)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}
	devID := sp.DeviceID
	if devID == "" {
		list, err := c.deviceList(ctx)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("小米账号下没有音箱")
		}
		var online []minaDevice
		for _, d := range list {
			if d.Presence == "online" {
				online = append(online, d)
			}
		}
		switch {
		case len(online) == 1:
			devID = online[0].DeviceID
			log.Printf("云端音箱自动选择：%s（%s）", online[0].Name, devID)
		case len(list) == 1:
			devID = list[0].DeviceID
		default:
			var sb strings.Builder
			for _, d := range list {
				fmt.Fprintf(&sb, "\n  %s  %-8s %s", d.DeviceID, d.Presence, d.Name)
			}
			return nil, fmt.Errorf("账号下有 %d 台音箱，请在 speakers[].device_id 指定：%s", len(list), sb.String())
		}
	}
	return &minaPlayer{client: c, deviceID: devID, media: sp.Media, playType: sp.PlayType}, nil
}

func (p *minaPlayer) Describe() string {
	return fmt.Sprintf("mina(cloud) → %s（media=%s）", p.deviceID, p.media)
}

func (p *minaPlayer) Play(ctx context.Context, url string, t netease.Track) error {
	_, err := p.client.ubusCall(ctx, p.deviceID, "mediaplayer", "player_play_url", map[string]any{
		"url": url, "type": p.playType, "media": p.media,
	})
	return err
}

func (p *minaPlayer) PlayWindow(ctx context.Context, items []player.WindowTrack, start int) error {
	_, err := p.client.ubusCall(ctx, p.deviceID, "mediaplayer", "player_play_music", map[string]any{
		"music": player.WindowPayload(items), "media": p.media,
	})
	return err
}

// SetLoop 见 ubusSSHPlayer.SetLoop：payload 列表默认播完即停，必须显式设循环。
func (p *minaPlayer) SetLoop(ctx context.Context) error {
	_, err := p.client.ubusCall(ctx, p.deviceID, "mediaplayer", "player_set_loop", map[string]any{
		"type": 1, "media": p.media,
	})
	return err
}

func (p *minaPlayer) operation(ctx context.Context, action string) error {
	_, err := p.client.ubusCall(ctx, p.deviceID, "mediaplayer", "player_play_operation", map[string]any{
		"action": action, "media": p.media,
	})
	return err
}

func (p *minaPlayer) Pause(ctx context.Context) error  { return p.operation(ctx, "pause") }
func (p *minaPlayer) Resume(ctx context.Context) error { return p.operation(ctx, "play") }
func (p *minaPlayer) Next(ctx context.Context) error   { return p.operation(ctx, "next") }
func (p *minaPlayer) Prev(ctx context.Context) error   { return p.operation(ctx, "prev") }
func (p *minaPlayer) Stop(ctx context.Context) error   { return p.operation(ctx, "stop") }

func (p *minaPlayer) Seek(ctx context.Context, positionMs int) error {
	info, err := p.client.ubusCall(ctx, p.deviceID, "mediaplayer", "player_set_positon", map[string]any{
		"position": positionMs, "media": p.media,
	})
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(info); s != "" {
		var r struct {
			Code int `json:"code"`
		}
		if json.Unmarshal([]byte(s), &r) == nil && r.Code != 0 {
			return fmt.Errorf("mina player_set_positon code=%d", r.Code)
		}
	}
	return nil
}

func (p *minaPlayer) Volume(ctx context.Context, value int) error {
	_, err := p.client.ubusCall(ctx, p.deviceID, "mediaplayer", "player_set_volume", map[string]any{"volume": value})
	return err
}

func (p *minaPlayer) Status(ctx context.Context) (player.DeviceStatus, error) {
	info, err := p.client.ubusCall(ctx, p.deviceID, "mediaplayer", "player_get_play_status", map[string]any{"media": p.media})
	if err != nil {
		return player.DeviceStatus{}, err
	}
	if strings.TrimSpace(info) == "" {
		return player.DeviceStatus{}, fmt.Errorf("空状态（设备待机？）")
	}
	return player.ParseStatus([]byte(info))
}
