package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PlayerConfig 是旧版单设备配置，保留兼容；新配置请用 speakers[]。
type PlayerConfig struct {
	Type        string `json:"type"`         // noop | ubus-ssh | mina
	SSHHost     string `json:"ssh_host"`     // root@192.168.1.20
	SSHKey      string `json:"ssh_key"`      // 私钥路径，空则用 ssh 默认
	UbusTimeout int    `json:"ubus_timeout"` // 秒
	Media       string `json:"media"`        // mediaplayer 通道，音乐用 common
	PlayType    int    `json:"play_type"`    // player_play_url 的 type
	UseWindow   bool   `json:"use_window"`   // true 时用 player_play_music 推整窗
	StatusPoll  int    `json:"status_poll"`  // 回读间隔（秒），-1=关闭
}

// SpeakerConfig 描述“一台音箱以及怎么操控它”。
//
// control 不填默认 cloud（云端 MiNA）；填 ssh 时必须给 ssh_host。
type SpeakerConfig struct {
	ID          string `json:"id"`                    // 稳定标识，默认按序号生成
	Name        string `json:"name"`                  // 显示名
	Control     string `json:"control"`               // cloud（默认）| ssh | noop
	StatusMode  string `json:"status_mode,omitempty"` // 状态感知：cloud（默认）| push（推荐给刷机设备）| ssh | none
	DeviceID    string `json:"device_id"`             // 云端：MiNA 设备 deviceID（UUID）
	SSHHost     string `json:"ssh_host"`              // ssh：root@192.168.1.20
	SSHKey      string `json:"ssh_key"`               // ssh：私钥路径
	UbusTimeout int    `json:"ubus_timeout"`          // ssh：ubus 超时（秒）
	Media       string `json:"media"`                 // 通道：ssh 用 common，cloud 用 app_ios
	PlayType    int    `json:"play_type"`             // player_play_url 的 type，默认 1
	UseWindow   *bool  `json:"use_window"`            // 整窗下发，默认 true
	StatusPoll  int    `json:"status_poll"`           // 稳态回读间隔（秒），0=按 control 默认，-1=关闭
	PollBurst   int    `json:"poll_burst"`            // 不稳定态回读间隔（秒），0=默认 5
	PollRounds  int    `json:"poll_rounds"`           // 不稳定态持续几轮，0=默认 3
	PollDriftMs int    `json:"poll_drift_ms"`         // 位置偏差多大算不可靠，0=默认 4000

	// 以下四项只在 status_mode=push 时有意义，是**服务端判定用的期望值**，
	// 必须与音箱上 daemon 的 /data/etc/state_reporter.conf 保持一致
	// （daemon 的静默阈值由这里推出来，两边不一致就会误判离线）。
	ReportInterval        int `json:"report_interval,omitempty"`    // daemon 本地采样间隔（秒），0=默认 5
	ReportHBPlay          int `json:"report_hb_play,omitempty"`     // 播放中心跳（秒），0=默认 15
	ReportHBIdle          int `json:"report_hb_idle,omitempty"`     // 暂停/空闲心跳（秒），0=默认 30
	ReportSeekToleranceMs int `json:"report_seek_tol_ms,omitempty"` // 位置异常容差（毫秒），0=默认 8000
}

// XiaomiConfig 是云端 MiNA 需要的账号凭据。
//
// 注意两个 device_id 不是一回事：
//   - xiaomi.device_id      登录用的手机设备 id（16 位，来自 .mi.token），不是音箱
//   - speakers[].device_id  音箱自己的 MiNA deviceID（UUID）
type XiaomiConfig struct {
	// user / password 只用于 `-login xiaomi` 的交互式登录，运行时永不使用。
	// 登录成功后一切以 token_file（加密的认证文件）为准，见 mina_auth.go。
	User      string `json:"user"`       // userId（可留空，登录时提示输入）
	Password  string `json:"password"`   // 可选：留空则登录时提示输入（不回显）
	PassToken string `json:"pass_token"` // 已废弃：仅为兼容旧配置保留，运行时不再读取
	DeviceID  string `json:"device_id"`  // 登录设备 id，与 passToken 绑定，必须稳定
	TokenFile string `json:"token_file"` // 认证文件（AES-256-GCM 加密）
	KeyFile   string `json:"key_file"`   // 密钥文件（0600），默认 token_file + ".key"
}

