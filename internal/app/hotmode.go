package app

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
)

// 无限心动（续杯）的节奏参数。
//
// 为什么要在“窗口最后一首刚起播”时重推：设备手里的是一份固定列表
// （`player_play_music` + `REPLACE_ALL`），服务端往队列尾部 Append 的歌它看不见。
// 设备的 `APPEND` 虽然能扩列表，但会**立刻跳到新追加的第一首**（实测），
// 也就是掐断当前曲。所以只能在自然边界上重推：让当前曲排第一，重启发生在 0 秒处。
//
// 任何一次切歌后的 0~5 秒都是安全重推点（重推是从 0 秒重启当前曲，
// 只有起播瞬间听不出来）——所以 restock 时机（任务 B6）放宽到这个窗口。
const (
	hotPushThrottle = 5 * time.Second  // 两次重推的最小间隔
	hotLastSongWin  = 5000             // “刚起播”的判定窗口（毫秒）
	hotFetchTimeout = 90 * time.Second // 一批推荐 + 直链校验的总预算
	hotPushTimeout  = 25 * time.Second
)

// hotBackoff 取歌失败（真实接口错误）的退避阶梯：30s → 2min → 10min。
// 旧版固定 30s + max_fail=3 会把“必然没有新歌”当成故障在 90 秒内静默退出，
// 新版「没有新歌」根本不计 fail，这个阶梯只处理真实网络/接口错误。
func hotBackoff(fails int) time.Duration {
	switch {
	case fails <= 1:
		return 30 * time.Second
	case fails == 2:
		return 2 * time.Minute
	default:
		return 10 * time.Minute
	}
}

// hotKeepRecent 是续杯滤重的“最近历史”范围：已播部分只回看这么多首
// （默认 max(window, 40)），而不是整个队列——旧版拿全队列比，69 首的队列
// 配上 20 首的候选必然全命中，然后被当成接口故障计数。
func (s *Session) hotKeepRecent() int {
	w := s.app.cfg.Window
	if w < 40 {
		w = 40
	}
	return w
}

// StartHotmode 把当前队列标记为无限心动。
//
// pid 是「我喜欢的音乐」歌单 id：网易云的心动接口要它当上下文。
func (s *Session) StartHotmode(pid string) {
	s.hotMu.Lock()
	defer s.hotMu.Unlock()
	s.hotOn = true
	s.hotPID = pid
	s.hotFails = 0
	s.hotPushed = 0
	s.hotStopReason = ""
	s.hotLastTry = time.Time{}
	s.hotGen++ // 让还在飞的取歌结果作废
}

// StopHotmode 退出无限心动。reason 会推给前端显示（“心动已停止：<原因>”），
// 不许静默消失。
func (s *Session) StopHotmode(reason string) {
	s.hotMu.Lock()
	defer s.hotMu.Unlock()
	s.hotOn = false
	s.hotStopReason = reason
	s.hotGen++
}

// HotmodeState 返回续杯状态（on / 连续失败次数 / 已续杯次数 / 停止原因）。
func (s *Session) HotmodeState() (on bool, fails, pushed int, stopReason string) {
	s.hotMu.Lock()
	defer s.hotMu.Unlock()
	return s.hotOn, s.hotFails, s.hotPushed, s.hotStopReason
}

// winLenSince 返回上次推窗后队列长出来多少首（restock 判定用）。
func (s *Session) winLenSince() int {
	s.hotMu.Lock()
	defer s.hotMu.Unlock()
	if s.winLen <= 0 {
		return 0
	}
	return s.q.Len() - s.winLen
}

