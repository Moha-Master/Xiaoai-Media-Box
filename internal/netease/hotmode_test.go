package netease

import (
	"strings"
	"testing"
)

// 真实响应裁剪版：data[] 每项是 {id, alg, recommended, songInfo:{...}}，
// 歌曲字段在 songInfo 里，版权标记在 songInfo.privilege.st。
const hotmodeFixture = `{
  "code": 200,
  "message": "SUCCESS",
  "data": [
    {"id": 11, "alg": "a", "recommended": false, "songInfo": {
      "id": 11, "name": "甲", "dt": 100000,
      "ar": [{"name": "歌手A"}, {"name": "歌手B"}],
      "al": {"name": "专辑A", "picUrl": "http://x/a.jpg"},
      "privilege": {"st": 0}
    }},
    {"id": 12, "alg": "b", "songInfo": {
      "id": 12, "name": "乙", "dt": 200000,
      "ar": [{"name": "歌手C"}],
      "al": {"name": "专辑B", "picUrl": ""},
      "privilege": {"st": -200}
    }},
    {"id": 11, "alg": "dup", "songInfo": {
      "id": 11, "name": "甲（重复）", "dt": 100000, "ar": [], "al": {"name": ""}, "privilege": {"st": 0}
    }},
    {"id": 13, "alg": "c", "songInfo": {
      "id": 13, "name": "丙", "dt": 300000,
      "ar": [{"name": "歌手D"}],
      "al": {"name": "专辑C"},
      "privilege": {"st": 0}
    }}
  ]
}`

func TestParseHotmode(t *testing.T) {
	tracks, err := parseHotmode([]byte(hotmodeFixture))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	// 无版权的 12 被丢、重复的 11 只留一条 → 11, 13
	if len(tracks) != 2 {
		t.Fatalf("应得 2 首，得到 %d：%+v", len(tracks), tracks)
	}
	if tracks[0].ID != 11 || tracks[1].ID != 13 {
		t.Fatalf("顺序/内容不对：%+v", tracks)
	}
	if tracks[0].Artists != "歌手A/歌手B" || tracks[0].Album != "专辑A" || tracks[0].Duration != 100000 {
		t.Fatalf("字段映射不对：%+v", tracks[0])
	}
	if tracks[0].Label() != "歌手A/歌手B - 甲" {
		t.Fatalf("Label 不对：%q", tracks[0].Label())
	}
}

// 去掉人为截断后，parseHotmode 必须返回全部去重后的候选（119 首收 20 首的事故不能再犯）。
func TestParseHotmodeNoTruncation(t *testing.T) {
	tracks, err := parseHotmode([]byte(hotmodeFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 { // fixture 只有 2 个可播 id，但断言它没被 limit 砍
		t.Fatalf("候选不该被截断：%+v", tracks)
	}
}

func TestParseHotmodeErrorCode(t *testing.T) {
	if _, err := parseHotmode([]byte(`{"code":301,"msg":"need login"}`)); err == nil {
		t.Fatal("非 200 的 code 必须报错")
	}
	if _, err := parseHotmode([]byte(`not json`)); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	// 实测：缺 count 键时上游回 {"success":false,"code":"500"}，code 是**字符串**。
	// 按 int 收会炸成"解析失败"，把真实错误码吞掉，排查时完全看不出是参数问题。
	_, err := parseHotmode([]byte(`{"success":false,"code":"500","message":"","data":null}`))
	if err == nil {
		t.Fatal("字符串 code 也必须报错")
	}
	if !strings.Contains(err.Error(), "code=500") {
		t.Fatalf("错误里要带上游真实 code，实际：%v", err)
	}
}