type Config struct {
	Listen        string `json:"listen"`
	PublicBase    string `json:"public_base"`
	CookieFile    string `json:"cookie_file"`
	CookieKeyFile string `json:"cookie_key_file"`
	Quality       string `json:"quality"`
	Window        int    `json:"window"`
	Loop          string `json:"loop"`

	// 无限心动（续杯）：队列里当前曲之后不足 HotmodeExtendBelow 首时再取一批推荐。
	// 0 = 跟随 window；负数 = 关闭（心动模式退回"取一次、循环这一窗"）。
	HotmodeExtendBelow int `json:"hotmode_extend_below"`
	// 连续取歌失败几次后放弃续杯，退回普通列表播放。0 = 3。
	HotmodeExtendMaxFail int `json:"hotmode_extend_max_fail"`

	StreamMode string          `json:"stream_mode"`
	Speakers   []SpeakerConfig `json:"speakers"`
	Xiaomi     XiaomiConfig    `json:"xiaomi"`
	// Player 是旧版单设备配置，只在 speakers[] 为空时被升级成一台音箱。
	// 用指针 + omitempty：升级完成后保存配置就不会再带上这个块。
	Player *PlayerConfig `json:"player,omitempty"`

	// 播放队列持久化：每台音箱一份 <工作目录>/state/queue-<id>.json，
	// 重启后恢复列表/进度/心动/音量。恢复时不会自动出声（设备端还在播就接着认领）。
	QueuePersist bool `json:"queue_persist"`

	// 下面两个不是配置项，由 Normalize 填：
	// BaseDir  = 配置文件所在目录（认证文件与运行态快照都相对它解析）
	// ConfigPath = 实际加载的文件（discoverConfig 可能选中 xiaoai-mb.json 也可能是 config.json，
	//            写回时必须知道到底是哪一个）
	BaseDir    string `json:"-"`
	ConfigPath string `json:"-"`
}

func Default() Config {
	return Config{
		Listen:     "0.0.0.0:8088",
		PublicBase: "http://127.0.0.1:8088",
		CookieFile: "cookie.json",
		Quality:    "exhigh",
		Window:     20,
		Loop:       "list",
		StreamMode: "redirect",

		QueuePersist: true,

		HotmodeExtendBelow:   0, // 0 → 跟随 window（在 Load 里解析）
		HotmodeExtendMaxFail: 3,
		Xiaomi: XiaomiConfig{
			TokenFile: ".mina_token.json",
			// KeyFile 故意留空：必须由最终确定的 token_file 推导出来。
			// 这里若给个默认值，就会挡住下面的推导逻辑，
			// 结果密钥按相对路径落到进程 cwd（实测跑到 /root/），而认证文件用绝对路径。
		},
		Player: &PlayerConfig{
			Type:        "noop",
			SSHHost:     "root@192.168.1.20",
			UbusTimeout: 5,
			Media:       "common",
			PlayType:    1,
			StatusPoll:  15,
		},
	}
}