// hotmodeTick 由 runScheduler 每 500ms 调一次：先补货，再在安全点重推。
func (s *Session) hotmodeTick(ctx context.Context) {
	if !s.windowMode {
		return
	}
	if on, _, _, _ := s.HotmodeState(); !on {
		return
	}
	// 续杯只对“顺序播放、列表循环”有意义；随机/单曲循环时窗口语义不同
	if s.q.Shuffle() || s.q.Loop() != queue.LoopList {
		return
	}
	v := s.q.View(0)
	if v.Current == nil {
		return
	}
	idx := s.q.Index()
	winFrom, pushedTo, advanced := s.windowBounds()
	if winFrom >= 0 && idx != winFrom && !advanced {
		s.markWinAdvanced()
		advanced = true
	}
	if !v.Playing {
		return
	}

	// 补货：队列里当前曲之后不够“一窗”就提前取下一批（离窗口结尾还很远）
	below := s.app.cfg.HotmodeExtendBelow
	remaining := s.q.Len() - idx - 1
	if below > 0 && remaining < below {
		s.fetchHotmode()
	}

	if act := hotmodeAction(idx, pushedTo, winFrom, advanced, v.PositionMs, below, remaining, s.winLenSince()); act != "" {
		s.extendWindow(ctx, act)
	}
}

// hotKeepBack 是心动模式下队列里保留的已播历史首数（与落盘裁剪的 ±100 先例一致）。
const hotKeepBack = 100

// trimHotHistory 裁掉已播历史并同步平移窗口边界。
//
// 队列只增不减（无限心动每轮 +上百首），不裁几个月就是几千首；但裁剪会让
// 所有队列下标前移，而 winFrom/pushedTo 正是用下标记的窗口边界——错一位，
// 续杯时机就全乱，所以必须在这里一起平移。
//
// 返回实际移除的条数。窗口起点被整段裁掉时置 -1（而不是钳成 0）：钳成 0 会
// 让「设备绕回窗口开头」的判定误报，禁用 wrap 比误报安全（last/restock 不受影响）。
func (s *Session) trimHotHistory(keepBack int) int {
	removed := s.q.TrimHistory(keepBack)
	if removed <= 0 {
		return 0
	}
	s.hotMu.Lock()
	if s.winFrom >= 0 {
		s.winFrom -= removed
		if s.winFrom < 0 {
			s.winFrom = -1 // 窗口开头已被裁掉，wrap 判定作废
		}
	}
	if s.pushedTo >= 0 {
		s.pushedTo -= removed
		if s.pushedTo < 0 {
			s.pushedTo = -1
		}
	}
	s.hotMu.Unlock()
	log.Printf("[%s] ♾ 心动：裁掉 %d 首已播历史，窗口边界平移到 winFrom=%d pushedTo=%d",
		s.speaker.Name, removed, s.winFrom, s.pushedTo)
	return removed
}

// windowBounds 读当前窗口在队列里的边界。这三个字段由 hotMu 保护：
// 写点是 pushWindow 与重启恢复，读点是续杯判定、设备同步和落盘快照。
func (s *Session) windowBounds() (from, to int, advanced bool) {
	s.hotMu.Lock()
	defer s.hotMu.Unlock()
	return s.winFrom, s.pushedTo, s.winAdvanced
}

// markWinAdvanced 记下“本窗已经离开过第一首”——只有这样，绕回开头那一刻才认得出是绕回。
func (s *Session) markWinAdvanced() {
	s.hotMu.Lock()
	s.winAdvanced = true
	s.hotMu.Unlock()
}

// hotmodeAction 判断这一拍该不该重推窗口。
//
// 返回 "last"（本窗最后一首刚起播）、"wrap"（兜底：设备已绕回窗口开头）或
// "restock"（起播窗口期 + 未播剩余不足补货阈值 + 上次推窗后队列有新歌），
// 空串表示不动。抽成纯函数是为了能单测。
//
// restock 解决手动 seek 到窗口中段后等不到 pushedTo 的问题：任何一次切歌后的
// 0~hotLastSongWin 都是安全重推点。winLenGrew（上次推窗后队列变长了）保证
// restock 只在真的有新内容可带时触发，不会在取歌失败期间反复重启同一首。
func hotmodeAction(idx, pushedTo, winFrom int, advanced bool, posMs, below, remaining, winLenDelta int) string {
	if pushedTo >= 0 && idx == pushedTo && posMs >= 0 && posMs < hotLastSongWin {
		return "last"
	}
	if winFrom >= 0 && idx == winFrom && advanced {
		return "wrap"
	}
	if below > 0 && remaining < below && winLenDelta > 0 && posMs >= 0 && posMs < hotLastSongWin {
		return "restock"
	}
	return ""
}

