package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
)

// BackupsDir 是配置文件旁边的备份目录；BackupKeep 是保留份数。
// 改坏了要能回滚，但也不想让 backups/ 长满。
const (
	BackupsDir = "backups"
	BackupKeep = 10
)

// Save 原子写配置文件，写之前先把磁盘上的现内容备份到 backups/。
// 返回备份路径（原来没有配置文件时为空串）。
//
// 落盘的是**归一化后的有效配置**（缺省值补齐、旧 player 块升级成 speakers[]），
// 但认证路径会还原成「相对配置文件目录」的写法——见 relativize。
func Save(cfg Config) (string, error) {
	path := cfg.ConfigPath
	if path == "" {
		return "", errors.New("ConfigPath 为空，不知道要写到哪里")
	}
	out := cfg
	out.Speakers = append([]SpeakerConfig(nil), cfg.Speakers...)
	if err := Normalize(&out, path); err != nil {
		return "", err
	}
	if len(out.Speakers) == 0 {
		// NormalizeSpeakers 只升级显式的旧版 player 块；这里显式拒绝，
		// 否则“删光所有音箱”会变成一份没有音箱的配置，重启后直接起不来。
		return "", errors.New("配置不合法：至少要有音箱（speakers[] 为空）")
	}
	base := filepath.Dir(path)
	relativize(&out, base)
	if err := out.Validate(); err != nil {
		return "", err
	}

	backup := ""
	if raw, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(raw)) > 0 {
		p, err := writeBackup(base, filepath.Base(path), raw)
		if err != nil {
			return "", err
		}
		backup = p
	}
	if err := util.WriteJSONFile(path, out); err != nil {
		return backup, fmt.Errorf("写配置文件失败：%w", err)
	}
	// 清理旧备份是尽力而为：失败不该让一次成功的保存变成失败
	_ = pruneBackups(filepath.Join(base, BackupsDir), filepath.Base(path))
	return backup, nil
}

// relativize 把认证路径还原成相对配置文件目录的写法。
//
// Load 会把它们绝对化（这是对的，运行时不该依赖 cwd），但原样写回去有两个后果：
// 换目录/换机器就全体失效，而且每次保存都被算成“改过”。落在配置文件目录之内的
// 才转相对，外面的保持绝对。
func relativize(cfg *Config, base string) {
	f := func(p string) string {
		if p == "" || !filepath.IsAbs(p) {
			return p
		}
		rel, err := filepath.Rel(base, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return p
		}
		return rel
	}
	cfg.Xiaomi.TokenFile = f(cfg.Xiaomi.TokenFile)
	cfg.Xiaomi.KeyFile = f(cfg.Xiaomi.KeyFile)
	cfg.CookieFile = f(cfg.CookieFile)
	cfg.CookieKeyFile = f(cfg.CookieKeyFile)
}

func writeBackup(dir, name string, raw []byte) (string, error) {
	bdir := filepath.Join(dir, BackupsDir)
	if err := os.MkdirAll(bdir, 0o700); err != nil {
		return "", fmt.Errorf("创建备份目录失败：%w", err)
	}
	stamp := time.Now().Format("20060102-150405")
	base := strings.TrimSuffix(name, filepath.Ext(name))
	ext := filepath.Ext(name)
	p := filepath.Join(bdir, base+"-"+stamp+ext)
	for i := 1; ; i++ { // 同一秒内连续保存（脚本循环）不该互相覆盖
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			break
		}
		p = filepath.Join(bdir, fmt.Sprintf("%s-%s-%d%s", base, stamp, i, ext))
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return "", fmt.Errorf("写备份失败：%w", err)
	}
	return p, nil
}

func pruneBackups(bdir, name string) error {
	files, err := backupFiles(bdir, name)
	if err != nil || len(files) <= BackupKeep {
		return err
	}
	for _, fi := range files[BackupKeep:] {
		_ = os.Remove(filepath.Join(bdir, fi.Name()))
	}
	return nil
}

// Backup 是一条备份记录（给网页的“回滚”列表用）。
type Backup struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modified_at"`
}

// ListBackups 按时间从新到旧列出当前配置文件的备份。
func ListBackups(cfg Config) ([]Backup, error) {
	if cfg.ConfigPath == "" {
		return nil, errors.New("ConfigPath 为空")
	}
	name := filepath.Base(cfg.ConfigPath)
	files, err := backupFiles(filepath.Join(filepath.Dir(cfg.ConfigPath), BackupsDir), name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Backup, 0, len(files))
	for _, fi := range files {
		out = append(out, Backup{Name: fi.Name(), Size: fi.Size(), ModifiedAt: fi.ModTime().Unix()})
	}
	return out, nil
}

