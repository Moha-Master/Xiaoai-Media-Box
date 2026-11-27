package app

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Moha-Master/Xiaoai-Media-Box/internal/config"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/netease"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/player"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/queue"
	"github.com/Moha-Master/Xiaoai-Media-Box/internal/xiaomi"
)

// Session 是一台音箱的完整运行态：独立队列、播放器、探针、音量与推进器。
// App 只保留全局共享的东西（netease/cookie、直链缓存、红心/歌词缓存、SSE hub）。
type Session struct {
	app     *App
	speaker config.SpeakerConfig
	player  player.Player
	q       *queue.Queue

	// 设备状态回读
	probe           player.StatusProbe
	probeEvery      time.Duration
	device          deviceTracker
	deviceErrLogged string
	endedID         atomic.Int64
	expectID        int64
	expectUntil     time.Time
	lastDevGen      uint64 // 已经同步过的设备数据代次

	// 主动探测：操作后 / 预计播完时立刻拉一次
	probeMu      sync.Mutex
	probePending atomic.Bool
	endProbeID   int64
	endProbeAt   time.Time

	// 设备可达性（status_mode=push 专用，见 poll.go 的 push 部分）。
	// 推送模式没有探针，"设备还在不在"只能靠"多久没收到上报"推断；
	// unreachable 期间整个播放器冻结：进度停在 frozenPosMs，对外一律报"未知"。
	pushTim     pushTiming   // 由配置推出来的推送节奏与静默阈值
	lastPushAt  atomic.Int64 // 上次拿到设备数据的 Unix 秒（拉取 / 推送都记）
	unreachable atomic.Bool
	frozenPosMs atomic.Int64

	// seek 的异步复核：探针不可用（push）时没法同步回读，改成
	// "下发即返回 + 下一帧真值上校验 + 最多重试一次"，见 verifyPendingSeek。
	seekMu   sync.Mutex
	pendSeek *pendingSeek

	// 回读节奏（稳定态 / 不稳定态，见 poll.go）
	pol      pollPolicy
	poll     pollState
	wakePoll chan struct{}

	// SSE 推送用的上一次变化签名
	lastSig   string
	lastLyric string

	// 整窗模式（窗口边界字段挪到 hotMu 那组里，见下面）
	windowMode bool

	// 无限心动（续杯）状态，见 hotmode.go
	// hotMu 同时护住下面这组窗口字段（winFrom/pushedTo/winAdvanced/winLen/stopReason），
	// 落盘要一次读全它们，见 state.go。
	hotMu         sync.Mutex
	hotOn         bool
	hotPID        string
	hotGen        int
	hotFails      int
	hotPushed     int
	hotStopReason string
	hotBusy       bool
	hotLastTry    time.Time
	winFrom       int  // 当前窗口在队列里的起始下标（检测“绕回窗口开头”用）
	pushedTo      int  // 当前窗口最后一首在队列里的下标（无限心动在这里续杯）
	winAdvanced   bool // 本窗是否已经离开过第一首
	winLen        int  // 上次推窗的曲目数（restock 判定：推窗后队列有没有长出来）

	// 播放队列持久化，见 state.go
	// stMu 护住下面三个字段：写点在调度器（落盘），读点在 HTTP/SSE（persistInfo）。
	stMu       sync.Mutex
	stSig      string    // 上次落盘内容的签名，用来判断“值不值得再写一次”
	stSavedAt  time.Time // 上次真正写盘的时间
	stHadQueue bool      // 是否曾经有过队列（避免给闲置音箱造空快照文件）
	lastRepush time.Time

	// 队列编辑（删除/重排）后的待同步标记。
	// 暂停时改队列不能立刻推窗（推窗会出声），所以记下位置，等 resume 时
	// 一次性重推窗口并跳回原进度，见 queue_edit.go。
	qEditMu  sync.Mutex
	pendEdit bool
	pendPos  int

	// 音量
	volMu    sync.Mutex
	volume   int
	muted    bool
	mutePrev int

	// 闹钟音量：独立于播放音量，走云端 MiNA 的 alarm.* ubus 方法
	alarm      xiaomi.AlarmVolumeController
	alarmMu    sync.Mutex
	alarmVol   int
	alarmKnown bool
	alarmAt    time.Time
	alarmErr   string
}

