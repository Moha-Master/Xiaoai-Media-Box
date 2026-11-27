package xiaomi

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/auth"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/qr"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
	"golang.org/x/term"
)

// 小米账号的认证状态。
//
// 设计原则（对齐 hass-xiaomi-miot / miservice / go2rtc 的成熟做法）：
//   - passToken 是**长期凭据**（实测 1~1.5 个月，每次登录会轮换）→ 必须落盘、文件为权威；
//   - serviceToken 是**会话凭据**（短命，绑定 cookie 会话）→ 丢了随时用 passToken 换；
//   - 密码**只用于交互式登录**，运行时永不使用（避免把账号撞进风控）。
type State struct {
	DeviceID     string `json:"device_id"`
	UserID       string `json:"user_id"`
	PassToken    string `json:"pass_token"`
	ServiceToken string `json:"service_token,omitempty"`
	UpdatedAt    int64  `json:"updated_at"`
}

type xiaomiState = State

// LoadState 供管理面与健康检查读取当前已落盘的小米认证凭据。
func LoadState(statePath, keyPath string) (State, bool, error) {
	return loadXiaomiState(statePath, keyPath)
}

// ClearState 删除已保存的小米凭据（登出用）。
func ClearState(statePath, keyPath string) error {
	_ = os.Remove(statePath)
	_ = os.Remove(keyPath)
	return nil
}

// 旧版 .mina_token.json 的字段名（camelCase），用于自动迁移。
type xiaomiStateLegacy struct {
	UserID       any    `json:"userId"`
	ServiceToken string `json:"serviceToken"`
	PassToken    string `json:"passToken"`
}

const (
	micoapiSID  = "micoapi"
	stsCallback = "https://sts.api.io.mi.com/sts"
)

func loadXiaomiState(statePath, keyPath string) (xiaomiState, bool, error) {
	var st xiaomiState
	ok, err := auth.LoadSealedJSON(statePath, keyPath, &st)
	if err != nil || !ok {
		return st, ok, err
	}
	if st.PassToken == "" && st.ServiceToken == "" {
		// 旧格式（明文或加密都兼容）
		var old xiaomiStateLegacy
		if ok2, err2 := auth.LoadSealedJSON(statePath, keyPath, &old); ok2 && err2 == nil {
			st.UserID = util.AsString(old.UserID)
			st.ServiceToken = old.ServiceToken
			st.PassToken = old.PassToken
		}
	}
	return st, true, nil
}

func saveXiaomiState(statePath, keyPath string, st xiaomiState) error {
	st.UpdatedAt = time.Now().Unix()
	return auth.SaveSealedJSON(statePath, keyPath, st)
}

// ---------- 交互式登录：会话与工具 ----------

type authSession struct {
	client *http.Client
}

func newAuthSession() *authSession {
	jar, _ := cookiejar.New(nil)
	return &authSession{client: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (s *authSession) setCookie(rawURL, name, value string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	s.client.Jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Path: "/"}})
}

// setCookieAll 把 cookie 设到所有会用到的域上（host-only，逐域设置）。
// 只设 https://xiaomi.com 是没用的：jar 里那是 host-only cookie，
// 不会发给 account.xiaomi.com / api2.mina.mi.com，而 STS 恰恰要看 deviceId。
func (s *authSession) setCookieAll(name, value string) {
	for _, h := range []string{
		"https://xiaomi.com",
		"https://account.xiaomi.com",
		"https://api2.mina.mi.com",
		"https://sts.api.io.mi.com",
		"https://mina.mi.com",
	} {
		s.setCookie(h, name, value)
	}
}

