package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 队列持久化默认开；显式写 false 才算关。BaseDir 由配置文件目录推导。
func TestQueuePersistAndBaseDir(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.json")

	for _, body := range []string{`{}`, `{"queue_persist":true}`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.QueuePersist {
			t.Fatalf("默认应开启持久化（body=%s）", body)
		}
		if cfg.BaseDir != dir {
			t.Fatalf("BaseDir 应是配置文件目录，得到 %q", cfg.BaseDir)
		}
	}

	if err := os.WriteFile(p, []byte(`{"queue_persist":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QueuePersist {
		t.Fatal("显式 false 应关掉持久化")
	}
}

// 配置文件不存在时（走默认配置）BaseDir 也要有，否则快照会写到 cwd。
func TestBaseDirWhenConfigMissing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseDir != dir {
		t.Fatalf("BaseDir=%q，应为 %q", cfg.BaseDir, dir)
	}
}