// NormalizeSpeakers 把旧 player 块升级成 speakers[]，并补齐每台的默认值。
// 独立出来是为了让命令行 -player 覆盖后还能再跑一遍。
func NormalizeSpeakers(cfg *Config) error {
	// 只有**显式的旧版 player 块**才升级成 speakers[]。
	// 手工构造的 Config、或者配置里 speakers 被清空的情况都不该凭空造出一台音箱。
	if len(cfg.Speakers) == 0 && cfg.Player != nil {
		pl := cfg.Player
		sp := SpeakerConfig{
			ID:          "default",
			Name:        "默认",
			SSHHost:     pl.SSHHost,
			SSHKey:      pl.SSHKey,
			UbusTimeout: pl.UbusTimeout,
			Media:       pl.Media,
			PlayType:    pl.PlayType,
			StatusPoll:  pl.StatusPoll,
		}
		w := pl.UseWindow
		sp.UseWindow = &w
		switch pl.Type {
		case "ubus-ssh":
			sp.Control = "ssh"
		case "mina":
			sp.Control = "cloud"
		default:
			sp.Control = "noop"
		}
		if sp.Control == "ssh" {
			sp.Name = "默认（SSH）"
		} else if sp.Control == "cloud" {
			sp.Name = "默认（云端）"
		}
		cfg.Speakers = []SpeakerConfig{sp}
	}

	needCloud := false
	for i := range cfg.Speakers {
		sp := &cfg.Speakers[i]
		if sp.Control == "" {
			sp.Control = "cloud" // 用户约定：不设置默认走云端
		}
		sp.Control = strings.ToLower(sp.Control)
		if sp.StatusMode == "" {
			switch sp.Control {
			case "ssh":
				if sp.StatusPoll == -1 {
					sp.StatusMode = "none"
				} else {
					sp.StatusMode = "ssh"
				}
			case "cloud":
				if sp.StatusPoll == -1 {
					sp.StatusMode = "none"
				} else {
					sp.StatusMode = "cloud"
				}
			default:
				sp.StatusMode = "none"
			}
		}
		sp.StatusMode = strings.ToLower(sp.StatusMode)
		if sp.StatusMode == "push" || sp.StatusMode == "none" {
			sp.StatusPoll = -1
		}
		if sp.ID == "" {
			sp.ID = fmt.Sprintf("spk%d", i+1)
		}
		if sp.Name == "" {
			sp.Name = sp.ID
		}
		if sp.PlayType == 0 {
			sp.PlayType = 1
		}
		if sp.StatusPoll == 0 {
			switch sp.Control {
			case "ssh":
				// 稳态统一 60s（和云端一致）：外部变更靠 5s×3 的 burst，
				// 以及 SSE 推送（xiaoai-mb 有真值，变化才推，不需要靠轮询抢）
				sp.StatusPoll = 60
			case "cloud":
				// 走 MiNA 公网：稳态懒一点，靠不稳定态（5s×3）补跟手
				sp.StatusPoll = 60
			default:
				sp.StatusPoll = -1
			}
		}
		if sp.UseWindow == nil {
			w := sp.Control != "noop"
			sp.UseWindow = &w
		}
		switch sp.Control {
		case "ssh":
			if sp.SSHHost == "" {
				return fmt.Errorf("speaker %q 选择 ssh，但缺少 ssh_host", sp.Name)
			}
			if sp.UbusTimeout <= 0 {
				sp.UbusTimeout = 5
			}
			if sp.Media == "" {
				sp.Media = "common"
			}
		case "cloud":
			needCloud = true
			if sp.Media == "" {
				sp.Media = "app_ios"
			}
		case "noop":
			if sp.Media == "" {
				sp.Media = "common"
			}
		default:
			return fmt.Errorf("speaker %q 的 control=%q 不支持（cloud / ssh / noop）", sp.Name, sp.Control)
		}
	}

	if needCloud {
		if cfg.Xiaomi.TokenFile == "" {
			cfg.Xiaomi.TokenFile = ".mina_token.json"
		}
		if cfg.Xiaomi.KeyFile == "" {
			cfg.Xiaomi.KeyFile = cfg.Xiaomi.TokenFile + ".key"
		}
	}
	// 旧 player 块的使命到此完成：要么被升级成 speakers[]，要么本来就被 speakers[] 取代。
	// 留着它，配置页保存时就会把一堆废弃字段又写回文件。
	cfg.Player = nil
	return nil
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return cfg, err
			}
		} else if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, err
		}
	}
	if err := Normalize(&cfg, path); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Normalize 补齐缺省值、按配置文件目录解析认证路径、把旧 player 块升级成 speakers[]，