func (s *authSession) do(ctx context.Context, req *http.Request, timeout time.Duration) (map[string]any, int, error) {
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", uaLogin)
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	c := *s.client
	if timeout > 0 {
		c.Timeout = timeout
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	m, perr := parseXiaomiJSON(body)
	if perr != nil {
		// 非 JSON（比如跳转页/HTML 错误页）时返回空 map，调用方看状态码
		return map[string]any{}, resp.StatusCode, nil
	}
	return m, resp.StatusCode, nil
}

func (s *authSession) get(ctx context.Context, rawURL string) (map[string]any, int, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	return s.do(ctx, req, 0)
}

func (s *authSession) getTimeout(ctx context.Context, rawURL string, d time.Duration) (map[string]any, int, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	return s.do(ctx, req, d)
}

func (s *authSession) postForm(ctx context.Context, rawURL string, form url.Values) (map[string]any, int, error) {
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	return s.do(ctx, req, 0)
}

// serviceToken 从 cookie jar 里捞：不同的跳转落点会把它挂在不同的域上。
func (s *authSession) serviceToken() string {
	for _, host := range []string{
		"https://sts.api.io.mi.com/sts",
		"https://api2.mina.mi.com",
		"https://account.xiaomi.com",
	} {
		u, err := url.Parse(host)
		if err != nil {
			continue
		}
		for _, ck := range s.client.Jar.Cookies(u) {
			if ck.Name == "serviceToken" && ck.Value != "" {
				return ck.Value
			}
		}
	}
	return ""
}

func (s *authSession) cookieValue(rawURL, name string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, ck := range s.client.Jar.Cookies(u) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

// finishLocation 沿登录跳转链走到底，从**任意一跳**的 Set-Cookie 里取 serviceToken。
//
// 为什么不用 http.Client 的自动跟随：它只把最后一跳的响应给我，而 micoapi 的 STS
// 端点经常返回 401（小米已知行为："已完成的登录被 STS 拒绝"），serviceToken 反而是
// 在前面某一跳就发下来的。手动走还能把链路打出来，排障时不用猜。
func (s *authSession) finishLocation(ctx context.Context, location string) (string, error) {
	if location == "" {
		return "", errors.New("登录响应里没有 location")
	}
	cur := location
	var hops []string
	noFollow := *s.client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	for i := 0; i < 10; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cur, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", uaLogin)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := noFollow.Do(req)
		if err != nil {
			return "", err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()

		// 这一跳下发的 cookie 全部收进 jar（后续步骤可能要用 cUserId/ick 之类）
		if cks := resp.Cookies(); len(cks) > 0 {
			s.client.Jar.SetCookies(req.URL, cks)
			for _, ck := range cks {
				if ck.Name == "serviceToken" && ck.Value != "" {
					return ck.Value, nil
				}
			}
		}

		hops = append(hops, fmt.Sprintf("%d %s", resp.StatusCode, hostPath(cur)))
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			next := resp.Header.Get("Location")
			if next == "" {
				break
			}
			nu, err := req.URL.Parse(next)
			if err != nil {
				break
			}
			cur = nu.String()
			continue
		}

		// 落地页可能是"确认手机号"之类的 /fe/ 页面，里面带 skipUrl 就再跟一次
		if u, err := url.Parse(cur); err == nil && strings.HasPrefix(u.Path, "/fe/") {
			if skip := u.Query().Get("skipUrl"); skip != "" {
				su, err := url.Parse(skip)
				if err != nil {
					break
				}
				if !su.IsAbs() {
					su = u.ResolveReference(su)
				}
				cur = su.String()
				continue
			}
		}
		break
	}

	if tok := s.serviceToken(); tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("登录跳转没有返回 serviceToken（链路：%s）", strings.Join(hops, " → "))
}

// hostPath 只保留 host+path，避免把 ticket/context 这类敏感 query 打进日志。
func hostPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "?"
	}
	return u.Host + u.Path
}

