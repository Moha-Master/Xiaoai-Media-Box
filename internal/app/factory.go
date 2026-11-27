package app

import (
	"strings"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/xiaomi"
)

// newPlayer 按 speaker.control 选择传输层：
//   - ssh   本地/局域网直接 ssh → ubus（需 ssh_host）
//   - cloud MiNA 云代理（默认，需 xiaomi 凭据）
//   - noop  只打日志
func newPlayer(sp config.SpeakerConfig, x config.XiaomiConfig) (player.Player, error) {
	switch strings.ToLower(sp.Control) {
	case "ssh":
		return player.NewUbusSSH(sp), nil
	case "cloud":
		return xiaomi.NewPlayer(sp, x)
	default:
		return player.NewNoop(), nil
	}
}
