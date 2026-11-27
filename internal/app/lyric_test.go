package app

import (
	"testing"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
)

func TestLyricAt(t *testing.T) {
	lines := []netease.LyricLine{{T: 1000, Text: "a"}, {T: 5000, Text: "b"}, {T: 9000, Text: "c"}}
	if idx, cur, next := lyricAt(lines, 0); idx != -1 || cur != "" || next != "" {
		t.Fatalf("前奏应还没有词：%d %q %q", idx, cur, next)
	}
	if idx, cur, next := lyricAt(lines, 5200); idx != 1 || cur != "b" || next != "c" {
		t.Fatalf("5.2s 应在第 2 行：%d %q %q", idx, cur, next)
	}
	if idx, cur, next := lyricAt(lines, 99000); idx != 2 || cur != "c" || next != "" {
		t.Fatalf("最后一行不该有 next：%d %q %q", idx, cur, next)
	}
	if idx, cur, _ := lyricAt(nil, 1000); idx != -1 || cur != "" {
		t.Fatalf("无歌词应安静：%d %q", idx, cur)
	}
}