func newSession(a *App, sp config.SpeakerConfig) (*Session, error) {
	p, err := newPlayer(sp, a.cfg.Xiaomi)
	if err != nil {
		return nil, err
	}
	loop, _ := queue.ParseLoop(a.cfg.Loop)
	s := &Session{
		app:     a,
		speaker: sp,
		player:  p,
		q:       queue.NewQueue(loop),
		volume:  30,
	}
	s.q.SetShuffle(queue.WantsShuffle(a.cfg.Loop))
	s.pushTim = pushTimingFromConfig(sp)
	if sp.StatusMode == "push" {
		// 推送模式：不设 probe（永不发起拉取），新鲜度用静默阈值表达，见 deviceFresh
		s.probeEvery = s.pushTim.offline
	} else if probe, ok := p.(player.StatusProbe); ok && sp.StatusPoll >= 0 {
		s.initPoll()
		s.probe = probe
		if sp.StatusPoll > 0 {
			s.probeEvery = time.Duration(sp.StatusPoll) * time.Second
		} else {
			s.probeEvery = 5 * time.Second // 0 按 5 秒；要关掉写 -1
		}
	}
	if _, ok := p.(player.WindowPlayer); ok && (sp.UseWindow == nil || *sp.UseWindow) {
		s.windowMode = true
	}
	s.pushedTo = -1
	s.winFrom = -1
	// 闹钟音量始终走云端（remote/ubus），与 control=ssh/cloud 无关：
	// 这样不依赖 SSH，原厂机和定制机行为一致。
	if sp.DeviceID != "" && a.cloud != nil {
		s.alarm = xiaomi.NewAlarm(a.cloud, sp.DeviceID)
	}
	// 上次的队列/进度/心动/音量从这里回来（此时还没有任何 goroutine，改状态是安全的）
	s.loadState()
	return s, nil
}

// ---------- 播放控制 ----------

// startCurrent 解析当前曲并把 URL 推给设备。解析失败的曲目直接跳过。
func (s *Session) startCurrent(ctx context.Context) error {
	s.clearPendingEdit()
	if s.windowMode {
		if wp, ok := s.player.(player.WindowPlayer); ok {
			return s.pushWindow(ctx, wp)
		}
	}
	for i := 0; i < 8; i++ {
		t, idx, ok := s.q.Current()
		if !ok {
			return fmt.Errorf("队列为空")
		}
		if _, err := s.app.resolve(t.ID); err != nil {
			log.Printf("[%s] 跳过 [%d/%d] %s：%v", s.speaker.Name, idx+1, s.q.Len(), t.Label(), err)
			if _, _, ok := s.q.Move(1, false); !ok {
				return err
			}
			continue
		}
		if err := s.player.Play(ctx, s.app.deviceURL(t.ID), t); err != nil {
			return err
		}
		s.q.MarkPlaying()
		s.endedID.Store(0)
		s.expectID = t.ID
		s.expectUntil = time.Now().Add(6 * time.Second)
		log.Printf("[%s] ▶ [%d/%d] %s", s.speaker.Name, idx+1, s.q.Len(), t.Label())
		s.probeSoon("下发单曲")
		return nil
	}
	return fmt.Errorf("连续 8 首都无法播放，停止")
}

func (s *Session) next(ctx context.Context) error {
	s.clearPendingEdit()
	if s.windowMode {
		if s.q.Shuffle() || s.q.Loop() != queue.LoopList {
			// 随机 / 单曲 / 不循环：设备窗口里的“下一首”不一定就是队列的下一首，
			// 本地移动后重推整窗最可靠。
			if _, _, ok := s.q.Move(1, false); !ok {
				return s.stopAtEnd()
			}
			return s.startCurrent(ctx)
		}
		if err := s.player.Next(ctx); err != nil {
			return err
		}
		if _, _, ok := s.q.Move(1, false); !ok {
			return s.stopAtEnd()
		}
		s.q.MarkPlaying()
		return nil
	}
	if _, _, ok := s.q.Move(1, false); !ok {
		return s.stopAtEnd()
	}
	return s.startCurrent(ctx)
}

func (s *Session) prev(ctx context.Context) error {
	s.clearPendingEdit()
	if s.windowMode {
		if s.q.Shuffle() || s.q.Loop() != queue.LoopList {
			s.q.Move(-1, false)
			return s.startCurrent(ctx)
		}
		if err := s.player.Prev(ctx); err != nil {
			return err
		}
		s.q.Move(-1, false)
		s.q.MarkPlaying()
		return nil
	}
	s.q.Move(-1, false)
	return s.startCurrent(ctx)
}

// stopAtEnd 处理“不循环时队列走到头”：停下播放器但保留队列。
func (s *Session) stopAtEnd() error {
	log.Printf("[%s] ⏹ 队列已到末尾（不循环），停止播放", s.speaker.Name)
	if s.player != nil {
		if err := s.player.Stop(context.Background()); err != nil {
			log.Printf("[%s] 停止播放器失败：%v", s.speaker.Name, err)
		}
	}
	s.q.MarkPaused()
	s.probeSoon("队列到末尾")
	return nil
}

