package player

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
)

// Player 是“设备侧执行面”的抽象。设备不在线/正在调试时用 noop，整套逻辑照样能跑。
type Player interface {
	Play(ctx context.Context, url string, t netease.Track) error
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
	Next(ctx context.Context) error
	Prev(ctx context.Context) error
	Stop(ctx context.Context) error
	Seek(ctx context.Context, positionMs int) error
	Volume(ctx context.Context, value int) error
	Describe() string
}

// WindowTrack 是推给设备的一首。
//
// SongID 是网易云原始 id。AIVS `payload` 分支的 `audio_id` 是**字符串**、支持 64 位，
// 所以不再需要自建小整数——`musics[]` 分支那个 `global_id` 的 int32 限制随之作废，
// `songToDev`/`devToSong` 映射表已删除。
//
// URL 仍指向本服务的稳定 locator（`/s/<songID>`）：设备每次起播才现取现跳，
// 从而绕开网易云直链 20 分钟就过期的问题。**这一层转发躲不掉**——小米自己
// 也是用 `cp_resource_locator` 做同样的事。
type WindowTrack struct {
	SongID   int64
	Duration int
	URL      string
}

// WindowPlayer 是可选能力：把整窗推给 mediaplayer 的 player_play_music。
// 只有 player.use_window=true 且设备空闲时才启用。
type WindowPlayer interface {
	PlayWindow(ctx context.Context, items []WindowTrack, start int) error
}

// LoopSetter 是可选能力：设置设备侧循环模式。
//
// payload 建立的列表**播完即停**（不像 musics[] 的 `play_mode: LoopSequence` 会自己循环），
// 所以每次推窗后要显式设一次 `player_set_loop {type:1}`，才能与我们"窗口 + 绕回检测"
// 的既有模型保持一致（包括 queue.LoopNone 靠"绕回队首"判定播完那套）。
type LoopSetter interface {
	SetLoop(ctx context.Context) error
}

// WindowPayload 把一窗曲目编成 AIVS 标准 payload。
//
// 三个字段是实测出来的硬要求（详见 docs/MEDIA-ARCH.md §7.1.2）：
//   - `audio_type` 必须是**顶层字符串 "MUSIC"**：不给/给错会被当 TTS，设备就不做
//     音乐记账，`play_song_detail`/`track_list`/`audio_meta` 全空；
//   - `needs_loadmore` 必须 0：否则设备播完这批会去小米云端 loadmore，
//     把小米自己的内容续进我们的队列；
//   - `stream.url` 用我们自己的 locator，`authentication` 保持 false。
func WindowPayload(items []WindowTrack) string {
	list := make([]map[string]any, 0, len(items))
	for i, it := range items {
		list = append(list, map[string]any{
			"item_id": map[string]any{
				"audio_id": strconv.FormatInt(it.SongID, 10),
				"cp":       map[string]any{"name": "wangyiyun"},
			},
			"offset": i,
			"stream": map[string]any{
				"url":            it.URL,
				"authentication": false,
				"duration_in_ms": it.Duration,
				"offset_in_ms":   0,
				"redirect":       false,
			},
		})
	}
	// 注意外面必须再包一层 {"payload": ...}：parse_audio_player 先找 payload 字段，
	// 找不到就 "failed by no payload field" 直接返回（实测踩过）。
	return mustJSON(map[string]any{
		"payload": map[string]any{
			"play_behavior":  "REPLACE_ALL",
			"audio_type":     "MUSIC",
			"needs_loadmore": 0,
			"audio_items":    list,
		},
	})
}

// DeviceStatus 是 mediaplayer 的 player_get_play_status 回复。实机形态：
//
//	{"code":0,"info":"{ status, volume, loop_type, media_type,
//	                    play_song_detail:{audio_id, position, duration, global_id},
//	                    track_list:[…] }"}
//
// 关键：真正的细节在 **`play_song_detail` 里另起一层**，不是平铺在 info 顶层。
// 而且 `musics[]` 方式下发的条目 `audio_id` 恒为 "0"，**身份只能靠 `global_id` 认**。
type DeviceStatus struct {
	Status    int    // 1=播放中（实测），0=停止，2=暂停
	Volume    int    // 0-100
	LoopType  int    // 循环模式（内部枚举，2=列表循环）
	MediaType int    // audiotype，13=音乐
	AudioID   string // play_url 方式下是我们传的 id；musics 方式恒为 "0"
	GlobalID  int64  // musics[] 里的 global_id —— 唯一可靠的曲目身份
	Position  int    // 毫秒
	Duration  int    // 毫秒
	Raw       map[string]any
}

