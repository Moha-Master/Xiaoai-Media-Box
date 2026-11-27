package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverConfig(t *testing.T) {
	dir := t.TempDir()
	if got := discoverConfig(dir); got != "" {
		t.Fatalf("空目录应返回空，得到 %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := discoverConfig(dir); got != filepath.Join(dir, "config.json") {
		t.Fatalf("得到 %q", got)
	}
	// xiaoai-mb.json 优先级更高
	if err := os.WriteFile(filepath.Join(dir, "xiaoai-mb.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := discoverConfig(dir); got != filepath.Join(dir, "xiaoai-mb.json") {
		t.Fatalf("得到 %q", got)
	}
}