// pushWindow 把一整段队列推给设备（player_play_music 的 musics 形式）。
//
// 随机模式下窗口本身就是洗牌后的顺序——设备按窗口顺序播，
// 服务端的队列下标靠回读的 global_id 对齐（syncWithDevice），所以不用改设备侧。
func (s *Session) pushWindow(ctx context.Context, wp player.WindowPlayer) error {
	tracks := s.q.Snapshot()
	if len(tracks) == 0 {
		return fmt.Errorf("队列为空")
	}
	idx := s.q.Index()
	size := s.app.cfg.Window
	if size <= 0 || size > len(tracks) {
		size = len(tracks)
	}
	order := make([]int, 0, size)
	switch {
	case s.q.Loop() == queue.LoopSingle:
		// 单曲循环：窗口只放当前这一首，设备 LoopSequence 自然就是原地重放，
		// 服务端回读的 global_id 也永远对得上。
		order = append(order, idx)
	case s.q.Shuffle():
		order = append(order, idx)
		pool := make([]int, 0, len(tracks)-1)
		for i := range tracks {
			if i != idx {
				pool = append(pool, i)
			}
		}
		rand.Shuffle(len(pool), func(a, b int) { pool[a], pool[b] = pool[b], pool[a] })
		order = append(order, pool...)
		if len(order) > size {
			order = order[:size]
		}
	default:
		for i := 0; i < size; i++ {
			n := idx + i
			if s.q.Loop() == queue.LoopNone {
				if n >= len(tracks) {
					break // 不循环：窗口到队尾就截断，让设备自然停
				}
			} else {
				n = queue.Wrap(n, len(tracks))
			}
			order = append(order, n)
		}
	}
	win := make([]player.WindowTrack, 0, len(order))
	// 推窗前对窗口 ids 做一次批量 resolve：把试听区间修正回队列（SetPlayable），
	// 顺带预热直链——设备首次取流不再卡在现取现跳上。
	winIDs := make([]int64, 0, len(order))
	for _, n := range order {
		winIDs = append(winIDs, tracks[n].ID)
	}
	infos := s.app.resolveMany(winIDs)
	for _, n := range order {
		t := tracks[n]
		dur := t.Duration
		if info, ok := infos[t.ID]; ok {
			s.q.SetPlayable(t.ID, info.StartMs, info.EndMs)
			// 设备拿到的是截断后的片段文件，duration_in_ms 必须传**片段真实长度**
			// （time 字段）；响应没给 time 时回退用区间长度。
			switch {
			case info.ClipMs > 0:
				dur = info.ClipMs
			case t.IsTrial():
				dur = t.ClipMs()
			}
		}
		win = append(win, player.WindowTrack{SongID: t.ID, Duration: dur, URL: s.app.deviceURL(t.ID)})
	}
	if err := wp.PlayWindow(ctx, win, 0); err != nil {
		return err
	}
	// payload 建立的列表默认播完即停，每次推窗后都要重设"循环这一窗"，
	// 否则 queue.LoopNone 的"绕回队首"探测、queue.LoopList 的持续播放都不再成立。
	if ls, ok := s.player.(player.LoopSetter); ok {
		if err := ls.SetLoop(ctx); err != nil {
			log.Printf("[%s] 设置循环模式失败（播完这一窗可能会停）：%v", s.speaker.Name, err)
		}
	}
	s.q.MarkPlaying()
	s.endedID.Store(0)
	s.expectID = tracks[idx].ID
	s.expectUntil = time.Now().Add(8 * time.Second)
	// 窗口里最后一首的队列下标：无限心动要在“这首刚起播”时重推续杯。
	// 用 order 的末元素而不是 idx+len-1，循环模式下窗口可能绕回队首。
	s.hotMu.Lock()
	s.pushedTo = order[len(order)-1]
	s.winFrom = idx
	s.winAdvanced = false
	s.winLen = len(win)
	s.hotMu.Unlock()
	log.Printf("[%s] ▶▶ 整窗下发 %d 首，从 [%d/%d] %s 开始%s",
		s.speaker.Name, len(win), idx+1, len(tracks), tracks[idx].Label(),
		map[bool]string{true: "（随机）", false: ""}[s.q.Shuffle()])
	s.probeSoon("整窗下发")
	return nil
}

// ---------- 视图 / 进度 / 音量 / 入队 ----------

// view 是队列视图 + 红心状态：红心只有 App 知道（外部 API），queue.Queue 不管。
//
// 对外视图在这里叠加「设备不可达」：冻结位置 + 播放态归零 + Unreachable 标记。
// 内部逻辑（推进、落盘、心动、歌词）一律用 s.q.View，不走这个函数，
// 所以冻结只影响显示，不会污染权威状态。
func (s *Session) view(window int) queue.QueueView {
	v := s.q.View(window)
	if v.Current != nil {
		// 收藏状态不做轮询：只有在“当前这首歌”变了的时候按需拉一次列表
		s.app.EnsureLiked(v.Current.ID)
		v.Liked = s.app.Liked(v.Current.ID)
	}
	s.freezeView(&v)
	return v
}

// fullView 是完整队列视图（WebUI / HA 浏览用）。
func (s *Session) fullView() queue.QueueFullView {
	v := s.q.FullView()
	if v.Current != nil {
		s.app.EnsureLiked(v.Current.ID)
		v.Liked = s.app.Liked(v.Current.ID)
	}
	s.freezeView(&v.QueueView)
	return v
}

// freezeView 把不可达状态盖到对外视图上：位置停在冻结值、playing 归零、
// 打上 Unreachable，让 WebUI / HA 显示「未知」。
func (s *Session) freezeView(v *queue.QueueView) {
	off, pos := s.unreachableView()
	if !off {
		return
	}
	v.Unreachable = true
	v.Playing = false
	v.PositionMs = pos
}

