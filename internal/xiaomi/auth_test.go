package xiaomi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/auth"
)

func TestSealedStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "auth.json")
	keyPath := filepath.Join(dir, "auth.key")

	want := xiaomiState{
		DeviceID: "TESTDEVICE01234567", UserID: "10000000000",
		PassToken: "V1:secret-pass", ServiceToken: "secret-service",
	}
	if err := saveXiaomiState(statePath, keyPath, want); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	// 文件里不能出现明文 token
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatal("认证文件里还有明文凭据，加密没生效")
	}
	if !strings.HasPrefix(string(raw), auth.Magic) {
		t.Fatalf("缺少 magic 头：%q", string(raw[:12]))
	}

	got, ok, err := loadXiaomiState(statePath, keyPath)
	if err != nil || !ok {
		t.Fatalf("读取失败：ok=%v err=%v", ok, err)
	}
	if got.PassToken != want.PassToken || got.ServiceToken != want.ServiceToken || got.UserID != want.UserID {
		t.Fatalf("往返不一致：%+v", got)
	}
}

func TestSealedStateTamperDetected(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "auth.json")
	keyPath := filepath.Join(dir, "auth.key")
	if err := saveXiaomiState(statePath, keyPath, xiaomiState{PassToken: "x"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(statePath)
	raw[len(raw)-1] ^= 0xFF // 改掉最后一个字节
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadXiaomiState(statePath, keyPath); err == nil {
		t.Fatal("密文被篡改却没有报错（GCM 认证失效）")
	}
}

func TestLegacyPlaintextStillReadable(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "auth.json")
	keyPath := filepath.Join(dir, "auth.key")
	legacy := `{"userId":"9000000001","serviceToken":"old-svc","passToken":"old-pass"}`
	if err := os.WriteFile(statePath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	st, ok, err := loadXiaomiState(statePath, keyPath)
	if err != nil || !ok {
		t.Fatalf("旧格式读取失败：ok=%v err=%v", ok, err)
	}
	if st.PassToken != "old-pass" || st.ServiceToken != "old-svc" || st.UserID != "9000000001" {
		t.Fatalf("旧格式迁移失败：%+v", st)
	}
	// 再存一次应自动升级成密文
	if err := saveXiaomiState(statePath, keyPath, st); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(statePath)
	if !strings.HasPrefix(string(raw), auth.Magic) {
		t.Fatal("旧格式没有被升级成密文")
	}
}

func TestAuthMessageClassification(t *testing.T) {
	yes := []string{"auth err", "invalid signature", "SERVICETOKEN_EXPIRED", "Unauthorized"}
	for _, m := range yes {
		if !isAuthMessage(m) {
			t.Fatalf("%q 应判为鉴权失效", m)
		}
	}
	no := []string{"", "device offline", "player is busy"}
	for _, m := range no {
		if isAuthMessage(m) {
			t.Fatalf("%q 不该判为鉴权失效", m)
		}
	}
}

// 小米登录响应里的 nonce 是 18 位整数。默认 json.Unmarshal 会解成 float64 丢精度，
// 用它算 clientSign 必然失败——这个坑曾经表现为"续期响应缺少 nonce/ssecurity/location"。
func TestParseXiaomiJSONKeepsBigInts(t *testing.T) {
	raw := []byte(`&&&START&&&{"code":0,"nonce":123456789012345678,"userId":9000000001,"ssecurity":"abc"}`)
	m, err := parseXiaomiJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := strOf(m["nonce"]); got != "123456789012345678" {
		t.Fatalf("nonce 精度丢了：%q", got)
	}
	if got := strOf(m["userId"]); got != "9000000001" {
		t.Fatalf("userId 解错了：%q", got)
	}
}

// 实机返回的 notificationUrl 是相对路径，必须补全并把 path 换成 /identity/list，
// query（sid/context/_locale）要原样保留。
func TestIdentityListURL(t *testing.T) {
	got, err := identityListURL("/fe/service/identity/authStart?sid=micoapi&context=abc123&_locale=zh_CN")
	if err != nil {
		t.Fatalf("相对路径应能规范化：%v", err)
	}
	want := "https://account.xiaomi.com/identity/list?sid=micoapi&context=abc123&_locale=zh_CN"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	abs, err := identityListURL("https://account.xiaomi.com/fe/service/identity/authStart?sid=micoapi")
	if err != nil || abs != "https://account.xiaomi.com/identity/list?sid=micoapi" {
		t.Fatalf("绝对路径处理错误：%q %v", abs, err)
	}

	if _, err := identityListURL("/evil/authStart?x=1"); err == nil {
		t.Fatal("非 identity/authStart 的地址应该报错")
	}
	if _, err := identityListURL("https://evil.example.com/fe/service/identity/authStart"); err == nil {
		t.Fatal("非 account.xiaomi.com 的地址应该报错")
	}
}
