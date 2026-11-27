package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// key_file 必须跟着 token_file 走：给绝对路径的 token_file、不给 key_file 时，
// 密钥不能落到进程 cwd（曾经因为 Default 预置了相对路径而踩过）。
func TestKeyFileDerivedFromTokenFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.json")
	tokenPath := filepath.Join(dir, "auth.json")
	cfgObj := map[string]any{
		"cookie_file": "c.json",
		"speakers": []map[string]any{
			{"id": "a", "name": "A", "control": "ssh", "ssh_host": "root@h"},
		},
		"xiaomi": map[string]any{"user": "1", "device_id": "D", "token_file": tokenPath},
	}
	body, err := json.Marshal(cfgObj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := tokenPath + ".key"; cfg.Xiaomi.KeyFile != want {
		t.Fatalf("key_file=%q，期望 %q", cfg.Xiaomi.KeyFile, want)
	}
}

// 相对路径的 token_file 必须相对**配置文件所在目录**解析，而不是进程 cwd。
func TestRelativeTokenFileResolvedAgainstConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.json")
	cfgObj := map[string]any{
		"speakers": []map[string]any{
			{"id": "a", "name": "A", "control": "ssh", "ssh_host": "root@h"},
		},
		"xiaomi": map[string]any{"user": "1", "device_id": "D", "token_file": ".mina_token.json"},
	}
	body, _ := json.Marshal(cfgObj)
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, ".mina_token.json"); cfg.Xiaomi.TokenFile != want {
		t.Fatalf("token_file=%q，期望 %q", cfg.Xiaomi.TokenFile, want)
	}
	if want := filepath.Join(dir, ".mina_token.json.key"); cfg.Xiaomi.KeyFile != want {
		t.Fatalf("key_file=%q，期望 %q", cfg.Xiaomi.KeyFile, want)
	}
}

func TestStatusModeNormalizationAndValidation(t *testing.T) {
	// 默认状态模式推导
	c1 := Config{
		Speakers: []SpeakerConfig{
			{ID: "spk1", Name: "云端", Control: "cloud"},
			{ID: "spk2", Name: "SSH", Control: "ssh", SSHHost: "root@192.168.1.20"},
			{ID: "spk3", Name: "无状态", Control: "ssh", SSHHost: "root@192.168.1.20", StatusPoll: -1},
			{ID: "spk4", Name: "推送", Control: "ssh", SSHHost: "root@192.168.1.20", StatusMode: "push"},
		},
	}
	if err := Normalize(&c1, ""); err != nil {
		t.Fatal(err)
	}
	if err := c1.Validate(); err != nil {
		t.Fatal(err)
	}
	if c1.Speakers[0].StatusMode != "cloud" {
		t.Fatalf("spk1 默认应为 cloud，实际 %q", c1.Speakers[0].StatusMode)
	}
	if c1.Speakers[1].StatusMode != "ssh" {
		t.Fatalf("spk2 默认应为 ssh，实际 %q", c1.Speakers[1].StatusMode)
	}
	if c1.Speakers[2].StatusMode != "none" {
		t.Fatalf("spk3 应为 none，实际 %q", c1.Speakers[2].StatusMode)
	}
	if c1.Speakers[3].StatusMode != "push" {
		t.Fatalf("spk4 应为 push，实际 %q", c1.Speakers[3].StatusMode)
	}
	if c1.Speakers[3].StatusPoll != -1 {
		t.Fatalf("push 模式下 status_poll 应归一成 -1，实际 %d", c1.Speakers[3].StatusPoll)
	}

	// 非法 status_mode
	c2 := Config{
		Speakers: []SpeakerConfig{
			{ID: "spk1", Name: "测试", Control: "ssh", SSHHost: "root@h", StatusMode: "invalid_mode"},
		},
	}
	_ = Normalize(&c2, "")
	if err := c2.Validate(); err == nil {
		t.Fatal("非法的 status_mode 应校验失败")
	}
}