// Seek 拖动进度条。入参是**原曲轴**（歌词/前端显示同轴）。
// 设备侧是 player_set_positon（毫秒，实机验证有效），下发前换算成片段轴。
func (s *Session) Seek(ctx context.Context, positionMs int) error {
	s.clearPendingEdit()
	t, _, ok := s.q.Current()
	if !ok {
		return fmt.Errorf("队列为空")
	}
	if positionMs < 0 {
		positionMs = 0
	}
	// 钳进可播区间 [start, end-1000]：试听曲拖到片段外的位置只会白等
	start, end := t.TrialStartMs, t.PlayableEndMs()
	if positionMs < start {
		positionMs = start
	}
	if end > 1000 && positionMs > end-1000 {
		positionMs = end - 1000
	}
	if !s.seekVerified(ctx, positionMs, 2) {
		return fmt.Errorf("设备没有接受跳转（目标 %dms）", positionMs)
	}
	return nil
}

// pendingSeek 是「seek 已下发、等下一帧真值来确认」的登记。
//
// 为什么需要它：设备刚收到新窗口时会**静默忽略** player_set_positon（ubus 照样回
// code 0，位置没动）。有探针时做法是同步回读、没到位就重试；推送模式没有探针，
// 只能把目标记下来，等 daemon 下一帧上报时校验，没到位再补发一次。
type pendingSeek struct {
	targetMs int   // 期望位置（原曲轴）
	devMs    int   // 下发给设备的位置（片段轴）
	songID   int64 // 登记时的当前曲，换歌了就不必再校验
	at       time.Time
	tries    int
}

// seekVerified 带确认的 seek。
//
// 设备刚收到新窗口时（例如入队后重推）会**静默忽略** set_positon：ubus 照样回
// code 0，但位置没动。所以每次 seek 之后直接回读一次设备位置，没到位就重试。
// 返回是否已经落在目标位置附近。
//
// 轴约定：入参 positionMs 是**原曲轴**；对设备下发时 −TrialStartMs 换算成片段轴，
// 回读比对后再 +TrialStartMs 换算回原曲轴存队列。
func (s *Session) seekVerified(ctx context.Context, positionMs, tries int) bool {
	start := 0
	if t, _, ok := s.q.Current(); ok {
		start = t.TrialStartMs
	}
	devMs := positionMs - start
	if devMs < 0 {
		devMs = 0
	}
	if s.probe == nil {
		if err := s.player.Seek(ctx, devMs); err != nil {
			return false
		}
		// 保持当前播放态：暂停态 seek 不该被翻成播放（noop/关回读时没有任何
		// 机制能把这个错误状态纠回来，真实设备靠探针回读自愈）
		s.q.SyncPosition(positionMs, s.q.View(0).Playing)
		if s.pushMode() {
			// 推送模式：把校验挂到下一帧真值上（异步），这里只能报“已下发”
			id, _, _ := s.q.Current()
			s.seekMu.Lock()
			s.pendSeek = &pendingSeek{targetMs: positionMs, devMs: devMs, songID: id.ID, at: time.Now()}
			s.seekMu.Unlock()
		}
		return true
	}
	for attempt := 0; attempt < tries; attempt++ {
		if attempt > 0 {
			time.Sleep(350 * time.Millisecond)
		}
		if err := s.player.Seek(ctx, devMs); err != nil {
			log.Printf("[%s] seek 第 %d 次失败：%v", s.speaker.Name, attempt+1, err)
			continue
		}
		st, err := s.probe.Status(ctx)
		if err != nil {
			// 读不到就不纠缠，按“已生效”处理，交给常规探针纠偏
			s.q.SyncPosition(positionMs, s.q.View(0).Playing)
			return true
		}
		if st.Position >= devMs-2000 {
			s.device.put(st)
			s.q.SyncPosition(st.Position+start, st.Playing())
			return true
		}
	}
	return false
}

// verifyPendingSeek 在收到设备真值时复核登记的 seek。origPos 是原曲轴。
//
// 容差为什么是 2s + 一个采样周期：设备 seek 成功后位置只会**更靠前**（继续播），
// 但 daemon 报回来的是上一拍的采样值，天然滞后最多一个采样间隔；不给这个余量就会
// 在每次正常 seek 后都误判成“没生效”而白补一发。
// 补发上限 2 次：真被静默忽略时（刚推完窗）第二次基本就到位了；再多次就是设备不配合，
// 交给 syncWithDevice 已经把队列锚回真值了，用户看到的是“拖回去失败了”，而不是假成功。
func (s *Session) verifyPendingSeek(ctx context.Context, songID int64, origPos int) {
	s.seekMu.Lock()
	ps := s.pendSeek
	s.seekMu.Unlock()
	if ps == nil {
		return
	}
	sample := s.pushTiming().sample
	tol := 2000 + int(sample/time.Millisecond)
	switch {
	case ps.songID != songID:
		s.clearPendingSeek(ps) // 都换歌了，校验目标已经不存在
		return
	case origPos >= ps.targetMs-tol:
		s.clearPendingSeek(ps) // 生效了
		return
	case time.Since(ps.at) > 3*sample:
		log.Printf("[%s] ⚠️ seek 到 %dms 未被设备采纳（采样 %s 内没等到位置），已按设备真值纠回",
			s.speaker.Name, ps.targetMs, 3*sample)
		s.clearPendingSeek(ps)
		return
	case ps.tries >= 2:
		log.Printf("[%s] ⚠️ seek 到 %dms 重试 %d 次仍未生效（设备报 %dms）",
			s.speaker.Name, ps.targetMs, ps.tries, origPos)
		s.clearPendingSeek(ps)
		return
	}
	// 没到位：补发一次（设备刚收到新窗口时会静默忽略 set_positon，重试是既有结论）
	ps.tries++
	s.seekMu.Lock()
	s.pendSeek = ps
	s.seekMu.Unlock()
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.player.Seek(c, ps.devMs); err != nil {
		log.Printf("[%s] seek 补发失败：%v", s.speaker.Name, err)
	}
}

