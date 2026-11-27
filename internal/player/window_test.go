package player

import (
	"encoding/json"
	"testing"
)

// 把 WindowPayload 的形状钉死：这三条是实测出来的硬要求（MEDIA-ARCH §7.1.2），
// 任何一条写错都会让设备"能播但不记账"（play_song_detail/audio_meta 全空）。
func TestWindowPayloadShape(t *testing.T) {
	items := []WindowTrack{
		{SongID: 469391964, Duration: 323693, URL: "http://nas:8088/s/469391964"},
		{SongID: 2657613721, Duration: 200000, URL: "http://nas:8088/s/2657613721"},
	}
	// 外层必须是 {"payload": ...}
	var outer struct {
		Payload *struct {
			PlayBehavior  string `json:"play_behavior"`
			AudioType     string `json:"audio_type"`
			NeedsLoadmore int    `json:"needs_loadmore"`
			AudioItems    []struct {
				ItemID struct {
					AudioID string `json:"audio_id"`
					CP      struct {
						Name string `json:"name"`
					} `json:"cp"`
				} `json:"item_id"`
				Offset int `json:"offset"`
				Stream struct {
					URL            string `json:"url"`
					Authentication bool   `json:"authentication"`
					DurationInMs   int    `json:"duration_in_ms"`
				} `json:"stream"`
			} `json:"audio_items"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(WindowPayload(items)), &outer); err != nil {
		t.Fatalf("payload 不是合法 JSON：%v", err)
	}
	if outer.Payload == nil {
		t.Fatal(`缺少顶层 "payload" 包装（设备会报 failed by no payload field）`)
	}
	got := *outer.Payload

	// ① audio_type 必须在顶层且是字符串 "MUSIC"（塞进 item 里会退化成 TTS）
	if got.AudioType != "MUSIC" {
		t.Fatalf(`audio_type 必须是顶层 "MUSIC"，得到 %q`, got.AudioType)
	}
	// ② needs_loadmore 必须 0，否则设备会去小米云端续内容
	if got.NeedsLoadmore != 0 {
		t.Fatalf("needs_loadmore 必须为 0，得到 %d", got.NeedsLoadmore)
	}
	if got.PlayBehavior != "REPLACE_ALL" {
		t.Fatalf("play_behavior=%q", got.PlayBehavior)
	}
	if len(got.AudioItems) != 2 {
		t.Fatalf("audio_items 应有 2 条，得到 %d", len(got.AudioItems))
	}
	// ③ audio_id 必须是字符串且保留 64 位（这个 id > 2^31-1）
	if it := got.AudioItems[1]; it.ItemID.AudioID != "2657613721" {
		t.Fatalf("audio_id 应原样保留 64 位 id，得到 %q", it.ItemID.AudioID)
	}
	for i, it := range got.AudioItems {
		if it.Offset != i {
			t.Fatalf("offset 应等于下标，第 %d 条是 %d", i, it.Offset)
		}
		if it.ItemID.CP.Name != "wangyiyun" {
			t.Fatalf("cp.name=%q", it.ItemID.CP.Name)
		}
		if it.Stream.Authentication {
			t.Fatal("authentication 必须是 false，否则设备会给我们的 URL 硬拼 MIOT auth")
		}
		if it.Stream.URL != items[i].URL {
			t.Fatalf("url 被改了：%q != %q", it.Stream.URL, items[i].URL)
		}
	}
}
