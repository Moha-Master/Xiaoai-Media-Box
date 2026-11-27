package app

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
)

// 回读节奏：稳定态 + 不稳定态两档。
//
// 依据（2026-11-20 实测原厂行为）：
//   - 原厂设备不会主动上报任何播放事件（ubus listen 全程 0 条；按键时公网零流量）；
//   - 云端是拉方：米家 app 在前台时，云每 5 秒查一次设备，app 一退到后台就不查了；
//     另有 miio_helper 每 60 秒一次的属性快照上报（audio_meta / position / track_list）。
//
// 所以我们也没法“事件驱动”，只能把轮询做成按需：
//   - 稳态：poll_steady（云端默认 60s，照顾 MiNA 可能的反滥用）；
//   - 不稳定态：poll_burst × poll_rounds（默认 5s×3 轮）——凡是“当前状态不可靠”
//     的时刻都切到这一档：我们刚发过指令、设备换了歌、播放态翻转、位置对不上、
//     探测从失败恢复过来。跑完轮数自动回落到稳态。

type pollPolicy struct {
	steady time.Duration // 稳态间隔
	burst  time.Duration // 不稳定态间隔
	rounds int           // 不稳定态轮数
	drift  time.Duration // 位置偏差超过这个值就认为状态不可靠
}

func policyFromConfig(sp config.SpeakerConfig) pollPolicy {
	p := pollPolicy{
		steady: time.Duration(sp.StatusPoll) * time.Second,
		burst:  time.Duration(sp.PollBurst) * time.Second,
		rounds: sp.PollRounds,
		drift:  time.Duration(sp.PollDriftMs) * time.Millisecond,
	}
	if p.burst <= 0 {
		p.burst = 5 * time.Second
	}
	if p.rounds <= 0 {
		p.rounds = 3
	}
	if p.drift <= 0 {
		// ssh 一次 ~50ms，4 秒偏差说明确实有人在动它；
		// 云端 MiNA 从下发到真正开播有 2~5 秒网络+起播延迟，阈值要放宽，
		// 否则起播那阵子会一直误判成“不可靠”，burst 停不下来。
		if sp.Control == "cloud" {
			p.drift = 8 * time.Second
		} else {
			p.drift = 4 * time.Second
		}
	}
	// 加密比稳态还慢就没意义了
	if p.steady > 0 && p.burst > p.steady {
		p.burst = p.steady
	}
	return p
}

// pollState 是节奏的运行时状态（burst 余量、最近一次判定原因）。
type pollState struct {
	mu      sync.Mutex
	left    int    // 还剩几轮不稳定态
	reason  string // 最近一次进入不稳定态的原因
	fail    bool   // 上一次探测是否失败（用于“失败→恢复”边沿触发）
	nextDue time.Time
}

func (s *Session) initPoll() {
	s.pol = policyFromConfig(s.speaker)
	s.wakePoll = make(chan struct{}, 1)
	s.poll.nextDue = time.Now().Add(s.pol.nextDelay(0))
}

// nextDelay 算下一次探测要等多久，并消耗一轮不稳定态额度。
func (p pollPolicy) nextDelay(left int) time.Duration {
	if left > 0 {
		return p.burst
	}
	return p.steady
}

// markUnstable 把状态标为“不可靠”，接下来几轮用短间隔密集回读。
//
// 重复触发只重置余量、不叠加，所以连续操作期间最多也就是 burst×rounds 的节奏。
func (s *Session) markUnstable(reason string) {
	if s.pol.steady <= 0 || s.wakePoll == nil {
		return // 关掉回读（noop / status_poll=-1）时不需要
	}
	s.poll.mu.Lock()
	fresh := s.poll.left == 0
	s.poll.left = s.pol.rounds
	s.poll.reason = reason
	if s.poll.nextDue.Before(time.Now()) {
		s.poll.nextDue = time.Now().Add(s.pol.burst)
	}
	s.poll.mu.Unlock()
	if fresh {
		log.Printf("[%s] ⚡ 状态不可靠（%s）→ %ds×%d 加密回读",
			s.speaker.Name, reason, int(s.pol.burst/time.Second), s.pol.rounds)
	}
	select {
	case s.wakePoll <- struct{}{}:
	default:
	}
}