func (s *Session) clearPendingSeek(ps *pendingSeek) {
	s.seekMu.Lock()
	if s.pendSeek == ps {
		s.pendSeek = nil
	}
	s.seekMu.Unlock()
}

// wrapPosWindow 是「设备绕回窗口开头」判定允许的位置上限（片段轴，毫秒）。
//
// 这个阈值本质是在问“这首歌是不是刚开始”，而我们看到的永远是采样瞬间的值：
// 拉取模式下靠 burst（5s×3）把误差压到几秒；推送模式下最坏要等一个采样间隔才
// 收到那一帧，所以必须把窗口放宽到「采样间隔 + 2s」，否则绕回会被看成
// “设备端换歌”而继续循环，`repeat=none` 的语义就丢了（实测 5s 采样时 3000ms
// 的固定窗口约有四成概率判不出来）。
func (s *Session) wrapPosWindow() int {
	const base = 3000
	if !s.pushMode() {
		return base
	}
	if w := int(s.pushTiming().sample/time.Millisecond) + 2000; w > base {
		return w
	}
	return base
}

// VolumeState 返回逻辑音量与静音状态。
func (s *Session) VolumeState() (int, bool) {
	s.volMu.Lock()
	defer s.volMu.Unlock()
	return s.volume, s.muted
}

// AlarmVolumeState 返回缓存的闹钟音量（0-100）以及是否已知。
func (s *Session) AlarmVolumeState() (int, bool) {
	s.alarmMu.Lock()
	defer s.alarmMu.Unlock()
	return s.alarmVol, s.alarmKnown
}

// GetAlarmVolume 现查一次（云端 ubus），并更新缓存。
func (s *Session) GetAlarmVolume(ctx context.Context) (int, error) {
	if s.alarm == nil {
		return 0, fmt.Errorf("这台音箱没有配置云端 device_id，无法读取闹钟音量")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	v, err := s.alarm.AlarmVolume(ctx)

	s.alarmMu.Lock()
	defer s.alarmMu.Unlock()
	if err != nil {
		s.alarmErr = err.Error()
		return s.alarmVol, err
	}
	s.alarmVol, s.alarmKnown, s.alarmAt, s.alarmErr = v, true, time.Now(), ""
	return v, nil
}

// SetAlarmVolume 设置闹钟音量（0-100）。闹钟音量与播放音量互不影响。
func (s *Session) SetAlarmVolume(ctx context.Context, v int) error {
	if s.alarm == nil {
		return fmt.Errorf("这台音箱没有配置云端 device_id，无法设置闹钟音量")
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.alarm.SetAlarmVolume(ctx, v); err != nil {
		return err
	}
	s.alarmMu.Lock()
	s.alarmVol, s.alarmKnown, s.alarmAt, s.alarmErr = v, true, time.Now(), ""
	s.alarmMu.Unlock()
	log.Printf("[%s] ⏰ 闹钟音量 → %d", s.speaker.Name, v)
	return nil
}

// runAlarmPoll 每 60 秒回读一次闹钟音量（手机 App 也可能改它）。
func (s *Session) runAlarmPoll(ctx context.Context) {
	if s.alarm == nil {
		return
	}
	s.refreshAlarmVolume(ctx)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshAlarmVolume(ctx)
		}
	}
}

func (s *Session) refreshAlarmVolume(ctx context.Context) {
	if _, err := s.GetAlarmVolume(ctx); err != nil {
		log.Printf("[%s] 读取闹钟音量失败：%v", s.speaker.Name, err)
	}
}

// SetVolume 是音量总入口：设到 0 不改变逻辑音量，只是静音。
func (s *Session) SetVolume(ctx context.Context, v int) error {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	if err := s.player.Volume(ctx, v); err != nil {
		return err
	}
	s.volMu.Lock()
	if v == 0 {
		if !s.muted {
			s.mutePrev = s.volume
		}
		s.muted = true
	} else {
		s.volume = v
		s.muted = false
	}
	s.volMu.Unlock()
	s.probeSoon("音量变化")
	return nil
}

