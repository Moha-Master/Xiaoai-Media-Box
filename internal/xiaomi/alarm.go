package xiaomi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/util"
)

// AlarmVolumeController 是“闹钟音量”这条独立能力。
//
// 为什么不复用播放音量：闹钟是另一套状态。设备侧由 alarmd 维护，
// 持久化在 /data/player/alarmvolume.cfg，响铃时才通过 mediaplayer 的
// `player_set_volume {type:"alarm"}` 应用。改它不会影响正在放的音乐。
//
// 为什么只走云端 MiNA：闹钟音量在 MIoT 规范里没有暴露（实测型号的 spec 只有
// speaker.volume / clock.stop-alarm），手机 app.App 走的是云端 ubus 代理。
// 走 MiNA 的好处是**不依赖 SSH、不要求设备在局域网**，定制机和原厂机行为一致。
type AlarmVolumeController interface {
	AlarmVolume(ctx context.Context) (int, error)
	SetAlarmVolume(ctx context.Context, volume int) error
}

// Alarm 通过 https://api2.mina.mi.com/remote/ubus 调 alarm.* 两个方法。
type Alarm struct {
	client   *Client
	deviceID string
}

// NewAlarm 返回一台音箱的闹钟音量控制器（只走云端 MiNA）。
func NewAlarm(client *Client, deviceID string) AlarmVolumeController {
	return &Alarm{client: client, deviceID: deviceID}
}

func (c *Alarm) AlarmVolume(ctx context.Context) (int, error) {
	info, err := c.client.ubusCall(ctx, c.deviceID, "alarm", "alarm_get_volume", map[string]any{})
	if err != nil {
		return 0, err
	}
	return parseAlarmVolume(info)
}

// parseAlarmVolume 解析 alarm_get_volume 的 info。
// 实机返回：`{ "volume": 17 }`（带空格，别按固定格式切）。
func parseAlarmVolume(info string) (int, error) {
	var r struct {
		Volume int `json:"volume"`
	}
	if err := json.Unmarshal([]byte(info), &r); err != nil {
		return 0, fmt.Errorf("解析闹钟音量失败：%w（%s）", err, util.Snippet([]byte(info)))
	}
	if r.Volume < 0 || r.Volume > 100 {
		return 0, fmt.Errorf("闹钟音量越界：%d（设备回读异常）", r.Volume)
	}
	return r.Volume, nil
}

func (c *Alarm) SetAlarmVolume(ctx context.Context, volume int) error {
	if volume < 0 {
		volume = 0
	}
	if volume > 100 {
		volume = 100
	}
	_, err := c.client.ubusCall(ctx, c.deviceID, "alarm", "alarm_set_volume", map[string]any{
		"volume":    volume,
		"isPreview": 0,
	})
	return err
}