// consumeDelay 取走本轮额度并返回该等待多久。
func (s *Session) consumeDelay() time.Duration {
	s.poll.mu.Lock()
	defer s.poll.mu.Unlock()
	d := s.pol.nextDelay(s.poll.left)
	if s.poll.left > 0 {
		s.poll.left--
		if s.poll.left == 0 {
			s.poll.reason = "稳态"
		}
	}
	s.poll.nextDue = time.Now().Add(d)
	return d
}

// pollDebug 给 /api/state 用，方便实机观察节奏到底处在哪个档。
func (s *Session) pollDebug() map[string]any {
	if s.speaker.StatusMode == "push" {
		_, at, ok, _ := s.device.get()
		age := -1
		if ok {
			age = int(time.Since(at).Seconds())
		}
		off := s.unreachable.Load()
		t := s.pushTiming()
		sec := func(d time.Duration) int { return int(d / time.Second) }
		out := map[string]any{
			"mode":            "push",
			"healthy":         !off,
			"unreachable":     off,
			"last_age_sec":    age,
			"sample_sec":      sec(t.sample),
			"hb_play_sec":     sec(t.hbPlay),
			"hb_idle_sec":     sec(t.hbIdle),
			"seek_tol_ms":     int(t.seekTol / time.Millisecond),
			"offline_after":   sec(t.offline),
			"fresh_ttl_sec":   sec(t.offline), // 推送模式合成一个阈值，见 deviceFresh
			"daemon_interval": s.speaker.ReportInterval,
			"daemon_hb_play":  s.speaker.ReportHBPlay,
			"daemon_hb_idle":  s.speaker.ReportHBIdle,
		}
		if off {
			out["frozen_position_ms"] = int(s.frozenPosMs.Load())
		}
		return out
	}
	if s.pol.steady <= 0 || s.wakePoll == nil { // noop / status_poll=-1
		return map[string]any{"mode": "off", "steady_sec": 0, "burst_sec": 0, "left": 0}
	}
	s.poll.mu.Lock()
	defer s.poll.mu.Unlock()
	mode := "steady"
	if s.poll.left > 0 {
		mode = "burst"
	}
	return map[string]any{
		"mode":        mode,
		"steady_sec":  int(s.pol.steady / time.Second),
		"burst_sec":   int(s.pol.burst / time.Second),
		"left":        s.poll.left,
		"reason":      s.poll.reason,
		"next_in_sec": int(time.Until(s.poll.nextDue) / time.Second),
	}
}

// ---------- 推送模式（status_mode=push）的可达性 ----------
//
// 推送模式没有探针，所以「设备还在不在」只能反过来判断：多久没收到上报。
// 阈值不能写死，它由 daemon 的心跳节奏推出来（两边必须一致，否则 daemon 改了
// 心跳而服务端还在按老阈值判，会误判离线）：
//
//	offline = max(3×hb_play, 2×hb_idle)   默认 max(45,60)=60s
//
// 数据新鲜度与离线阈值这里**合成一个**：daemon 只在「有变化」或「心跳到点」时发包，
// 所以"多久没收到"本身就是唯一信号，没必要再分两级（分两级会让 device 块消失的
// 时刻和判离线的时刻不一致，排查时很别扭）。
//
// daemon 上报失败不会推进基线，下一拍（sample 间隔）就重试，所以静默超过 offline
// 基本只剩「daemon 真死了 / 音箱关机」两种情况。
type pushTiming struct {
	sample  time.Duration // daemon 本地采样间隔
	hbPlay  time.Duration // 播放中心跳
	hbIdle  time.Duration // 暂停/空闲心跳
	seekTol time.Duration // 位置异常容差（daemon 用它判 leap，服务端用它复核 seek）
	offline time.Duration // 静默多久判离线
}

