package netease

import "testing"

// 真实响应裁剪版（2026-11 实测）：每条带自己的 code，不存在的 id 是 404 + url 空；
// freeTrialInfo.start/end 是原曲时间轴上的秒；试听曲 level 被降成 standard。
const songURLsFixture = `{
  "data": [
    {"id": 108914, "url": "http://m7.music.net/1.mp3", "code": 200, "expi": 1200,
     "time": 22752, "br": 128000, "level": "standard",
     "freeTrialInfo": {"start": 69, "end": 92, "fragmentType": 1003}},
    {"id": 1371780785, "url": "http://m7.music.net/2.mp3", "code": 200,
     "expi": 1200, "time": 243000, "br": 320001, "level": "exhigh"},
    {"id": 999999999999, "url": null, "code": 404, "time": 0, "freeTrialInfo": null},
    {"id": 29848657, "url": "http://m7.music.net/3.mp3", "code": 200,
     "expi": 0, "time": 20035, "br": 128000, "level": "standard",
     "freeTrialInfo": {"start": 0, "end": 20}}
  ]
}`

func TestParsePlayInfoTrial(t *testing.T) {
	info, ok := parsePlayInfo(songURLItem{
		ID: 108914, URL: "http://m7.music.net/1.mp3", Code: 200, Expi: 1200,
		Time: 22752, Br: 128000, Level: "standard",
		Trial: &trialRange{Start: 69, End: 92},
	})
	if !ok {
		t.Fatal("试听曲应解析成功")
	}
	if info.StartMs != 69000 || info.EndMs != 92000 {
		t.Fatalf("试听区间应为 69000–92000（原曲轴），得到 %d–%d", info.StartMs, info.EndMs)
	}
	if info.ClipMs != 22752 || info.Level != "standard" {
		t.Fatalf("ClipMs/Level 不对：%+v", info)
	}
}

func TestParsePlayInfoFull(t *testing.T) {
	info, ok := parsePlayInfo(songURLItem{ID: 1, URL: "u", Expi: 1200, Time: 243000, Level: "exhigh"})
	if !ok {
		t.Fatal("全曲应解析成功")
	}
	if info.StartMs != 0 || info.EndMs != 0 || info.Expi != 1200 {
		t.Fatalf("全曲不该有试听区间：%+v", info)
	}
}

func TestParsePlayInfoNoURL(t *testing.T) {
	if _, ok := parsePlayInfo(songURLItem{ID: 1, Code: 404}); ok {
		t.Fatal("url 为空必须返回 false")
	}
}

func TestParsePlayInfoExpiFallback(t *testing.T) {
	info, ok := parsePlayInfo(songURLItem{ID: 1, URL: "u", Expi: 0})
	if !ok || info.Expi != 600 {
		t.Fatalf("expi 缺省应回退 600，得到 %+v", info)
	}
}

func TestTrackTrialHelpers(t *testing.T) {
	full := Track{ID: 1, Duration: 243000}
	if full.IsTrial() || full.PlayableEndMs() != 243000 || full.ClipMs() != 243000 {
		t.Fatalf("全曲判定不对：%v %d %d", full.IsTrial(), full.PlayableEndMs(), full.ClipMs())
	}
	trial := Track{ID: 2, Duration: 267946, TrialStartMs: 69000, TrialEndMs: 92000}
	if !trial.IsTrial() {
		t.Fatal("副歌切片应判为试听")
	}
	if trial.PlayableEndMs() != 92000 || trial.ClipMs() != 23000 {
		t.Fatalf("PlayableEndMs/ClipMs 不对：%d %d", trial.PlayableEndMs(), trial.ClipMs())
	}
	// end == Duration 视为全曲（网易偶尔给 end=整曲的标记）
	degenerate := Track{Duration: 200000, TrialEndMs: 200000}
	if degenerate.IsTrial() || degenerate.PlayableEndMs() != 200000 {
		t.Fatalf("end==Duration 应视为全曲：%v %d", degenerate.IsTrial(), degenerate.PlayableEndMs())
	}
}