// extendWindow 重推整窗，把刚取到的歌带进设备列表。
//
// 只在“当前曲刚起播”或“绕回窗口开头”时调用，这样重推导致的“从 0 秒重来”听不出来。
func (s *Session) extendWindow(ctx context.Context, reason string) {
	if s.q.Len()-s.q.Index()-1 <= 0 {
		// 后面一首都没有，推了也只是原地打转，等取歌成功
		return
	}
	if time.Since(s.lastRepush) < hotPushThrottle {
		return
	}
	wp, ok := s.player.(player.WindowPlayer)
	if !ok {
		return
	}
	s.lastRepush = time.Now()

	c, cancel := context.WithTimeout(ctx, hotPushTimeout)
	defer cancel()
	if err := s.pushWindow(c, wp); err != nil {
		// 交给下一拍的兜底重试（设备绕回窗口开头时还有一次机会）
		log.Printf("[%s] ♾ 心动续杯重推失败（%s）：%v", s.speaker.Name, reason, err)
		return
	}
	s.hotMu.Lock()
	s.hotPushed++
	pushed := s.hotPushed
	s.hotMu.Unlock()
	log.Printf("[%s] ♾ 心动续杯（%s）：队列 %d 首，当前 [%d/%d]，累计续杯 %d 次",
		s.speaker.Name, reason, s.q.Len(), s.q.Index()+1, s.q.Len(), pushed)
}

// fetchHotmode 异步取下一批推荐并追加到队尾。同一时刻只允许一个在跑。
func (s *Session) fetchHotmode() {
	s.hotMu.Lock()
	if s.hotBusy || !s.hotOn || s.hotPID == "" {
		s.hotMu.Unlock()
		return
	}
	backoff := hotBackoff(s.hotFails)
	if time.Since(s.hotLastTry) < backoff {
		s.hotMu.Unlock()
		return
	}
	s.hotBusy = true
	s.hotLastTry = time.Now()
	gen, pid := s.hotGen, s.hotPID
	s.hotMu.Unlock()

	go func() {
		defer func() {
			s.hotMu.Lock()
			s.hotBusy = false
			s.hotMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), hotFetchTimeout)
		defer cancel()

		tracks, replay, err := s.nextHotmodeBatch(ctx, pid)
		if err != nil {
			s.hotmodeFailed(err)
			return
		}

		s.hotMu.Lock()
		stale := gen != s.hotGen || !s.hotOn
		if !stale {
			s.hotFails = 0
		}
		s.hotMu.Unlock()
		if stale {
			return // 期间换了歌单/退出了心动，这批不能污染队列
		}
		if s.q.Append(tracks) >= 0 {
			label := ""
			if replay {
				label = "（重播：推荐池暂时没有新歌）"
			}
			log.Printf("[%s] ♾ 心动续杯%s：追加 %d 首，队列 %d 首",
				s.speaker.Name, label, len(tracks), s.q.Len())
		}
		// 队列规模护栏：只保留当前曲之前 100 首（与落盘裁剪的 ±100 先例一致），
		// 否则每轮 +上百首几个月就是几千首。
		s.trimHotHistory(hotKeepBack)
	}()
}

// recentSeenIDs 续杯滤重用的“已占”集合：未播部分（当前曲及之后）+ 当前曲之前
// 最近 hotKeepRecent 首。不再拿整个队列历史比（那是“续杯自杀”事故的根因）。
func (s *Session) recentSeenIDs() map[int64]bool {
	tracks := s.q.Snapshot()
	idx := s.q.Index()
	keep := s.hotKeepRecent()
	from := idx - keep
	if from < 0 {
		from = 0
	}
	if idx < 0 || idx > len(tracks) {
		idx = len(tracks)
	}
	seen := make(map[int64]bool, idx-from+len(tracks)-idx)
	for i := from; i < len(tracks); i++ {
		if tracks[i].ID != 0 {
			seen[tracks[i].ID] = true
		}
	}
	return seen
}