// SetMute 用“设备音量置 0 + 记住原值”实现静音。
//
// 设备 ubus 没有独立的 mute 方法（只有 player_set_volume），米家的静音开关
// 本质也是音量 0。HA 侧用 is_volume_muted 表达，音量条保持不变。
func (s *Session) SetMute(ctx context.Context, on bool) error {
	s.volMu.Lock()
	if on == s.muted {
		s.volMu.Unlock()
		return nil
	}
	target := 0
	if !on {
		target = s.mutePrev
		if target <= 0 {
			target = 30
		}
	} else {
		s.mutePrev = s.volume
	}
	s.volMu.Unlock()

	if err := s.player.Volume(ctx, target); err != nil {
		return err
	}
	s.volMu.Lock()
	s.muted = on
	s.volMu.Unlock()
	s.probeSoon("静音切换")
	return nil
}

// Enqueue 把曲目插到当前曲后面（next=true）或队尾，并让设备窗口跟着更新。
//
// 整窗模式下设备手里是一份固定列表，所以必须重推；设备不认 startOffset
// （实测被忽略），重推会让当前曲从头开始，因此补一次 seek 回到原位置。
func (s *Session) Enqueue(ctx context.Context, tracks []netease.Track, next bool) error {
	if len(tracks) == 0 {
		return nil
	}
	v := s.q.View(0)
	pos := v.PositionMs
	playing := v.Playing && v.Current != nil
	if next {
		s.q.InsertAfterCurrent(tracks)
	} else {
		s.q.Append(tracks)
	}
	if !playing || !s.windowMode {
		return nil
	}
	wp, ok := s.player.(player.WindowPlayer)
	if !ok {
		return nil
	}
	if err := s.pushWindow(ctx, wp); err != nil {
		return err
	}
	// pushWindow 里的 SetPlayable 可能把当前曲的位置从片段轴重锚定到原曲轴，
	// 这里必须重新取一次（旧值是换算前的，直接用会把进度拖错）。
	pos = s.q.View(0).PositionMs
	if pos > 1500 {
		// 重推窗口会让当前曲从头播，把位置补回去
		if !s.seekVerified(ctx, pos, 4) {
			log.Printf("[%s] 入队后回跳到 %dms 未成功", s.speaker.Name, pos)
		}
	}
	return nil
}

// ---------- 设备状态回读 ----------

// updateDeviceStatus 统一把设备回报写进 tracker、更新逻辑音量与可达性。
// 无论是 pollOnce 拉取还是 daemon 主动推送，都走这里——两条路径共用同一套
// 判定，才不会一边判离线一边恢复。
func (s *Session) updateDeviceStatus(st player.DeviceStatus) {
	s.device.put(st)
	s.markDeviceData()
	if st.Volume > 0 {
		s.volMu.Lock()
		// 静音时设备音量是 0，但用户设定的逻辑音量要保留（HA 里音量条不该被拽到 0）
		if !s.muted {
			s.volume = st.Volume
		}
		s.volMu.Unlock()
	}
	if s.deviceErrLogged != "" {
		log.Printf("[%s] 设备状态恢复", s.speaker.Name)
		s.deviceErrLogged = ""
	}
}

func (s *Session) pollOnce(ctx context.Context) {
	timeout := s.probeEvery + 4*time.Second
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	st, err := s.probe.Status(c)
	s.noteProbeResult(err)
	if err != nil {
		s.device.fail(err)
		if s.deviceErrLogged != err.Error() {
			log.Printf("[%s] 拉设备状态失败：%v", s.speaker.Name, err)
			s.deviceErrLogged = err.Error()
		}
		return
	}
	s.updateDeviceStatus(st)
}

// deviceFresh 表示最近一次状态获取还在有效期内。
//
// 两种模式的语义不同，别混：
//   - 拉取：3 × 稳态间隔（间隔就是节奏本身，超 3 轮说明探针链路出问题了）；
//   - 推送：直接用静默阈值，与 checkPushLiveness **同一把钟**（lastPushAt）。
//
// 推送模式必须用 lastPushAt 而不是 device.get() 的时间戳：两者虽然都在
// updateDeviceStatus 里赋值，但「数据过期」和「设备离线」若各看一把钟，
// 就可能出现 device 块消失了却没判离线（或反之）的中间态，排查时极难对齐。
func (s *Session) deviceFresh() (player.DeviceStatus, bool) {
	st, at, ok, _ := s.device.get()
	if !ok {
		return st, false
	}
	ttl := 3 * s.probeEvery
	if s.pushMode() {
		ttl = s.pushTiming().offline
		if last := s.lastPushAt.Load(); last > 0 {
			at = time.Unix(last, 0)
		}
	}
	if ttl <= 0 {
		ttl = 45 * time.Second
	}
	if time.Since(at) > ttl {
		return st, false
	}
	return st, true
}

// probeNow 同步拉一次设备状态，probeMu 保证同一时刻只有一个探测。
func (s *Session) probeNow(ctx context.Context) {
	if s.probe == nil {
		return
	}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	s.pollOnce(ctx)
}

// triggerProbe 异步探测（操作后调用）。如果已经有一个在排队，直接跳过。
// probeSoon 是“我们刚做了点什么 / 状态到了不可靠时刻”的统一收尾：
// 先把回读切到不稳定态（burst×rounds），再立刻异步探一次。
func (s *Session) probeSoon(reason string) {
	s.markUnstable(reason)
	s.triggerProbe()
}