func (s DeviceStatus) Playing() bool { return s.Status == 1 }

// TrackID 返回可用于回查队列的曲目 id。
// musics 方式的 audio_id 是常量 "0"，只有 global_id 有意义；play_url 方式反之
// （global_id 是 0x7FFFFFFF 哨兵）。
func (s DeviceStatus) TrackID() int64 {
	switch {
	case s.GlobalID > 0 && s.GlobalID != 0x7FFFFFFF:
		return s.GlobalID
	case s.AudioID != "" && s.AudioID != "0":
		return util.ParseInt64(s.AudioID)
	}
	return 0
}

// StatusProbe 是可选能力：能向设备问“现在播到哪了”。
// 不实现的 Player（比如 noop）退回纯服务端计时。
type StatusProbe interface {
	Status(ctx context.Context) (DeviceStatus, error)
}

// ---------- noop ----------

type noopPlayer struct{}

// NewNoop 返回一个只打日志、不碰设备的播放器。
func NewNoop() Player { return noopPlayer{} }

func (noopPlayer) Play(ctx context.Context, url string, t netease.Track) error {
	log.Printf("[noop] play  %-40s %s", t.Label(), url)
	return nil
}
func (noopPlayer) Pause(ctx context.Context) error  { log.Printf("[noop] pause"); return nil }
func (noopPlayer) Resume(ctx context.Context) error { log.Printf("[noop] resume"); return nil }
func (noopPlayer) Next(ctx context.Context) error   { log.Printf("[noop] next"); return nil }
func (noopPlayer) Prev(ctx context.Context) error   { log.Printf("[noop] prev"); return nil }
func (noopPlayer) Stop(ctx context.Context) error   { log.Printf("[noop] stop"); return nil }
func (noopPlayer) Seek(ctx context.Context, ms int) error {
	log.Printf("[noop] seek %dms", ms)
	return nil
}
func (noopPlayer) Volume(ctx context.Context, v int) error {
	log.Printf("[noop] volume %d", v)
	return nil
}
func (noopPlayer) Describe() string { return "noop（只打日志，不碰设备）" }

// ---------- ubus over ssh ----------

type ubusSSHPlayer struct {
	host    string
	key     string
	timeout int
	media   string
	playTyp int
}

// NewUbusSSH 返回通过 SSH 调设备 ubus 的播放器。
func NewUbusSSH(sp config.SpeakerConfig) Player {
	return &ubusSSHPlayer{
		host:    sp.SSHHost,
		key:     sp.SSHKey,
		timeout: sp.UbusTimeout,
		media:   sp.Media,
		playTyp: sp.PlayType,
	}
}

func (p *ubusSSHPlayer) Describe() string {
	return fmt.Sprintf("ubus-ssh → %s（media=%s）", p.host, p.media)
}

func (p *ubusSSHPlayer) runOut(ctx context.Context, remote string) ([]byte, error) {
	return p.runOutInput(ctx, remote, nil)
}