func pushTimingFromConfig(sp config.SpeakerConfig) pushTiming {
	sec := func(v, def int) time.Duration {
		if v <= 0 {
			v = def
		}
		return time.Duration(v) * time.Second
	}
	t := pushTiming{
		sample:  sec(sp.ReportInterval, 5),
		hbPlay:  sec(sp.ReportHBPlay, 15),
		hbIdle:  sec(sp.ReportHBIdle, 30),
		seekTol: time.Duration(sp.ReportSeekToleranceMs) * time.Millisecond,
	}
	if t.seekTol <= 0 {
		t.seekTol = 8 * time.Second
	}
	t.offline = 3 * t.hbPlay
	if twice := 2 * t.hbIdle; twice > t.offline {
		t.offline = twice
	}
	return t
}

// pushTiming 取本 session 的推送节奏（非 push 模式也返回默认值，调用方自己判模式）。
//
// 兜底重算而不是直接返回字段：Session 有若干构造路径（测试里的字面量、
// 以后可能的热更新）不会走 newSession，零值会让 offline 阈值变成 0，
// 于是 deviceFresh 永远 false、设备一上线就被判离线。
func (s *Session) pushTiming() pushTiming {
	if s.pushTim.offline > 0 {
		return s.pushTim
	}
	return pushTimingFromConfig(s.speaker)
}

// pushMode 表示这台音箱的状态来源是设备主动上报。
func (s *Session) pushMode() bool { return s.speaker.StatusMode == "push" }

// markDeviceData 记录「刚刚拿到一份设备数据」，并把可达性恢复成在线。
// 拉取路径（pollOnce）和推送路径（hDeviceState）都走这里。
func (s *Session) markDeviceData() {
	s.lastPushAt.Store(time.Now().Unix())
	if s.unreachable.Swap(false) {
		log.Printf("[%s] 📶 设备恢复上报，解除冻结", s.speaker.Name)
	}
}

// checkPushLiveness 由调度循环每 tick 调一次：推送静默超时就把播放器整体冻结。
//
// 为什么必须冻：没有新数据时 syncWithDevice 不会运行，而整窗模式下
// advanceByTimer 也直接 return（推进靠设备自己），于是队列会永远停在
// 「正在播放第 N 首」，而 Queue.PositionMs() 是无上限插值——进度条和歌词时间轴
// 会一路涨过曲尾（4:27 的歌显示到 7:12），既不报错也不判离线。
func (s *Session) checkPushLiveness() {
	if !s.pushMode() {
		return
	}
	last := s.lastPushAt.Load()
	if last == 0 {
		return // 还没收到过第一帧：启动阶段不算离线
	}
	t := s.pushTiming()
	silent := time.Since(time.Unix(last, 0))
	if silent <= t.offline {
		return
	}
	// Swap 返回**旧值**：旧值 false 才是「在线 → 离线」这个边沿，只在这个时刻冻结
	if !s.unreachable.Swap(true) {
		s.frozenPosMs.Store(int64(s.q.View(0).PositionMs))
		log.Printf("[%s] 📴 设备上报静默 %s（阈值 %s）→ 播放器冻结，状态报未知",
			s.speaker.Name, silent.Round(time.Second), t.offline)
	}
}

// unreachableView 是给对外视图用的：返回（是否离线, 冻结位置）。
func (s *Session) unreachableView() (bool, int) {
	if !s.unreachable.Load() {
		return false, 0
	}
	return true, int(s.frozenPosMs.Load())
}

func (s *Session) runPoll(ctx context.Context) {
	if s.probe == nil || s.pol.steady <= 0 {
		return
	}
	s.pollOnce(ctx)
	d := s.consumeDelay()
	for {
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wakePoll:
			timer.Stop()
			// 有人把状态标脏了：重新按新的节奏等（操作本身已各自探过一次）
			d = s.consumeDelay()
		case <-timer.C:
			s.pollOnce(ctx)
			d = s.consumeDelay()
		}
	}
}

// noteProbeResult 让“失败 → 恢复”这个边沿也进入不稳定态，
// 这样设备掉线又上线后不用等一个完整稳态周期才纠正。
func (s *Session) noteProbeResult(err error) {
	s.poll.mu.Lock()
	wasFail := s.poll.fail
	s.poll.fail = err != nil
	s.poll.mu.Unlock()
	// 只在“失败 → 成功”这个边沿上加密：连续失败时不能一直高频撞云端
	if err == nil && wasFail {
		s.markUnstable("探测从失败中恢复")
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