// nextHotmodeBatch 随机挑种子取心动推荐，滤掉不可播与已占的歌。
//
// 兜底阶梯（用户选定）：换随机种子重试最多 3 次 → 仍无新歌 → 允许重复入队
// （replay=true，调用方会在日志/状态里说明是重播）。
// 「没有新歌」不算错误；只有真实接口错误（网络/非 200/解析失败）才返回 err。
func (s *Session) nextHotmodeBatch(ctx context.Context, pid string) (tracks []netease.Track, replay bool, err error) {
	if e := ctx.Err(); e != nil {
		return nil, false, e
	}
	seen := s.recentSeenIDs()
	var lastPlayable []netease.Track
	for attempt := 0; attempt < 3; attempt++ {
		if e := ctx.Err(); e != nil {
			return nil, false, e
		}
		seed, ok := s.randomSeed()
		if !ok {
			return nil, false, fmt.Errorf("队列里没有可当种子的歌")
		}
		recs, e := s.app.ne.Hotmode(seed, pid)
		if e != nil {
			return nil, false, e // 真实接口错误：交给 hotmodeFailed 计数
		}
		// 推荐里常混着 VIP/版权失效的歌，设备播到会 404，先批量验一遍
		recs = s.app.filterPlayable(recs, 0)
		lastPlayable = recs

		out := recs[:0]
		for _, t := range recs {
			if t.ID == 0 || seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			out = append(out, t)
		}
		if len(out) > 0 {
			return out, false, nil
		}
		// 这批全是已占的歌：换一个随机种子再试（服务端同种子两次交集只有
		// 81/119，换种子基本就是换一批）
	}
	// 3 个种子都没挖到新歌：允许重复入队（重播），绝不静默退出
	if len(lastPlayable) == 0 {
		return nil, false, fmt.Errorf("这批推荐没有可播放的歌")
	}
	return lastPlayable, true, nil
}

// randomSeed 从当前队列里随机挑一首（排除正在放的），当心动接口的种子。
func (s *Session) randomSeed() (int64, bool) {
	tracks := s.q.Snapshot()
	if len(tracks) == 0 {
		return 0, false
	}
	cur, curIdx, ok := s.q.Current()
	pool := make([]int64, 0, len(tracks))
	for i, t := range tracks {
		if ok && i == curIdx {
			continue
		}
		if t.ID != 0 {
			pool = append(pool, t.ID)
		}
	}
	if len(pool) == 0 {
		if !ok || cur.ID == 0 {
			return 0, false
		}
		return cur.ID, true
	}
	return pool[rand.Intn(len(pool))], true
}

// hotmodeFailed 记一次**真实接口错误**（网络/非 200/解析失败）。
// 「推荐全是已播过的歌」不算错误，走 nextHotmodeBatch 的重播兜底，不会到这里。
// 达到上限时不再静默退出：保留 stop_reason 推给 UI。
func (s *Session) hotmodeFailed(err error) {
	max := s.app.cfg.HotmodeExtendMaxFail
	if max <= 0 {
		max = 3
	}
	s.hotMu.Lock()
	s.hotFails++
	fails := s.hotFails
	giveUp := fails >= max
	if giveUp {
		s.hotOn = false
		s.hotStopReason = fmt.Sprintf("取歌连续失败 %d 次：%v", fails, err)
		s.hotGen++
	}
	s.hotMu.Unlock()

	if giveUp {
		log.Printf("[%s] ♾ 心动续杯连续失败 %d 次（%v），停止续杯（普通列表继续播）", s.speaker.Name, fails, err)
		return
	}
	log.Printf("[%s] ♾ 心动续杯取歌失败（第 %d/%d 次，%s 后重试）：%v",
		s.speaker.Name, fails, max, hotBackoff(fails), err)
}