func prompt(label string) string {
	fmt.Print(label)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptSecret(label string) (string, error) {
	fmt.Print(label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func randomDeviceID() string {
	const alphabet = "0123456789ABCDEF"
	b := make([]byte, 16)
	seed := sha1.Sum([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	for i := range b {
		b[i] = alphabet[int(seed[i%len(seed)])%len(alphabet)]
	}
	return string(b)
}

func containsInt(list []any, want int) bool {
	for _, v := range list {
		if util.AsInt(v) == want {
			return true
		}
	}
	return false
}

// ---------- 方式一：扫码 / 浏览器链接授权 ----------

// 小米 passport 的二维码登录：二维码内容本身就是 loginUrl，
// 所以「终端画码」和「把链接粘到已登录的浏览器」是同一个入口。
func loginXiaomiQR(ctx context.Context, deviceID string) (xiaomiState, error) {
	s := newAuthSession()
	// STS 那边会把 deviceId 放进 d= 参数；扫码会话也得带上，否则容易 401
	s.setCookieAll("sdkVersion", "accountsdk-18.8.15")
	s.setCookieAll("deviceId", deviceID)
	q := url.Values{
		"_qrsize":      {"480"},
		"qs":           {fmt.Sprintf("?sid=%s&_json=true", micoapiSID)},
		"callback":     {stsCallback},
		"_hasLogo":     {"false"},
		"sid":          {micoapiSID},
		"serviceParam": {""},
		"_locale":      {"zh_CN"},
		"_dc":          {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	}
	d, status, err := s.get(ctx, minaAccount+"/longPolling/loginUrl?"+q.Encode())
	if err != nil {
		return xiaomiState{}, err
	}
	loginURL, lp := strOf(d["loginUrl"]), strOf(d["lp"])
	if loginURL == "" || lp == "" {
		return xiaomiState{}, fmt.Errorf("拿不到登录链接（HTTP %d, code=%v）", status, d["code"])
	}
	timeout := util.AsInt(d["timeout"])
	if timeout <= 0 {
		timeout = 300
	}

	fmt.Println("\n━━━ 扫码 / 链接授权 ━━━")
	fmt.Println("在浏览器里打开下面这个链接（手机、电脑都行）：")
	fmt.Println("  · 已经登录过小米账号 → 直接点确认授权")
	fmt.Println("  · 还没登录 → 先登录**同一个账号**，登录完也会回到这一步")
	fmt.Println()
	fmt.Println("  " + loginURL)
	fmt.Println()
	fmt.Println("或者扫下面这个二维码（内容就是上面的链接；没登录会先要求登录）：")
	fmt.Println()
	if err := qr.Print(loginURL); err != nil {
		fmt.Println("（二维码渲染失败，请用上面的链接）")
	}
	fmt.Printf("\n等待授权，最长 %d 秒…\n", timeout)

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	var info map[string]any
	for time.Now().Before(deadline) {
		d, status, err := s.getTimeout(ctx, lp, 20*time.Second)
		if err != nil {
			continue // 长轮询超时是正常的，继续等
		}
		if status != http.StatusOK {
			continue
		}
		if strOf(d["location"]) != "" {
			info = d
			break
		}
	}
	if info == nil {
		return xiaomiState{}, errors.New("等待超时，未收到授权（链接过期，请重新运行）")
	}

	st := xiaomiState{
		DeviceID:  deviceID,
		UserID:    strOf(info["userId"]),
		PassToken: strOf(info["passToken"]),
	}
	if st.PassToken == "" {
		return xiaomiState{}, errors.New("授权成功但没拿到 passToken（无法长期续期）")
	}
	// 不依赖 loginUrl 给的 location：小米对 micoapi 的 STS 端点经常直接 401。
	// 用已经验证稳定的 passToken → serviceLogin → clientSign → serviceToken。
	token, passToken, userID, err := fetchServiceToken(ctx, st.UserID, deviceID, st.PassToken)
	if err != nil {
		if tok2, err2 := s.finishLocation(ctx, strOf(info["location"])); err2 == nil {
			token = tok2
		} else {
			return st, fmt.Errorf("已拿到 passToken，但换 serviceToken 失败：%v；直连跳转也不通：%v", err, err2)
		}
	}
	if passToken != "" {
		st.PassToken = passToken
	}
	if userID != "" {
		st.UserID = userID
	}
	st.ServiceToken = token
	return st, nil
}

// ---------- 方式二：账号密码 + 短信/邮箱验证码 ----------

func loginXiaomiPassword(ctx context.Context, user, password, deviceID string) (xiaomiState, error) {
	s := newAuthSession()
	// deviceId 与 passToken 绑定：必须稳定（实测错配 → code=70016）
	s.setCookieAll("sdkVersion", "accountsdk-18.8.15")
	s.setCookieAll("deviceId", deviceID)

	// finish 统一收尾：拿到 passToken → 用实测稳定的路径换 serviceToken
	finish := func(passToken, userID, location string) (xiaomiState, error) {
		if passToken == "" {
			return xiaomiState{}, errors.New("登录成功但没拿到 passToken（无法长期续期）")
		}
		tok, newPass, newUID, err := fetchServiceToken(ctx, userID, deviceID, passToken)
		if err != nil {
			if location == "" {
				return xiaomiState{}, err
			}
			tok2, err2 := s.finishLocation(ctx, location)
			if err2 != nil {
				return xiaomiState{}, fmt.Errorf("换 serviceToken 失败：%v；直连跳转也不通：%v", err, err2)
			}
			tok = tok2
		}
		if newPass != "" {
			passToken = newPass
		}
		if newUID != "" {
			userID = newUID
		}
		return xiaomiState{DeviceID: deviceID, UserID: userID,
			PassToken: passToken, ServiceToken: tok}, nil
	}

	auth, err := s.step1(ctx)
	if err != nil {
		return xiaomiState{}, err
	}
	if pt := strOf(auth["passToken"]); pt != "" {
		// 浏览器/设备已有登录态，step1 直接给了凭据，不用密码
		return finish(pt, strOf(auth["userId"]), s.withClientSign(auth, strOf(auth["location"])))
	}

	if password == "" {
		return xiaomiState{}, errors.New("需要密码")
	}
	resp, err := s.step2(ctx, auth, user, password)
	if err != nil {
		return xiaomiState{}, err
	}

	switch {
	case strOf(resp["location"]) != "":
		return finish(strOf(resp["passToken"]), strOf(resp["userId"]),
			s.withClientSign(resp, strOf(resp["location"])))
	case strOf(resp["notificationUrl"]) != "":
		passToken, userID, err := s.verifyFlow(ctx, strOf(resp["notificationUrl"]))
		if err != nil {
			return xiaomiState{}, err
		}
		return finish(passToken, userID, "")
	case strOf(resp["captchaUrl"]) != "":
		return xiaomiState{}, fmt.Errorf("小米要求图形验证码——请改用扫码登录（xiaoai-mb -login xiaomi）")
	}
	return xiaomiState{}, fmt.Errorf("登录被拒：code=%v desc=%v", resp["code"], resp["desc"])
}

// step1：拿 qs/_sign（以及可能的 passToken / ssecurity）
func (s *authSession) step1(ctx context.Context) (map[string]any, error) {
	d, status, err := s.get(ctx, minaAccount+"/pass/serviceLogin?sid="+micoapiSID+"&_json=true")
	if err != nil {
		return nil, err
	}
	if _, ok := d["qs"]; !ok && codeOf(d) != 0 {
		return nil, fmt.Errorf("登录 step1 失败：HTTP %d code=%v", status, d["code"])
	}
	return d, nil
}

// step2：提交密码（hash = MD5 大写）
func (s *authSession) step2(ctx context.Context, auth map[string]any, user, password string) (map[string]any, error) {
	sum := md5.Sum([]byte(password))
	form := url.Values{
		"user":     {user},
		"hash":     {strings.ToUpper(hex.EncodeToString(sum[:]))},
		"callback": {strOf(auth["callback"])},
		"sid":      {micoapiSID},
		"qs":       {strOf(auth["qs"])},
		"_sign":    {strOf(auth["_sign"])},
	}
	d, _, err := s.postForm(ctx, minaAccount+"/pass/serviceLoginAuth2?_json=true", form)
	if err != nil {
		return nil, err
	}
	if strOf(d["location"]) != "" || strOf(d["notificationUrl"]) != "" || strOf(d["captchaUrl"]) != "" {
		return d, nil
	}
	switch codeOf(d) {
	case 70016, 70002, 20003:
		return nil, fmt.Errorf("账号或密码错误（code=%v）", d["code"])
	case 81003:
		return nil, errors.New("账号需要二次验证，但小米没给验证入口——请改用扫码登录")
	}
	return d, nil
}

// withClientSign：非 xiaomiio 的 sid，location 必须补 clientSign，否则拿不到 serviceToken。
func (s *authSession) withClientSign(resp map[string]any, location string) string {
	nonce, ssecurity := strOf(resp["nonce"]), strOf(resp["ssecurity"])
	if nonce == "" || ssecurity == "" {
		return location
	}
	sum := sha1.Sum([]byte("nonce=" + nonce + "&" + ssecurity))
	sign := base64.StdEncoding.EncodeToString(sum[:])
	return location + "&clientSign=" + url.QueryEscape(sign)
}

// identityListURL 把小米给的 notificationUrl 规范化成 identity 列表地址。
func identityListURL(notify string) (string, error) {
	raw := strings.TrimSpace(notify)
	if raw == "" {
		return "", errors.New("小米没有返回二次验证地址")
	}
	switch {
	case strings.HasPrefix(raw, "/"):
		raw = minaAccount + raw
	case !strings.HasPrefix(raw, "http"):
		raw = minaAccount + "/" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("二次验证地址不合法：%s", notify)
	}
	if u.Host != "account.xiaomi.com" {
		return "", fmt.Errorf("二次验证地址不在 account.xiaomi.com：%s", u.Host)
	}
	if !strings.HasSuffix(u.Path, "/identity/authStart") {
		return "", fmt.Errorf("二次验证地址不是 authStart：%s", u.Path)
	}
	u.Scheme = "https"
	u.Path = "/identity/list" // 注意是整个 path 替换，不是拼接
	return u.String(), nil
}

// verifyFlow 走完二次验证，只负责拿到 passToken（换 serviceToken 由调用方统一做）。
func (s *authSession) verifyFlow(ctx context.Context, notifyURL string) (string, string, error) {
	listURL, err := identityListURL(notifyURL)
	if err != nil {
		return "", "", err
	}
	d, status, err := s.get(ctx, listURL)
	if err != nil {
		return "", "", err
	}
	options := d["options"]
	list, _ := options.([]any)
	flag, channel := 0, ""
	if containsInt(list, 4) {
		flag, channel = 4, "Phone"
	} else if containsInt(list, 8) {
		flag, channel = 8, "Email"
	} else if util.AsInt(d["flag"]) == 4 {
		flag, channel = 4, "Phone"
	} else if util.AsInt(d["flag"]) == 8 {
		flag, channel = 8, "Email"
	}
	if flag == 0 {
		return "", "", fmt.Errorf("小米没有提供可用的验证方式（HTTP %d, code=%v desc=%v keys=%v）——建议改用扫码登录",
			status, d["code"], d["desc"], keysOf(d))
	}

	// 先在 identity 会话里选定通道，再发码（顺序不能反）
	probeURL := fmt.Sprintf("%s/identity/auth/verify%s?_flag=%d&_json=true", minaAccount, channel, flag)
	if pd, _, err := s.get(ctx, probeURL); err == nil && codeOf(pd) != 0 {
		return "", "", fmt.Errorf("验证通道 %s 不可用：%v", channel, pd["desc"])
	}
	sendURL := fmt.Sprintf("%s/identity/auth/send%sTicket?_dc=%d", minaAccount, channel, time.Now().UnixMilli())
	sd, _, err := s.postForm(ctx, sendURL, url.Values{
		"_flag": {strconv.Itoa(flag)}, "retry": {"0"}, "_json": {"true"}, "icode": {""},
	})
	if err != nil {
		return "", "", err
	}
	if codeOf(sd) != 0 {
		if strOf(sd["captchaUrl"]) != "" {
			return "", "", fmt.Errorf("小米要求图形验证码（code=%v）——这条路走不通，请改用扫码登录", sd["code"])
		}
		return "", "", fmt.Errorf("发送验证码失败：code=%v %v", sd["code"], sd["desc"])
	}
	fmt.Printf("\n验证码已发到你的%s。\n", map[string]string{"Phone": "手机", "Email": "邮箱"}[channel])
	code := prompt("请输入验证码：")
	if code == "" {
		return "", "", errors.New("没有输入验证码")
	}
	verifyURL := fmt.Sprintf("%s/identity/auth/verify%s?_dc=%d", minaAccount, channel, time.Now().UnixMilli())
	vd, _, err := s.postForm(ctx, verifyURL, url.Values{
		"_flag": {strconv.Itoa(flag)}, "ticket": {code}, "trust": {"false"}, "_json": {"true"},
	})
	if err != nil {
		return "", "", err
	}
	if codeOf(vd) != 0 {
		return "", "", fmt.Errorf("验证码被拒：code=%v %v", vd["code"], vd["desc"])
	}
	loc := strOf(vd["location"])
	if loc == "" {
		return "", "", errors.New("验证通过但没有返回跳转地址")
	}
	// 访问跳转页只是为了让身份流程落地（可能 401，忽略），关键在下一步的 step1
	_, _ = s.finishLocation(ctx, loc)
	again, err := s.step1(ctx)
	if err != nil {
		return "", "", err
	}
	passToken, userID := strOf(again["passToken"]), strOf(again["userId"])
	if passToken == "" {
		return "", "", errors.New("验证通过但没拿到 passToken，请重试或改用扫码登录")
	}
	return passToken, userID, nil
}

// ---------- 编排 ----------

func Login(ctx context.Context, cfg config.XiaomiConfig) error {
	statePath, keyPath := cfg.TokenFile, cfg.KeyFile
	if statePath == "" {
		return errors.New("配置里没有 xiaomi.token_file")
	}
	if keyPath == "" {
		keyPath = statePath + ".key"
	}

	fmt.Printf("认证文件：%s\n密钥文件：%s\n", statePath, keyPath)
	// 先确认写得进去，再做扫码/验证码——否则用户白等 5 分钟才在最后一步报 EACCES。
	if err := auth.CheckWritable(statePath, keyPath); err != nil {
		return err
	}

	deviceID := cfg.DeviceID
	if st, ok, err := loadXiaomiState(statePath, keyPath); err == nil && ok {
		if deviceID == "" {
			deviceID = st.DeviceID
		}
	}
	if deviceID == "" {
		deviceID = randomDeviceID()
		fmt.Printf("已生成登录设备 id：%s（会和 passToken 一起存起来）\n", deviceID)
	}

	fmt.Println("\n小米账号登录方式：")
	fmt.Println("  1) 扫码 / 浏览器链接授权   ← 推荐：免密码、免验证码、不触发风控")
	fmt.Println("  2) 账号密码 + 短信验证码")
	choice := prompt("请选择 [1]: ")
	if choice == "" {
		choice = "1"
	}

	var st xiaomiState
	var err error
	switch choice {
	case "2":
		user := cfg.User
		if user == "" {
			user = prompt("小米账号（手机号 / 邮箱 / userId）：")
		}
		pwd := cfg.Password
		if pwd == "" {
			if pwd, err = promptSecret("密码（输入时不回显）："); err != nil {
				return err
			}
		}
		st, err = loginXiaomiPassword(ctx, user, pwd, deviceID)
	default:
		st, err = loginXiaomiQR(ctx, deviceID)
	}
	if err != nil {
		return err
	}
	if st.DeviceID == "" {
		st.DeviceID = deviceID
	}
	if err := saveXiaomiState(statePath, keyPath, st); err != nil {
		return fmt.Errorf("保存认证文件失败：%w", err)
	}
	fmt.Printf("\n✅ 登录成功（userId=%s）\n", st.UserID)
	fmt.Printf("   认证文件：%s（加密，密钥在 %s）\n", statePath, keyPath)

	if err := verifyXiaomiToken(ctx, st.UserID, st.ServiceToken); err != nil {
		return fmt.Errorf("token 已保存，但验证失败：%w", err)
	}
	return nil
}

// verifyXiaomiToken 用刚拿到的 serviceToken 拉一次设备列表 —— 最直接的"能用吗"检查。
func verifyXiaomiToken(ctx context.Context, userID, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		minaAPI+"/admin/v2/device_list?master=0", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", uaMina)
	req.Header.Set("Cookie", fmt.Sprintf("userId=%s; serviceToken=%s", userID, token))
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("device_list HTTP %d", resp.StatusCode)
	}
	var r struct {
		Code int          `json:"code"`
		Data []minaDevice `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return err
	}
	if r.Code != 0 {
		return fmt.Errorf("device_list code=%d", r.Code)
	}
	fmt.Println("\n账号下的音箱：")
	for _, d := range r.Data {
		fmt.Printf("  %-38s %-8s %s\n", d.DeviceID, d.Presence, d.Name)
	}
	return nil
}

// CheckAuth 是排障入口：强制用 passToken 续期一次，再拉设备列表。
// 用来回答"现在这套凭据还能用吗 / 是不是已经废了"。
func CheckAuth(ctx context.Context, cfg config.XiaomiConfig) error {
	st, ok, err := loadXiaomiState(cfg.TokenFile, cfg.KeyFile)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("认证文件 %s 不存在，请先运行 xiaoai-mb -login xiaomi", cfg.TokenFile)
	}
	fmt.Printf("认证文件：%s\n", cfg.TokenFile)
	fmt.Printf("  userId      : %s\n", mask(st.UserID))
	fmt.Printf("  deviceId    : %s\n", mask(st.DeviceID))
	fmt.Printf("  passToken   : %s\n", mask(st.PassToken))
	fmt.Printf("  serviceToken: %s\n", mask(st.ServiceToken))
	fmt.Printf("  更新时间    : %s\n", time.Unix(st.UpdatedAt, 0).Format("2006-01-02 15:04:05"))

	c := NewClient(cfg)
	if st.UserID != "" {
		c.userID = st.UserID
	}
	if st.DeviceID != "" {
		c.loginDevID = st.DeviceID
	}
	c.passToken = st.PassToken
	c.noReload = true // 强制走续期，验证 passToken 本身
	if err := c.ensureToken(ctx); err != nil {
		return fmt.Errorf("passToken 已不可用（需要重新 xiaoai-mb -login xiaomi）：%w", err)
	}
	fmt.Printf("\npassToken 续期：成功\n")

	devs, err := c.deviceList(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("设备列表：\n")
	for _, d := range devs {
		fmt.Printf("  %-38s %-8s %s\n", d.DeviceID, d.Presence, d.Name)
	}
	return nil
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mask(s string) string {
	if s == "" {
		return "（空）"
	}
	if len(s) <= 8 {
		return s + "…"
	}
	return s[:6] + "…" + s[len(s)-4:] + fmt.Sprintf("（%d 字节）", len(s))
}