func (s *Session) triggerProbe() {
	if s.probe == nil || !s.probePending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.probePending.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.probeNow(ctx)
	}()
}

// triggerProbeWait 用于 ?fresh=1：等这次探测完成再构建响应。
func (s *Session) triggerProbeWait(ctx context.Context) {
	if s.probe == nil {
		return
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	s.probeNow(c)
}

// runScheduler 是唯一的推进器：有设备状态就跟着设备走，没有就按本地计时。
func (s *Session) runScheduler(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flushState() // 关机前补一次落盘，最后这段进度别丢
			return
		case <-tick.C:
			// 可达性：推送静默超时 → 冻结整个播放器（见 poll.go 的 push 部分）
			s.checkPushLiveness()
			// 预计播完：设备侧可能已经切歌了，立刻探一次，别等下一个 status_poll。
			// 「播完」按可播区间算——试听曲在片段终点就该切，不能按 4 分钟的原曲总长爬。
			if v := s.q.View(0); v.Current != nil && v.Playing && v.DurationMs > 0 &&
				v.PositionMs >= v.Current.PlayableEndMs()-1200 {
				if s.endProbeID != v.Current.ID || time.Since(s.endProbeAt) > 2*time.Second {
					s.endProbeID = v.Current.ID
					s.endProbeAt = time.Now()
					s.probeSoon("临近播完")
				}
			}
			// 设备真相：只在新数据到达时同步一次。
			// 稳态间隔被拉长到几十秒后，反复拿同一份过期位置去 SyncPosition
			// 会把进度条钉死在旧值上（然后再跳一下），这里必须按代次去重。
			if st, fresh := s.deviceFresh(); fresh {
				if g := s.device.generation(); g != s.lastDevGen {
					s.lastDevGen = g
					s.syncWithDevice(ctx, st)
				}
			}
			// 离线期间不做任何"猜"：不推进、不续杯、不推歌词帧。
			// 恢复上报后下一帧真值会把位置重新锚定，队列指针也没被乱挪过。
			if !s.unreachable.Load() {
				// 本地估算始终在推进：没有设备回读时也能按时接歌
				s.advanceByTimer(ctx)
				// 无限心动：补货 + 在窗口末尾无缝重推
				s.hotmodeTick(ctx)
			}
			// 推给 SSE 订阅者（HA / WebUI）：状态与歌词各判一次，没变就是零开销
			s.emitStateIfChanged()
			if !s.unreachable.Load() {
				s.emitLyricIfChanged()
			}
			// 落盘：只在“权威数据真的变了”时写（见 state.go 的分桶策略）
			s.saveStateIfChanged()
		}
	}
}

// syncWithDevice 拿设备真相校正队列。
//
// 轴约定：设备回报的 position 是**片段轴**（试听曲播的是截断文件，它的 0 秒 =
// 原曲 start 处），进队列前 +当前曲.TrialStartMs 换算回原曲轴。
func (s *Session) syncWithDevice(ctx context.Context, st player.DeviceStatus) {
	v := s.q.View(0)
	if v.Current == nil {
		return
	}
	curID := v.Current.ID
	curStart := v.Current.TrialStartMs
	if curStart < 0 {
		curStart = 0
	}
	// payload 分支回读的就是网易云原 id（`play_song_detail.audio_id`），不需要再做映射
	devTrack := st.TrackID()
	if !s.expectUntil.IsZero() && time.Now().Before(s.expectUntil) && devTrack != 0 && devTrack != s.expectID {
		return
	}
	if devTrack == 0 {
		if v.Playing && st.Status == 0 && v.PositionMs > 3000 {
			if s.windowMode {
				s.repush(ctx, "设备已停止")
			} else {
				s.finishCurrent(ctx, "设备已停止")
			}
		}
		return
	}
	if devTrack == curID {
		// 片段轴 → 原曲轴（换曲对齐之前都是当前曲的片段）
		origPos := st.Position + curStart
		playing := st.Playing()
		// 只有“设备比我们更靠前”才说明中间发生了我们没看见的事（外部快进、
		// 我们漏掉了接歌）。反过来——我们估算在前——多半是单曲循环/窗口绕回把
		// 位置重置成 0 了，SyncPosition 会安静地重新锚定，不该触发加密回读。
		if playing && v.Playing && s.pol.drift > 0 {
			if d := origPos - v.PositionMs; d > int(s.pol.drift/time.Millisecond) {
				s.markUnstable(fmt.Sprintf("设备比我们靠前 %dms", d))
			}
		}
		if playing != v.Playing {
			s.markUnstable("播放态与预期不符")
		}
		s.q.SyncPosition(origPos, playing)
		// 推送模式下 seek 是异步确认的：拿这一帧真值复核登记的目标位置
		s.verifyPendingSeek(ctx, curID, origPos)
		// 设备报播完：这是**设备轴**判定——st.Duration 是设备眼里这个音频文件的
		// 长度（试听曲就是片段长度；设备对代理流上报的 duration 偏差约 ±1s，
		// 之前文档写"约 35s 不准"实为旧样本，试听片段本来就只有 35s）。
		if !playing && st.Duration > 0 && st.Position >= st.Duration-1500 {
			if s.windowMode {
				s.repush(ctx, "设备报播完")
			} else {
				s.finishCurrent(ctx, "设备报播完")
			}
		}
		return
	}
	if t, idx, ok := s.q.FindByID(devTrack); ok {
		// 不循环时窗口到队尾就截断了，但设备的 LoopSequence 仍会绕回窗口开头。
		// 认出来这个“绕回”，就当作整张队列播完，停下。
		winFrom, _, _ := s.windowBounds()
		if s.windowMode && s.q.Loop() == queue.LoopNone && winFrom >= 0 &&
			idx == winFrom && s.q.Index() != winFrom && st.Position < s.wrapPosWindow() {
			log.Printf("[%s] 窗口绕回队首，且不循环 → 视为播完", s.speaker.Name)
			_ = s.stopAtEnd()
			return
		}
		s.q.Jump(idx)
		// 换歌了：之前登记的 seek 校验目标已经不存在
		s.seekMu.Lock()
		s.pendSeek = nil
		s.seekMu.Unlock()
		// 新曲的片段轴 → 原曲轴
		newStart := t.TrialStartMs
		if newStart < 0 {
			newStart = 0
		}
		s.q.SyncPosition(st.Position+newStart, st.Playing())
		s.expectID, s.expectUntil = 0, time.Time{}
		log.Printf("[%s] 设备端换歌 → 队列对齐到 [%d/%d] %s", s.speaker.Name, idx+1, s.q.Len(), t.Label())
		s.markUnstable("设备端换歌")
		return
	}
	if v.Playing {
		s.q.MarkPaused()
		log.Printf("[%s] 设备在放别的（track_id=%d audio_id=%s media_type=%d），xiaoai-mb 让位",
			s.speaker.Name, devTrack, st.AudioID, st.MediaType)
		s.markUnstable("外部内容抢占")
	}
}