// 并记下 BaseDir / ConfigPath。
//
// 导出它是因为**保存必须走同一套**：否则网页写回去的东西和重启后 Load 出来的不是
// 同一回事（默认值忽有忽无、相对/绝对路径来回变），也没法可靠地比对“改了哪些项”。
func Normalize(cfg *Config, path string) error {
	d := Default()
	if cfg.Listen == "" {
		cfg.Listen = d.Listen
	}
	if cfg.PublicBase == "" {
		cfg.PublicBase = d.PublicBase
	}
	if cfg.CookieFile == "" {
		cfg.CookieFile = d.CookieFile
	}
	if cfg.CookieKeyFile == "" {
		cfg.CookieKeyFile = cfg.CookieFile + ".key"
	}
	if cfg.Quality == "" {
		cfg.Quality = d.Quality
	}
	if cfg.Window <= 0 {
		cfg.Window = d.Window
	}
	// 无限心动：0 = 跟随 window，负数 = 关闭；上限缺省 3 次
	if cfg.HotmodeExtendBelow == 0 {
		cfg.HotmodeExtendBelow = cfg.Window
	}
	if cfg.HotmodeExtendBelow < 0 {
		cfg.HotmodeExtendBelow = 0
	}
	if cfg.HotmodeExtendMaxFail <= 0 {
		cfg.HotmodeExtendMaxFail = d.HotmodeExtendMaxFail
	}
	if cfg.Loop == "" {
		cfg.Loop = d.Loop
	}
	if cfg.StreamMode == "" {
		cfg.StreamMode = d.StreamMode
	}
	if cfg.Player != nil {
		p := *cfg.Player
		if p.Type == "" {
			p.Type = d.Player.Type
		}
		if p.Media == "" {
			p.Media = d.Player.Media
		}
		if p.UbusTimeout <= 0 {
			p.UbusTimeout = d.Player.UbusTimeout
		}
		cfg.Player = &p
	}
	if cfg.Xiaomi.TokenFile == "" {
		cfg.Xiaomi.TokenFile = d.Xiaomi.TokenFile
	}
	if cfg.Xiaomi.KeyFile == "" {
		cfg.Xiaomi.KeyFile = cfg.Xiaomi.TokenFile + ".key"
	}

	// 认证文件的相对路径**相对配置文件**，而不是进程 cwd。
	// 否则 `xiaoai-mb -config /x/config.json -login xiaomi` 会把 token 写到当前目录，
	// 而且换个目录跑就会"找不到已登录的凭据"（实机踩过）。
	baseDir := "."
	if path != "" {
		baseDir = filepath.Dir(path)
	}
	cfg.BaseDir = baseDir
	cfg.ConfigPath = path
	if cfg.Xiaomi.TokenFile != "" && !filepath.IsAbs(cfg.Xiaomi.TokenFile) {
		cfg.Xiaomi.TokenFile = filepath.Join(baseDir, cfg.Xiaomi.TokenFile)
	}
	if cfg.Xiaomi.KeyFile != "" && !filepath.IsAbs(cfg.Xiaomi.KeyFile) {
		cfg.Xiaomi.KeyFile = filepath.Join(baseDir, cfg.Xiaomi.KeyFile)
	}
	if cfg.CookieFile != "" && !filepath.IsAbs(cfg.CookieFile) {
		cfg.CookieFile = filepath.Join(baseDir, cfg.CookieFile)
	}
	if cfg.CookieKeyFile != "" && !filepath.IsAbs(cfg.CookieKeyFile) {
		cfg.CookieKeyFile = filepath.Join(baseDir, cfg.CookieKeyFile)
	}

	return NormalizeSpeakers(cfg)
}

// CheckCloudReady 是**运行**前的检查（登录模式不走这里）：
// 云端音箱必须有可用的认证文件，否则直接报错让人去登录，
// 而不是像以前那样悄悄用密码重登、把账号撞进风控。
func CheckCloudReady(cfg Config) error {
	needCloud := false
	for _, sp := range cfg.Speakers {
		if sp.Control == "cloud" {
			needCloud = true
			break
		}
	}
	if !needCloud {
		return nil
	}
	// user / device_id 都可以从认证文件里读（QR 登录会自动生成 device_id），
	// 所以这里只检查"有没有登录过"这一件事。
	if _, err := os.Stat(cfg.Xiaomi.TokenFile); err != nil {
		return fmt.Errorf("小米账号未登录：先运行 xiaoai-mb -login xiaomi（认证文件 %s 不存在）", cfg.Xiaomi.TokenFile)
	}
	return nil
}
