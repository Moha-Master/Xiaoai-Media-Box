package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// 白名单。写配置（网页/手工）之前都要过一遍校验，
// 否则一个拼错的 stream_mode 会悄悄退化成默认行为，比报错更难查。
var (
	// 取值来自 netease-music 的 service.SongQualityLevel。
	validQuality = map[string]bool{
		"standard": true, "higher": true, "exhigh": true, "lossless": true, "hires": true,
		"jyeffect": true, "sky": true, "jymaster": true,
	}
	validLoop       = map[string]bool{"none": true, "list": true, "single": true, "shuffle": true}
	validStreamMode = map[string]bool{"redirect": true, "proxy": true}
	validControl    = map[string]bool{"ssh": true, "cloud": true, "noop": true}
	validStatusMode = map[string]bool{"cloud": true, "push": true, "ssh": true, "none": true}
)

const (
	maxWindow    = 200 // 一窗 200 首的 payload 设备 ssh 命令行装不下，也没人真听那么多
	maxSpeakerID = 64
)

// Validate 检查一份**已经 Normalize 过**的配置。
//
// 一次报全部问题而不是遇到第一个就返回：配置页要一次性把这些显示出来，
// 让人改一轮就过，不用“报错—改—再报错”来回五趟。
func (c Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if host, port, err := net.SplitHostPort(c.Listen); err != nil {
		add("listen=%q 不是 host:port（%v）", c.Listen, err)
	} else if port != "" {
		var p int
		if _, err := fmt.Sscanf(port, "%d", &p); err != nil || p < 1 || p > 65535 {
			add("listen=%q 的端口不合法", host+":"+port)
		}
	}
	if u, err := url.Parse(c.PublicBase); err != nil || u.Host == "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		add("public_base=%q 必须是 http(s)://主机[:端口] 形式", c.PublicBase)
	}
	if !validQuality[c.Quality] {
		add("quality=%q 不支持（standard / higher / exhigh / lossless / hires / jyeffect / sky / jymaster）", c.Quality)
	}
	if !validLoop[c.Loop] {
		add("loop=%q 不支持（none / list / single / shuffle）", c.Loop)
	}
	if !validStreamMode[c.StreamMode] {
		add("stream_mode=%q 不支持（redirect / proxy）", c.StreamMode)
	}
	if c.Window < 1 || c.Window > maxWindow {
		add("window=%d 应在 1-%d 之间", c.Window, maxWindow)
	}
	if c.HotmodeExtendBelow < 0 || c.HotmodeExtendBelow > 1000 {
		add("hotmode_extend_below=%d 应在 0-1000 之间（0=跟随 window）", c.HotmodeExtendBelow)
	}
	if c.HotmodeExtendMaxFail < 1 || c.HotmodeExtendMaxFail > 20 {
		add("hotmode_extend_max_fail=%d 应在 1-20 之间", c.HotmodeExtendMaxFail)
	}
	if strings.TrimSpace(c.CookieFile) == "" {
		problems = append(problems, "cookie_file 不能为空")
	}

	seen := map[string]int{}
	for i, sp := range c.Speakers {
		where := fmt.Sprintf("speakers[%d]", i)
		if sp.ID == "" {
			add("%s 缺少 id", where)
		} else {
			if prev, dup := seen[sp.ID]; dup {
				// id 是 session 的 key、快照文件名、HA entity_id 的来源，重复必出事
				add("%s 的 id=%q 与 speakers[%d] 重复", where, sp.ID, prev)
			}
			seen[sp.ID] = i
			if len(sp.ID) > maxSpeakerID {
				add("%s 的 id=%q 太长（超过 %d 个字符）", where, sp.ID, maxSpeakerID)
			}
			if strings.ContainsAny(sp.ID, `/\ `+"\t\n") || strings.HasPrefix(sp.ID, ".") {
				// 这些字符会污染快照文件名（state/queue-<id>.json）和 HA 的 entity_id
				add("%s 的 id=%q 不能包含斜杠、空白或以点开头", where, sp.ID)
			}
		}
		if !validControl[sp.Control] {
			add("%s（%s）的 control=%q 不支持（cloud / ssh / noop）", where, sp.Name, sp.Control)
		}
		if sp.StatusMode != "" && !validStatusMode[sp.StatusMode] {
			add("%s（%s）的 status_mode=%q 不支持（cloud / push / ssh / none）", where, sp.Name, sp.StatusMode)
		}
		if sp.Control == "ssh" && strings.TrimSpace(sp.SSHHost) == "" {
			add("%s（%s）选择 ssh 但缺少 ssh_host", where, sp.Name)
		}
		if sp.StatusPoll < -1 {
			add("%s（%s）的 status_poll=%d 不合法（-1 关闭，0 按默认，其余为秒）", where, sp.Name, sp.StatusPoll)
		}
		// 推送节奏四项是「服务端判定用的期望值」，负数没有意义；上限是给 daemon 留的
		// 常识边界（采样再快也只是白耗设备电，心跳再长就等于没有存活依据）
		for _, f := range []struct {
			name string
			v    int
			max  int
		}{
			{"report_interval", sp.ReportInterval, 60},
			{"report_hb_play", sp.ReportHBPlay, 300},
			{"report_hb_idle", sp.ReportHBIdle, 300},
			{"report_seek_tol_ms", sp.ReportSeekToleranceMs, 60000},
		} {
			if f.v < 0 || f.v > f.max {
				add("%s（%s）的 %s=%d 不合法（0=按默认，上限 %d）", where, sp.Name, f.name, f.v, f.max)
			}
		}
	}
	if len(c.Speakers) == 0 {
		problems = append(problems, "至少要有音箱（speakers[] 为空）")
	}

	if len(problems) > 0 {
		return fmt.Errorf("配置不合法：%s", strings.Join(problems, "；"))
	}
	return nil
}
