package player

import (
	"strconv"
	"testing"
)

func TestParseStatusNested(t *testing.T) {
	// 实机形态：真正的状态塞在 info 字符串里（mphelper 的 mute_stat 就这么读）
	raw := []byte(`{"code":0,"info":"{\"status\":1,\"volume\":37,\"loop_type\":2,` +
		`\"media_type\":13,\"audio_id\":\"1234567\",\"position\":45200,` +
		`\"duration\":240000,\"global_id\":1234567}"}`)
	st, err := ParseStatus(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if st.Status != 1 || !st.Playing() {
		t.Fatalf("status=%d playing=%v", st.Status, st.Playing())
	}
	if st.AudioID != "1234567" {
		t.Fatalf("audio_id=%q", st.AudioID)
	}
	if st.Position != 45200 || st.Duration != 240000 {
		t.Fatalf("position=%d duration=%d", st.Position, st.Duration)
	}
	if st.GlobalID != 1234567 || st.Volume != 37 || st.MediaType != 13 {
		t.Fatalf("global_id=%d volume=%d media_type=%d", st.GlobalID, st.Volume, st.MediaType)
	}
}

func TestParseStatusFlatAndNumericID(t *testing.T) {
	raw := []byte(`{"code":0,"status":0,"audio_id":42,"position":0,"duration":0}`)
	st, err := ParseStatus(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if st.AudioID != "42" {
		t.Fatalf("数字 audio_id 应转字符串，得到 %q", st.AudioID)
	}
	if st.Playing() {
		t.Fatal("status=0 不应判定为播放中")
	}
}

func TestParseStatusWithLoginBanner(t *testing.T) {
	// 实机抓到的真实输出：ssh 登录 banner 在前，JSON 在后
	raw := []byte("_____  _              __     __ __  ___  ___\r\n" +
		" |     ||_| ___  ___   |  |   |  |  ||   ||  _|\r\n" +
		" ----------------------------------------------\r\n" +
		"      ROM Type:release / Ver:1.82.10\r\n" +
		" ----------------------------------------------\r\n" +
		"{\n\t\"code\": 0,\n\t\"info\": \"{ \\\"status\\\": 0, \\\"volume\\\": 29, \\\"loop_type\\\": 1 }\"\n}\n")
	st, err := ParseStatus(raw)
	if err != nil {
		t.Fatalf("带 banner 的输出应能解析：%v", err)
	}
	if st.Status != 0 || st.Volume != 29 || st.LoopType != 1 {
		t.Fatalf("解析结果不对：%+v", st)
	}
	if st.Playing() {
		t.Fatal("status=0 不该判定为播放中")
	}
}

func TestParseStatusSongDetail(t *testing.T) {
	// 实机抓到的整段（musics 方式在播第 1 首）
	raw := []byte(`{"code":0,"info":"{ \"status\": 1, \"volume\": 19, \"loop_type\": 2, ` +
		`\"media_type\": 13, \"play_song_detail\": { \"audio_id\": \"0\", \"position\": 2026, ` +
		`\"duration\": 29440, \"global_id\": 111111 }, \"track_list\": [ \"0\", \"0\" ] }"}`)
	st, err := ParseStatus(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if st.Position != 2026 || st.Duration != 29440 {
		t.Fatalf("play_song_detail 没被读出来：position=%d duration=%d", st.Position, st.Duration)
	}
	if st.MediaType != 13 || st.LoopType != 2 {
		t.Fatalf("media_type=%d loop_type=%d", st.MediaType, st.LoopType)
	}
	if st.TrackID() != 111111 {
		t.Fatalf("musics 方式应靠 global_id 认曲目，得到 %d", st.TrackID())
	}
}

func TestTrackIDPrefersMeaningfulID(t *testing.T) {
	// musics 方式：audio_id 恒为 "0"，只能看 global_id
	if got := (DeviceStatus{AudioID: "0", GlobalID: 222222}).TrackID(); got != 222222 {
		t.Fatalf("应取 global_id，得到 %d", got)
	}
	// player_play_url 方式：global_id 是 0x7FFFFFFF 哨兵，得回落到 audio_id
	if got := (DeviceStatus{AudioID: "509781655", GlobalID: 0x7FFFFFFF}).TrackID(); got != 509781655 {
		t.Fatalf("应回落到 audio_id，得到 %d", got)
	}
	// 都没用 → 0
	if got := (DeviceStatus{AudioID: "0"}).TrackID(); got != 0 {
		t.Fatalf("应返回 0，得到 %d", got)
	}
}

func TestTrackIDSupports64Bit(t *testing.T) {
	// payload 分支的 audio_id 是字符串，网易云新歌 id 超过 2^31-1 也不会被钳。
	// （musics[] 的 global_id 是 int32，那套 devID 映射已随迁移删除。）
	const big = 2657613721
	st := DeviceStatus{AudioID: strconv.FormatInt(big, 10)}
	if got := st.TrackID(); got != big {
		t.Fatalf("64 位 audio_id 应原样返回，得到 %d", got)
	}
}

func TestParseStatusEmpty(t *testing.T) {
	st, err := ParseStatus([]byte(`{"code":0}`))
	if err != nil {
		t.Fatalf("空状态不该报错：%v", err)
	}
	if st.AudioID != "" || st.Status != 0 {
		t.Fatalf("空状态得到 %+v", st)
	}
}
