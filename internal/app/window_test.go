package app

import (
	"testing"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
)

// deviceURL 必须给设备一个稳定 locator，而不是会过期的直链。
func TestDeviceURLIsStableLocator(t *testing.T) {
	a := &App{cfg: config.Config{PublicBase: "http://nas:8088/"}}
	const song = 2657613721
	if got, want := a.deviceURL(song), "http://nas:8088/s/2657613721"; got != want {
		t.Fatalf("deviceURL=%q，期望 %q", got, want)
	}
}