// backupFiles 匹配 <name 去扩展名>-*<扩展名>，按 mtime 倒序。
func backupFiles(bdir, name string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(bdir)
	if err != nil {
		return nil, err
	}
	base, ext := strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name)
	var out []os.FileInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), base+"-") || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, fi)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModTime().Equal(out[j].ModTime()) {
			return out[i].Name() > out[j].Name() // 时间戳命名，名字大的更新
		}
		return out[i].ModTime().After(out[j].ModTime())
	})
	return out, nil
}

// RestoreBackup 把某份备份原样写回配置文件（写之前照样先备份现内容），返回新备份路径。
//
// 名字必须是 backups/ 里合法的一个：不接受路径分隔符、不接受以点开头，
// 免得接口变成任意文件读取/写入的入口。内容还要能 Normalize + Validate，
// 否则“回滚”会把服务变成一个起不来的配置。
func RestoreBackup(cfg Config, name string) (string, error) {
	if cfg.ConfigPath == "" {
		return "", errors.New("ConfigPath 为空")
	}
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("备份名 %q 不合法", name)
	}
	bdir := filepath.Join(filepath.Dir(cfg.ConfigPath), BackupsDir)
	p := filepath.Join(bdir, name)
	base, ext := filepath.Base(cfg.ConfigPath), filepath.Ext(cfg.ConfigPath)
	// 备份文件名是 <去扩展名的基名>-<时间戳><扩展名>，例如 config-20260101-120000.json
	if !strings.HasPrefix(name, strings.TrimSuffix(base, ext)+"-") || !strings.HasSuffix(name, ext) {
		return "", fmt.Errorf("备份名 %q 不属于 %s", name, base)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("读取备份失败：%w", err)
	}
	var probe Config
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("备份内容不是合法 JSON：%w", err)
	}
	if err := Normalize(&probe, cfg.ConfigPath); err != nil {
		return "", fmt.Errorf("备份里的配置不可用：%w", err)
	}
	if err := probe.Validate(); err != nil {
		return "", err
	}
	backup := ""
	if cur, err := os.ReadFile(cfg.ConfigPath); err == nil && len(bytes.TrimSpace(cur)) > 0 {
		b, err := writeBackup(filepath.Dir(cfg.ConfigPath), base, cur)
		if err != nil {
			return "", err
		}
		backup = b
	}
	tmp := cfg.ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", fmt.Errorf("写配置文件失败：%w", err)
	}
	if err := os.Rename(tmp, cfg.ConfigPath); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("替换配置文件失败：%w", err)
	}
	_ = pruneBackups(bdir, base)
	return backup, nil
}

// Redacted 返回可以安全发给网页的副本，以及哪些字段「有值但被抹掉了」。
//
// 网页拿不到明文，也就没法把它抄走；配置文件的权限（0600）仍然是唯一的墙。
func (c Config) Redacted() (Config, []string) {
	out := c
	var secrets []string
	if out.Xiaomi.Password != "" {
		secrets = append(secrets, "xiaomi.password")
		out.Xiaomi.Password = ""
	}
	if out.Xiaomi.PassToken != "" {
		secrets = append(secrets, "xiaomi.pass_token")
		out.Xiaomi.PassToken = ""
	}
	return out, secrets
}

// MergeSecrets 把网页留空的敏感字段回填成磁盘上的现值。
//
// 留空只能理解为「没改」，不能理解为「清空」——清空凭据要走显式的登出接口。
func MergeSecrets(in *Config, cur Config) {
	if in.Xiaomi.Password == "" {
		in.Xiaomi.Password = cur.Xiaomi.Password
	}
	if in.Xiaomi.PassToken == "" {
		in.Xiaomi.PassToken = cur.Xiaomi.PassToken
	}
}

// ChangedKeys 返回两份配置之间变过的顶层字段名（按 json tag 报，和文件里一致）。
func ChangedKeys(a, b Config) []string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	t := va.Type()
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" || tag == "player" {
			continue // player 归一化后固定是 nil，比它没意义
		}
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			out = append(out, tag)
		}
	}
	return out
}

// SpeakerDiff 说明 speakers[] 具体动了谁，给「哪些改动要重启」提供可读的原因。
func SpeakerDiff(a, b []SpeakerConfig) (added, removed, modified []string) {
	inA := map[string]SpeakerConfig{}
	inB := map[string]SpeakerConfig{}
	for _, sp := range a {
		inA[sp.ID] = sp
	}
	for _, sp := range b {
		inB[sp.ID] = sp
	}
	for id, sp := range inB {
		old, ok := inA[id]
		switch {
		case !ok:
			added = append(added, fmt.Sprintf("%s（%s）", sp.Name, id))
		case !reflect.DeepEqual(old, sp):
			modified = append(modified, fmt.Sprintf("%s（%s）", sp.Name, id))
		}
	}
	for id, sp := range inA {
		if _, ok := inB[id]; !ok {
			removed = append(removed, fmt.Sprintf("%s（%s）", sp.Name, id))
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(modified)
	return added, removed, modified
}