func (s *Session) advanceByTimer(ctx context.Context) {
	if s.windowMode {
		return
	}
	v := s.q.View(0)
	if !v.Playing || v.DurationMs <= 0 || v.Current == nil {
		return
	}
	if s.endedID.Load() == v.Current.ID {
		return
	}
	// 本地计时也要按可播区间算（同 runScheduler，见那里注释）
	if v.PositionMs >= v.Current.PlayableEndMs()-400 {
		s.finishCurrent(ctx, "本地计时到点")
	}
}

func (s *Session) finishCurrent(ctx context.Context, reason string) {
	cur, _, ok := s.q.Current()
	if !ok {
		return
	}
	if !s.endedID.CompareAndSwap(0, cur.ID) && s.endedID.Load() == cur.ID {
		return
	}
	log.Printf("[%s] 曲目结束（%s）：%s", s.speaker.Name, reason, cur.Label())
	if _, _, ok := s.q.Move(1, true); !ok {
		_ = s.stopAtEnd()
		return
	}
	if err := s.startCurrent(ctx); err != nil {
		log.Printf("[%s] 自动下一首失败：%v", s.speaker.Name, err)
	}
}

func (s *Session) repush(ctx context.Context, reason string) {
	if time.Since(s.lastRepush) < 10*time.Second {
		return
	}
	wp, ok := s.player.(player.WindowPlayer)
	if !ok {
		return
	}
	s.lastRepush = time.Now()
	log.Printf("[%s] 设备停止（%s），重新下发整窗", s.speaker.Name, reason)
	if err := s.pushWindow(ctx, wp); err != nil {
		log.Printf("[%s] 重推失败：%v", s.speaker.Name, err)
	}
}

// deviceView 给 /api/device 用。
func (s *Session) deviceView() map[string]any {
	st, at, ok, errMsg := s.device.get()
	poll := int(s.probeEvery / time.Second)
	if poll <= 0 {
		poll = s.speaker.StatusPoll
	}
	out := map[string]any{
		"probe":      s.probe != nil,
		"poll_sec":   poll,
		"last_ok":    ok,
		"last_error": errMsg,
	}
	// 设备不可达时这份回读是**最后一次已知**，不是"探针看到的真相"。
	// 不打标记的话，设备调试页会一边显示 144 秒前的旧采样、一边显示冻结的权威状态，
	// 排查时看着像两个互相矛盾的事实（实测踩过）。
	if off, _ := s.unreachableView(); off {
		out["stale"] = true
	}
	if !at.IsZero() {
		out["age_sec"] = int(time.Since(at).Seconds())
		out["status"] = st.Status
		out["playing"] = st.Playing()
		out["audio_id"] = st.AudioID
		out["global_id"] = st.GlobalID
		out["track_id"] = st.TrackID()
		out["position_ms"] = st.Position
		out["duration_ms"] = st.Duration
		out["volume"] = st.Volume
		out["media_type"] = st.MediaType
		out["loop_type"] = st.LoopType
	}
	if v := s.q.View(0); v.Current != nil {
		out["queue"] = map[string]any{
			"index":   v.Index,
			"total":   v.Total,
			"playing": v.Playing,
			"id":      v.Current.ID,
			"label":   v.Current.Label(),
		}
	}
	return out
}