// runOutInput 和 runOut 一样，但把 stdin 喂给远端 shell。
//
// 设备 sshd 对**命令行长度**有约 8KB 的硬限制（实测 8000B 正常、10000B 直接
// "Connection reset by peer"）。整窗 payload 一旦超过这个量级，就不能再塞进
// ssh 的 command 参数，只能走 stdin 传到设备临时文件再由远端展开。
func (p *ubusSSHPlayer) runOutInput(ctx context.Context, remote string, stdin []byte) ([]byte, error) {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=4",
		"-o", "StrictHostKeyChecking=no",
	}
	if p.key != "" {
		args = append(args, "-i", p.key)
	}
	args = append(args, p.host, remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("ssh %s 失败: %v: %s", p.host, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (p *ubusSSHPlayer) run(ctx context.Context, remote string) error {
	_, err := p.runOut(ctx, remote)
	return err
}

// ubus 的 JSON 参数统一用单引号包裹（remote shell 会剥掉），内容里不含单引号。
func (p *ubusSSHPlayer) ubusCall(method string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return p.run(ctx, fmt.Sprintf("ubus -t %d call mediaplayer %s '%s'", p.timeout, method, raw))
}

func (p *ubusSSHPlayer) operation(action string) error {
	return p.ubusCall("player_play_operation", map[string]any{
		"action": action,
		"media":  p.media,
	})
}

func (p *ubusSSHPlayer) Play(ctx context.Context, url string, t netease.Track) error {
	return p.ubusCall("player_play_url", map[string]any{
		"url":      url,
		"type":     p.playTyp,
		"media":    p.media,
		"id":       strconv.FormatInt(t.ID, 10),
		"duration": t.Duration,
		"auth":     false,
	})
}

// PlayWindow 推整窗，走 AIVS payload 分支（`docs/MEDIA-ARCH.md` §7.1.2）。
//
// 为什么不是 musics[]：那个分支的 `global_id` 只有 int32 宽，网易云新歌 id 会被钳成
// 2147483647，多首撞车后队列就跟不住；而 payload 的 `audio_id` 是字符串，64 位无压力，
// 还能顺带上报 `cp`，回读到的 `track_list` 也是真实 id。
//
// 窗口是**旋转过**的（当前曲排第一），所以不需要 `startaudioid`。
func (p *ubusSSHPlayer) PlayWindow(ctx context.Context, items []WindowTrack, start int) error {
	raw, err := json.Marshal(map[string]any{
		"music": WindowPayload(items),
		"media": p.media,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 小窗口直接走命令行；大窗口（50 首约 10KB）会被设备 sshd 断连。
	// 阈值取 6KB，给命令前缀和转义留余量。
	if len(raw) < 6000 {
		return p.run(ctx, fmt.Sprintf("ubus -t %d call mediaplayer player_play_music '%s'", p.timeout, raw))
	}
	remote := fmt.Sprintf(
		`f=$(mktemp) || exit 1; trap 'rm -f "$f"' EXIT INT TERM; cat > "$f" && ubus -t %d call mediaplayer player_play_music "$(cat "$f")"`,
		p.timeout)
	_, err = p.runOutInput(ctx, remote, raw)
	return err
}

// SetLoop 让设备循环当前这一窗（type=1）。必须每次推窗后重设：
// payload 列表默认播完即停，不设的话 queue.LoopNone/queue.LoopList 的语义都会变。
func (p *ubusSSHPlayer) SetLoop(ctx context.Context) error {
	return p.run(ctx, fmt.Sprintf(
		"ubus -t %d call mediaplayer player_set_loop '{\"type\":1,\"media\":\"%s\"}'",
		p.timeout, p.media))
}

func (p *ubusSSHPlayer) Pause(ctx context.Context) error  { return p.operation("pause") }
func (p *ubusSSHPlayer) Resume(ctx context.Context) error { return p.operation("play") }
func (p *ubusSSHPlayer) Next(ctx context.Context) error   { return p.operation("next") }
func (p *ubusSSHPlayer) Prev(ctx context.Context) error   { return p.operation("prev") }
func (p *ubusSSHPlayer) Stop(ctx context.Context) error   { return p.operation("stop") }

// ubusCallChecked 和 ubusCall 一样，但会检查回复体里的 code（0 = 成功）。
//
// 有些方法（比如 player_set_positon）在设备正在切歌时会返回 code 126，
// 而 ssh 本身是成功的——不检查就会误以为拖进度成功。
func (p *ubusSSHPlayer) ubusCallChecked(ctx context.Context, method string, payload map[string]any) error {
	out, err := p.runOut(ctx, fmt.Sprintf("ubus -t %d call mediaplayer %s '%s'", p.timeout, method, mustJSON(payload)))
	if err != nil {
		return err
	}
	i := strings.IndexByte(string(out), '{')
	if i < 0 {
		return nil
	}
	var r struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(out[i:], &r); err == nil && r.Code != 0 {
		return fmt.Errorf("ubus %s code=%d", method, r.Code)
	}
	return nil
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// Seek 拖进度：player_set_positon，position 单位毫秒（实机验证：60000 → 61.9s）。
func (p *ubusSSHPlayer) Seek(ctx context.Context, positionMs int) error {
	return p.ubusCallChecked(ctx, "player_set_positon", map[string]any{
		"position": positionMs,
		"media":    p.media,
	})
}

// Volume 走 mphelper：libmico-common 里就是这么调的，比直接 player_set_volume 少踩参数坑。
func (p *ubusSSHPlayer) Volume(ctx context.Context, value int) error {
	return p.run(ctx, fmt.Sprintf("/usr/bin/mphelper volume_set %d", value))
}

// Status 问设备“现在播到哪了”。这是把服务端估算换成设备真相的唯一入口，
// 也是发现“用户在机身按了下一首”的唯一手段（纯 pull，设备零改动）。
func (p *ubusSSHPlayer) Status(ctx context.Context) (DeviceStatus, error) {
	out, err := p.runOut(ctx, fmt.Sprintf("ubus -t %d call mediaplayer player_get_play_status", p.timeout))
	if err != nil {
		return DeviceStatus{}, err
	}
	return ParseStatus(out)
}

// extractJSON 从可能夹着 banner / 提示符的输出里截出 JSON 对象。
func extractJSON(raw []byte) ([]byte, error) {
	i := bytes.IndexByte(raw, '{')
	j := bytes.LastIndexByte(raw, '}')
	if i < 0 || j <= i {
		return nil, fmt.Errorf("输出里没有 JSON：%s", util.Snippet(raw))
	}
	return raw[i : j+1], nil
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
	case string:
		var n int64
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

// ParseStatus 兼容两种回复：{"code":0,"info":"{…}"}（实机形态，mphelper 已验证）
// 和直接平铺的那种（万一固件换了）。
//
// 实机踩到的坑：`ssh root@… "ubus call …"` 的 stdout 前面带着**登录 banner**
// （ROM Type / Ver 那一坨），直接 Unmarshal 必失败。所以先截取第一个 `{` 到最后一个 `}`。
func ParseStatus(raw []byte) (DeviceStatus, error) {
	body, err := extractJSON(raw)
	if err != nil {
		return DeviceStatus{}, err
	}
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return DeviceStatus{}, fmt.Errorf("状态不是合法 JSON：%w: %s", err, util.Snippet(body))
	}
	m := top
	if info, ok := top["info"]; ok {
		switch v := info.(type) {
		case string:
			var inner map[string]any
			if err := json.Unmarshal([]byte(v), &inner); err == nil {
				m = inner
			}
		case map[string]any:
			m = v
		}
	}
	st := DeviceStatus{
		Status:    util.AsInt(m["status"]),
		Volume:    util.AsInt(m["volume"]),
		LoopType:  util.AsInt(m["loop_type"]),
		MediaType: util.AsInt(m["media_type"]),
		AudioID:   util.AsString(m["audio_id"]),
		GlobalID:  asInt64(m["global_id"]),
		Position:  util.AsInt(m["position"]),
		Duration:  util.AsInt(m["duration"]),
		Raw:       m,
	}
	// 实机把 audio_id/position/duration/global_id 收在 play_song_detail 子对象里
	if d, ok := m["play_song_detail"].(map[string]any); ok {
		st.AudioID = util.AsString(d["audio_id"])
		st.GlobalID = asInt64(d["global_id"])
		st.Position = util.AsInt(d["position"])
		st.Duration = util.AsInt(d["duration"])
	}
	if _, ok := m["status"]; !ok {
		// 空回复（没在播）时设备可能只给 code
		if _, has := m["code"]; has && len(m) == 1 {
			return st, nil
		}
	}
	return st, nil
}
