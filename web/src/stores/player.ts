import { computed, reactive, ref, shallowRef, watch } from 'vue'
import * as api from '../api/client'
import type { Account, LyricPayload, QueueFull, Speaker, StatePayload, Track } from '../api/types'

const SPEAKER_KEY = 'xiaoai_mb_speaker'

/**
 * 播放器全局状态。
 *
 * 状态权威在服务端（设备不会主动上报），所以这里的实时性完全靠 `/api/events`（SSE）：
 * 服务端每次状态变化推一帧完整快照，我们只负责落地 + 本地插值进度。
 * SSE 断了就退回 5s 轮询，EventSource 自己也会重连。
 */
function createPlayerStore() {
  const speakers = ref<Speaker[]>([])
  const speakerId = ref<string>(localStorage.getItem(SPEAKER_KEY) || '')
  // 队列可能上千首，用 shallowRef：只换引用、不做深度代理
  const state = shallowRef<StatePayload | null>(null)
  const queue = shallowRef<QueueFull | null>(null)
  const lyric = shallowRef<LyricPayload | null>(null)
  const account = ref<Account | null>(null)
  const online = ref<boolean>(false)
  const busy = ref<string>('')
  const error = ref<string>('')

  /** 每台音箱最近一帧状态，给侧栏列表用（SSE 推的是 all，不过滤） */
  const perSpeaker = reactive<Record<string, StatePayload>>({})

  // ---- 进度本地插值 ----
  // 服务端只给「快照位置 + 是否播放」，进度条要每秒动，所以在本地记一个基准点：
  // position_ms 是采样时刻的值，posAt 是采样的墙上时间，二者相加得到当前位置。
  let posBase = 0
  let posAt = 0
  const now = ref(Date.now())
  const clock = setInterval(() => (now.value = Date.now()), 500)

  const playing = computed(() => !!state.value?.queue.playing)
  // 设备不可达时**不再插值**：服务端的 position_ms 已经是冻结值，
  // 本地继续按墙上时间加就会看到进度条涨过曲尾（4:27 显示到 7:12）。
  const unreachable = computed(() => !!state.value?.unreachable)
  const positionMs = computed(() => {
    if (unreachable.value) return posBase
    if (!playing.value) return posBase
    return posBase + Math.max(0, now.value - posAt)
  })
  const durationMs = computed(() => state.value?.queue.duration_ms || queue.value?.current?.duration_ms || 0)
  // 队列长度优先取 SSE 帧里的真值：/api/queue 那份是按需拉的快照，
  // 拿它当计数会在续杯/清空后显示陈旧数字。
  const queueTotal = computed(() => state.value?.queue.total ?? queue.value?.total ?? 0)
  const current = computed<Track | null>(() => state.value?.queue.current || queue.value?.current || null)
  // ---- 试听片段（原曲时间轴）----
  // 进度条底槽代表原曲总长，可播区间 [trialStartMs, playableEndMs] 用色带高亮；
  // 时间轴统一是原曲轴（服务端已换算），只有设备边界做偏移。
  const trialStartMs = computed(() => current.value?.trial_start_ms || 0)
  const playableEndMs = computed(() => {
    const c = current.value
    if (!c) return 0
    const end = c.trial_end_ms || 0
    return end > 0 && end < c.duration_ms ? end : c.duration_ms || 0
  })
  const isTrial = computed(() => !!current.value && (current.value.trial_end_ms || 0) > 0 && (current.value.trial_end_ms || 0) < (current.value.duration_ms || 0))
  const volume = computed(() => state.value?.volume ?? 0)
  const muted = computed(() => !!state.value?.muted)
  const repeat = computed(() => state.value?.queue.repeat || 'list')
  const shuffle = computed(() => !!state.value?.queue.shuffle)
  const liked = computed(() => !!state.value?.queue.liked)
  const hotmode = computed(() => state.value?.hotmode || null)
  const device = computed(() => state.value?.device || null)
  const hasQueue = computed(() => (state.value?.queue.total || 0) > 0)

  function rememberPosition(q: StatePayload['queue']) {
    posBase = q.position_ms || 0
    posAt = Date.now()
    // 单曲循环等设备侧会自己绕回，服务端位置可能突然掉回 0，这里跟着重置基准
    if (q.current && posBase > q.duration_ms && q.duration_ms > 0) {
      posBase = 0
    }
  }

  function applyState(frame: StatePayload) {
    perSpeaker[frame.speaker] = frame
    if (frame.speaker !== speakerId.value) return
    const sameSong = state.value?.queue.current?.id === frame.queue.current?.id
    state.value = frame
    if (sameSong || !frame.queue.playing) {
      rememberPosition(frame.queue)
    } else {
      // 换曲：设备已经在新歌上了，位置由服务端给
      rememberPosition(frame.queue)
    }
    // 队列**形状**变了要补拉一次完整列表：SSE 帧只带计数和当前曲，抽屉里的
    // 那份 tracks 是 /api/queue 单独拉的快照。不补的话，被动变化（无限心动
    // 续杯 +上百首、别的客户端清空、设备侧自己接歌）在抽屉里永远看不见，
    // 只有刷新页面才更新——这不是缓存策略，是陈旧 bug。
    // 只比 total：换曲不改列表内容，别为此把几百首重拉一遍。
    if (!booting && queue.value && queue.value.total !== frame.queue.total) scheduleQueueReload()
  }

  let queueReloadTimer: number | null = null

  // 合并短时间内的多次形状变化（续杯会连着推好几批），1.5s 内只拉一次。
  function scheduleQueueReload() {
    if (queueReloadTimer !== null) clearTimeout(queueReloadTimer)
    queueReloadTimer = window.setTimeout(() => {
      queueReloadTimer = null
      void loadQueue()
    }, 1500)
  }

  // ---- 加载 ----

  async function loadSpeakers() {
    try {
      const r = await api.getSpeakers()
      speakers.value = r.speakers
      if (!speakerId.value || !r.speakers.some((s) => s.id === speakerId.value)) {
        speakerId.value = r.speakers[0]?.id || ''
      }
    } catch (e) {
      error.value = (e as Error).message
    }
  }

  async function loadState() {
    try {
      const s = await api.getState(speakerId.value)
      state.value = s
      rememberPosition(s.queue)
      error.value = ''
      online.value = true
    } catch (e) {
      online.value = false
      error.value = (e as Error).message
    }
  }

  async function loadQueue() {
    try {
      queue.value = (await api.getQueue(speakerId.value)).queue
    } catch (e) {
      error.value = (e as Error).message
    }
  }

  async function loadLyric(full = false) {
    try {
      lyric.value = await api.getLyric(speakerId.value, full)
    } catch {
      lyric.value = null // 没歌词是常态，不算错误
    }
  }

  async function loadAccount() {
    try {
      account.value = await api.getAccount()
    } catch {
      account.value = null
    }
  }

  async function refreshAll() {
    // 先拿音箱列表：多台音箱时 /api/state 不带 speaker 是 400，顺序错了会白报一次错
    await loadSpeakers()
    await Promise.all([loadState(), loadQueue(), loadLyric(true), loadAccount()])
  }

  // ---- 动作 ----
  //
  // 每个动作都：置 busy → 调接口 → 立刻拉一次状态（服务端也会推 SSE 帧，
  // 但 HTTP 响应先回来，点完不该等下一帧）。

  async function act<T>(label: string, fn: () => Promise<T>, opts: { resync?: boolean } = {}): Promise<T | null> {
    busy.value = label
    try {
      const out = await fn()
      const resync = opts.resync ?? true
      if (resync) await Promise.all([loadState(), loadQueue()])
      return out
    } catch (e) {
      error.value = (e as Error).message
      return null
    } finally {
      busy.value = ''
    }
  }

  const actions = {
    toggle: () => (playing.value ? act('暂停', () => api.pause(speakerId.value)) : act('继续', () => api.resume(speakerId.value))),
    play: () => act('继续', () => api.resume(speakerId.value)),
    pause: () => act('暂停', () => api.pause(speakerId.value)),
    stop: () => act('停止', () => api.stop(speakerId.value)),
    next: () => act('下一首', () => api.next(speakerId.value)),
    prev: () => act('上一首', () => api.prev(speakerId.value)),
    jump: (i: number) => act('切歌', () => api.jump(i, speakerId.value)),
    seekTo: (ms: number) => {
      // 先钳进可播区间（试听曲拖到片段外只会白等），再本地跳——服务端也会钳一次
      const start = trialStartMs.value
      const end = playableEndMs.value
      let target = Math.max(0, ms)
      if (end > 0) {
        if (target < start) target = start
        if (end > 1000 && target > end - 1000) target = end - 1000
      }
      posBase = target
      posAt = Date.now()
      return act('跳转', () => api.seek(target, speakerId.value))
    },
    setVolume: (v: number) => {
      return act('', () => api.setVolume(v, speakerId.value), { resync: false })
    },
    toggleMute: () => act('静音', () => api.setMute(!muted.value, speakerId.value), { resync: false }),
    setRepeat: (r: 'none' | 'list' | 'single') => act('循环模式', () => api.setMode({ repeat: r }, speakerId.value)),
    toggleShuffle: () => act('随机', () => api.setMode({ shuffle: !shuffle.value }, speakerId.value)),
    /**
     * 循环模式四态循环，和旧版按钮一致：
     * 列表循环 → 单曲循环 → 随机（列表+随机） → 不循环 → 列表循环
     */
    cycleMode: () => {
      const next =
        shuffle.value && repeat.value === 'list'
          ? { repeat: 'none' as const, shuffle: false }
          : repeat.value === 'none'
            ? { repeat: 'list' as const, shuffle: false }
            : repeat.value === 'single'
              ? { repeat: 'list' as const, shuffle: true }
              : { repeat: 'single' as const, shuffle: false }
      return act('播放模式', () => api.setMode(next, speakerId.value))
    },
    toggleLike: () => {
      const t = current.value
      if (!t) return Promise.resolve(null)
      return act('红心', () => api.setLike(t.id, !liked.value, speakerId.value), { resync: false })
    },
    playSource: (src: api.PlaySource) => act('播放', () => api.play(src, speakerId.value)),
    enqueueSource: (src: api.PlaySource, nextNow = false) => act('添加队列', () => api.enqueue(src, nextNow, speakerId.value)),
    startHotmode: (seed: 'current' | 'liked' | string) => act('心动模式', () => api.hotmode(seed, speakerId.value)),
    stopHotmode: () => act('心动停止', () => api.hotmodeStop(speakerId.value)),
    removeAt: (i: number) => act('删除', () => api.removeTrack(i, speakerId.value)),
    clear: () => act('清空', () => api.clearQueue(speakerId.value)),
    moveTo: (from: number, to: number) => act('排序', () => api.moveTrack(from, to, speakerId.value)),
    setAlarmVolume: (v: number) => act('闹钟音量', () => api.setAlarmVolume(v, speakerId.value), { resync: false }),
  }

  // ---- SSE ----

  let es: EventSource | null = null
  let pollTimer: number | null = null

  function start() {
    // 首次加载期间 speakerId 会被填上，别让下面那个 watch 再刷一遍
    booting = true
    refreshAll().finally(() => {
      booting = false
    })
    connect()
  }

  function connect() {
    if (es) es.close()
    es = new EventSource('/api/events')
    es.onopen = () => {
      online.value = true
      if (pollTimer !== null) {
        clearInterval(pollTimer)
        pollTimer = null
      }
    }
    es.onerror = () => {
      online.value = false
      // EventSource 会自动重连；这里只补一个低频轮询兜底，避免长时间黑在这
      if (pollTimer === null) {
        pollTimer = window.setInterval(() => loadState(), 5000)
      }
    }
    es.addEventListener('state', (ev: MessageEvent) => {
      try {
        applyState(JSON.parse(ev.data) as StatePayload)
      } catch {
        /* 帧坏了就等下一帧 */
      }
    })
    es.addEventListener('lyric', (ev: MessageEvent) => {
      try {
        const p = JSON.parse(ev.data) as LyricPayload & { speaker?: string }
        if (!p.speaker || p.speaker === speakerId.value) {
          // 只换当前行，整首 lines 由 loadLyric(full) 提供
          lyric.value = { ...lyric.value, ...p, lines: lyric.value?.lines } as LyricPayload
        }
      } catch {
        /* ignore */
      }
    })
  }

  function stop() {
    if (es) es.close()
    es = null
    if (pollTimer !== null) clearInterval(pollTimer)
    clearInterval(clock)
  }

  let booting = false
  watch(speakerId, (id) => {
    localStorage.setItem(SPEAKER_KEY, id)
    if (booting) return
    state.value = perSpeaker[id] || null
    if (state.value) rememberPosition(state.value.queue)
    queue.value = null
    lyric.value = null
    refreshAll()
  })

  // 换曲时重新拉整首歌词（SSE 的 lyric 帧只带当前行，lines 必须走 /api/lyric?full=1）。
  // booting 期间 refreshAll 自己会拉一次，跳过避免重复请求。
  watch(
    () => state.value?.queue.current?.id,
    (id, old) => {
      if (!id || id === old || booting) return
      loadLyric(true)
    },
  )

  return {
    // 状态
    speakers,
    speakerId,
    state,
    queue,
    lyric,
    account,
    online,
    busy,
    error,
    perSpeaker,
    // 派生
    playing,
    positionMs,
    durationMs,
    unreachable,
    queueTotal,
    current,
    trialStartMs,
    playableEndMs,
    isTrial,
    volume,
    muted,
    repeat,
    shuffle,
    liked,
    hotmode,
    device,
    hasQueue,
    // 方法
    start,
    stop,
    refreshAll,
    loadSpeakers,
    loadQueue,
    loadLyric,
    loadState,
    loadAccount,
    actions,
  }
}

// reactive() 让模板里直接写 player.error 而不是 player.error.value；
// 里面的 ref 会被自动解包，赋值也直接落到 .value 上。
export const player = reactive(createPlayerStore())
export type PlayerStore = typeof player
