package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/app"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/auth"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/qr"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/xiaomi"
)

// defaultWorkDir 返回默认工作目录：~/.config/xiaoai-mb/。
// 配置文件和认证文件都放在这里，进程不再依赖启动时的 cwd。
func defaultWorkDir() string {
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return filepath.Join(d, "xiaoai-mb")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "xiaoai-mb")
	}
	return ".xiaoai-mb"
}

// expandHome 展开开头的 ~，方便命令行传 -dir ~/somewhere。
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// discoverConfig 在指定工作目录找配置文件，优先 xiaoai-mb.json，其次 config.json。
func discoverConfig(dir string) string {
	for _, name := range []string{"xiaoai-mb.json", "config.json"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// runLogin 跑一遍扫码登录流程，成功后 cookie 落盘（MUSIC_U 等）。
func runLogin(ne *netease.Netease, jar *auth.Jar) error {
	for {
		key, url, err := ne.QRKey()
		if err != nil {
			return err
		}
		fmt.Println("\n用网易云音乐 App 扫码登录：")
		if err := qr.Print(url); err != nil {
			fmt.Println("（二维码渲染失败，请手动打开：", url, "）")
		}
		fmt.Println("链接：", url)

		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			code, msg, cookie, err := ne.QRCheck(key)
			if err != nil {
				log.Printf("轮询失败：%v", err)
				continue
			}
			switch code {
			case 800:
				fmt.Println("二维码已过期，重新申请…")
				deadline = time.Time{} // 跳出内层，重新申请
			case 801:
				// 等待扫码，静默
			case 802:
				fmt.Println("已扫码，请在手机上确认")
			case 803:
				if cookie != "" {
					jar.Inject(cookie, "music.163.com")
				}
				uid, nick, err := ne.Account()
				if err != nil {
					return fmt.Errorf("登录回调成功但账号校验失败：%w", err)
				}
				fmt.Printf("\n登录成功：%s (uid=%d)\n", nick, uid)
				return nil
			default:
				fmt.Printf("未知状态 code=%d msg=%s\n", code, msg)
			}
			if deadline.IsZero() {
				break
			}
		}
	}
}

// warnUnreachable 提醒最常见的“设备播不了”配置：服务只监听回环，或下发给设备的
// public_base 是 127.0.0.1/localhost。音箱拿到这种 URL 只能连它自己，必然报
// “播放失败，请检查网络”。
func warnUnreachable(cfg config.Config) {
	if u, err := url.Parse(cfg.PublicBase); err == nil {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if (ip != nil && ip.IsLoopback()) || strings.EqualFold(host, "localhost") {
			log.Printf("⚠️  public_base=%s 是回环地址，音箱无法访问；请改成这台机器的局域网地址，例如 http://192.168.1.10:8088", cfg.PublicBase)
		}
	}
	if h, _, err := net.SplitHostPort(cfg.Listen); err == nil {
		ip := net.ParseIP(h)
		if (ip != nil && ip.IsLoopback()) || strings.EqualFold(h, "localhost") {
			log.Printf("⚠️  listen=%s 只监听回环地址，音箱无法连入；改成 0.0.0.0:8088 或局域网 IP", cfg.Listen)
		}
	}
}

func main() {
	var (
		dirFlag = flag.String("dir", "", "工作目录（配置与认证文件），默认 ~/.config/xiaoai-mb/")
		cfgPath = flag.String("config", "", "配置文件路径（默认在工作目录找 xiaoai-mb.json / config.json）")
		listen  = flag.String("listen", "", "覆盖监听地址，如 0.0.0.0:8088")
		player  = flag.String("player", "", "覆盖播放器类型：noop | ubus-ssh")
		base    = flag.String("public-base", "", "覆盖设备可达的本服务地址")
		quality = flag.String("quality", "", "覆盖音质：standard|higher|exhigh|lossless|hires")
		doLogin = flag.String("login", "", "交互式登录后退出：ncm | xiaomi")
		authChk = flag.Bool("auth-check", false, "检查小米认证文件：强制续期一次并验证，然后退出")
	)
	flag.Parse()

	workDir := expandHome(*dirFlag)
	if workDir == "" {
		workDir = defaultWorkDir()
	}
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		log.Fatalf("解析工作目录失败：%v", err)
	}
	// 按需自动创建工作目录：配置和认证文件都落在这里。
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		log.Fatalf("创建工作目录 %s 失败：%v", workDir, err)
	}

	cfgFile := *cfgPath
	if cfgFile == "" {
		cfgFile = discoverConfig(workDir)
		if cfgFile == "" {
			cfgFile = filepath.Join(workDir, "config.json")
			log.Printf("工作目录：%s（配置文件不存在，使用默认配置）", workDir)
		} else {
			log.Printf("工作目录：%s  配置文件：%s", workDir, cfgFile)
		}
	} else {
		if !filepath.IsAbs(cfgFile) {
			cfgFile = filepath.Join(workDir, cfgFile)
		}
		log.Printf("工作目录：%s  配置文件：%s", workDir, cfgFile)
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		log.Fatalf("读取配置失败：%v", err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *player != "" {
		if len(cfg.Speakers) > 0 {
			sp := &cfg.Speakers[0]
			switch *player {
			case "ubus-ssh":
				sp.Control = "ssh"
			case "mina":
				sp.Control = "cloud"
			default:
				sp.Control = "noop"
			}
			// 清掉按旧 control 推导出来的默认值，让 normalize 重新算
			sp.Media = ""
			sp.StatusPoll = 0
			if err := config.NormalizeSpeakers(&cfg); err != nil {
				log.Fatalf("应用 -player 覆盖失败：%v", err)
			}
		} else {
			if cfg.Player == nil {
				cfg.Player = &config.PlayerConfig{}
			}
			cfg.Player.Type = *player
		}
	}
	if *base != "" {
		cfg.PublicBase = *base
	}
	if *quality != "" {
		cfg.Quality = *quality
	}
	warnUnreachable(cfg)

	jar := auth.NewJar(cfg.CookieFile, cfg.CookieKeyFile)
	ne := netease.Init(jar)
	ne.Quality = cfg.Quality

	if *doLogin != "" {
		switch strings.ToLower(*doLogin) {
		case "ncm", "netease", "wangyiyun":
			if err := auth.CheckWritable(cfg.CookieFile, cfg.CookieKeyFile); err != nil {
				log.Fatal(err)
			}
			if err := runLogin(ne, jar); err != nil {
				log.Fatalf("网易云登录失败：%v", err)
			}
		case "xiaomi", "mi":
			if err := xiaomi.Login(context.Background(), cfg.Xiaomi); err != nil {
				log.Fatalf("小米登录失败：%v", err)
			}
		default:
			log.Fatalf("未知的登录目标 %q（可选：ncm | xiaomi）", *doLogin)
		}
		return
	}

	if *authChk {
		if err := xiaomi.CheckAuth(context.Background(), cfg.Xiaomi); err != nil {
			log.Fatalf("认证检查失败：%v", err)
		}
		return
	}

	if err := config.CheckCloudReady(cfg); err != nil {
		log.Fatal(err)
	}
	daemon, err := app.NewApp(cfg, jar, ne)
	if err != nil {
		log.Fatalf("初始化音箱失败：%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	daemon.InstallLogRing(500)
	daemon.SetShutdownHook(cancel)
	daemon.Start(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           daemon.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Println("收到退出信号，正在关闭…")
		cancel()
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("xiaoai-mb 启动：%s  音质=%s  推流=%s  音箱=%d 台",
		cfg.Listen, cfg.Quality, cfg.StreamMode, daemon.SessionCount())
	for _, line := range daemon.SessionDescriptions() {
		log.Printf("  - %s", line)
	}
	log.Printf("设备侧地址模板：%s/s/<songId>（现取现跳，绕开直链 20 分钟过期）", strings.TrimRight(cfg.PublicBase, "/"))
	if uid, nick, err := ne.Account(); err == nil {
		log.Printf("网易云账号：%s (uid=%d)", nick, uid)
	} else if _, statErr := os.Stat(cfg.CookieFile); statErr != nil {
		log.Printf("网易云未登录（%s 不存在）—— 先跑 `xiaoai-mb -login ncm`", cfg.CookieFile)
	} else {
		log.Printf("网易云登录已失效（%v）—— 重新扫码：`xiaoai-mb -login ncm`", err)
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("HTTP 服务退出：%v", err)
	}
	log.Println("已退出")
}
