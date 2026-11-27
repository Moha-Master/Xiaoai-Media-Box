package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleConfig = `{
  "listen": "0.0.0.0:8088",
  "public_base": "http://192.168.1.10:8088",
  "cookie_file": "cookie.json",
  "quality": "exhigh",
  "window": 20,
  "loop": "list",
  "stream_mode": "redirect",
  "queue_persist": true,
  "xiaomi": {"user": "u1", "password": "secret", "token_file": ".mina_token.json"},
  "speakers": [
    {"id": "living", "name": "客厅", "control": "ssh", "ssh_host": "root@192.168.1.20"}
  ]
}`

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// 保存再读回来必须等价：相对路径仍是相对、旧 player 块不再出现、权限 0600。
func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, sampleConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.QueuePersist {
		t.Fatal("queue_persist 缺省应为 true")
	}
	cfg.Window = 33
	cfg.Speakers[0].Name = "客厅音箱"
	backup, err := Save(cfg)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if backup == "" {
		t.Fatal("覆盖已有配置时应产生备份")
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("配置文件权限应为 0600，得到 %v (%v)", st.Mode().Perm(), err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 认证路径落盘时必须是相对配置文件目录的写法，换机器/换目录才不会全体失效
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("落盘的不是合法 JSON：%v", err)
	}
	if got := onDisk["cookie_file"]; got != "cookie.json" {
		t.Fatalf("cookie_file 应写成相对路径，得到 %v", got)
	}
	if _, has := onDisk["player"]; has {
		t.Fatal("升级完成后不应再写 player 块")
	}

	back, err := Load(path)
	if err != nil {
		t.Fatalf("重新 Load: %v", err)
	}
	if back.Window != 33 {
		t.Fatalf("window 没留住：%d", back.Window)
	}
	if back.Speakers[0].Name != "客厅音箱" {
		t.Fatalf("speaker name 没留住：%q", back.Speakers[0].Name)
	}
	if !filepath.IsAbs(back.CookieFile) {
		t.Fatalf("Load 后认证路径应是绝对路径，得到 %q", back.CookieFile)
	}
	if back.Xiaomi.Password != "secret" {
		t.Fatalf("凭据不该被改动：%q", back.Xiaomi.Password)
	}
}

// 保存前必须校验，校验失败时磁盘上的文件不能被动过。
func TestSaveRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, sampleConfig)
	before, _ := os.ReadFile(path)

	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"重复 id", func(c *Config) {
			c.Speakers = append(c.Speakers, SpeakerConfig{ID: "living", Name: "二号", Control: "noop"})
		}, "重复"},
		{"未知 control", func(c *Config) { c.Speakers[0].Control = "magic" }, "control"},
		{"ssh 缺 host", func(c *Config) { c.Speakers[0].SSHHost = "" }, "ssh_host"},
		{"没有音箱", func(c *Config) { c.Speakers = nil }, "至少要有音箱"},
		{"坏 listen", func(c *Config) { c.Listen = "nope" }, "listen"},
		{"坏 stream_mode", func(c *Config) { c.StreamMode = "carrier-pigeon" }, "stream_mode"},
		{"id 带斜杠", func(c *Config) { c.Speakers[0].ID = "a/b" }, "斜杠"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			tc.mut(&cfg)
			if _, err := Save(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误包含 %q，得到 %v", tc.want, err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("校验失败时不应改动配置文件")
			}
		})
	}
}

// 备份最多留 BackupKeep 份，并且最新的排在最前。
func TestBackupsPrunedAndSorted(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, sampleConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < BackupKeep+4; i++ {
		cfg.Window = 21 + i
		if _, err := Save(cfg); err != nil {
			t.Fatalf("第 %d 次保存：%v", i, err)
		}
	}
	list, err := ListBackups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != BackupKeep {
		t.Fatalf("备份应保留 %d 份，实际 %d", BackupKeep, len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].ModifiedAt > list[i-1].ModifiedAt {
			t.Fatalf("备份应新到旧排序：%v", list)
		}
	}
}

func TestRestoreBackup(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, sampleConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Window = 40
	if _, err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	// 名字穿越、前缀不符都拒绝
	for _, bad := range []string{"", "../config.json", ".hidden", "other-20260101-000000.json"} {
		if _, err := RestoreBackup(cfg, bad); err == nil {
			t.Fatalf("备份名 %q 应被拒绝", bad)
		}
	}

	// 再改一次，这次保存前的备份正是 window=40 那一版
	cfg.Window = 50
	backup, err := Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(backup)
	if _, err := RestoreBackup(cfg, name); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Window != 40 {
		t.Fatalf("回滚后 window 应为 40，得到 %d", back.Window)
	}
}

func TestRedactedAndMergeSecrets(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeConfig(t, dir, sampleConfig))
	if err != nil {
		t.Fatal(err)
	}
	red, secrets := cfg.Redacted()
	if red.Xiaomi.Password != "" {
		t.Fatal("脱敏后不该带明文密码")
	}
	if len(secrets) != 1 || secrets[0] != "xiaomi.password" {
		t.Fatalf("secrets_set 不对：%v", secrets)
	}
	// 网页留空 = 没改，回填磁盘现值
	in := red
	MergeSecrets(&in, cfg)
	if in.Xiaomi.Password != "secret" {
		t.Fatalf("留空应回填原值，得到 %q", in.Xiaomi.Password)
	}
}

func TestChangedKeysAndSpeakerDiff(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeConfig(t, dir, sampleConfig))
	if err != nil {
		t.Fatal(err)
	}
	same := cfg
	if keys := ChangedKeys(cfg, same); len(keys) != 0 {
		t.Fatalf("相同配置不该报改动：%v", keys)
	}
	mod := cfg
	mod.Window = 7
	mod.Speakers = append(mod.Speakers, SpeakerConfig{ID: "bedroom", Name: "卧室", Control: "noop"})
	keys := ChangedKeys(cfg, mod)
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("应报 window 与 speakers 两项：%v", keys)
	}
	added, removed, modified := SpeakerDiff(cfg.Speakers, mod.Speakers)
	if len(added) != 1 || len(removed) != 0 || len(modified) != 0 {
		t.Fatalf("diff 不对：+%v -%v ~%v", added, removed, modified)
	}
}

// 网页提交走 Marshal→Unmarshal 时，BaseDir/ConfigPath 不该被抹掉或被序列化出去。
func TestInternalFieldsNotSerialized(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, sampleConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ConfigPath") || strings.Contains(string(raw), `"base_dir"`) {
		t.Fatalf("内部字段不该被序列化：%s", raw)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ConfigPath != "" || back.BaseDir != "" {
		t.Fatal("反序列化不该凭空造出 ConfigPath/BaseDir")
	}
}
