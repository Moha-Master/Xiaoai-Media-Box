package netease

import "testing"

func TestParseLRC(t *testing.T) {
	lrc := `[ti:晴天]
[ar:RyaVocal]
[00:12.50]天气慢慢转晴
[00:15.00][01:20.00]重复的一句
[00:18.20]  带空格的行
[01:00,300]逗号毫秒
[00:20.00]`
	lines := parseLRC(lrc)
	want := []LyricLine{
		{T: 12500, Text: "天气慢慢转晴"},
		{T: 15000, Text: "重复的一句"},
		{T: 18200, Text: "带空格的行"},
		{T: 60300, Text: "逗号毫秒"},
		{T: 80000, Text: "重复的一句"},
	}
	if len(lines) != len(want) {
		t.Fatalf("行数 %d，期望 %d：%+v", len(lines), len(want), lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Fatalf("第 %d 行 %+v，期望 %+v", i, lines[i], w)
		}
	}
}

func TestParseLRCEmpty(t *testing.T) {
	if got := parseLRC(""); got != nil {
		t.Fatalf("空文本应返回 nil，得到 %+v", got)
	}
	if got := parseLRC("没有时间戳的一行\n"); len(got) != 0 {
		t.Fatalf("无时间戳应返回空，得到 %+v", got)
	}
}
