// 与 Go 服务端对齐的类型。字段名以 internal/{queue,netease,app} 的 json tag 为准，
// 改服务端契约时这里要同步（HA 集成也在消费同一批字段）。

export interface Track {
  id: number
  name: string
  artists: string
  album: string
  cover: string
  /** 原曲总时长（试听曲也保持原曲长度；可播区间看 trial_* 字段） */
  duration_ms: number
  /** 试听片段起点（原曲时间轴毫秒）；缺省 0 = 从头 */
  trial_start_ms?: number
  /** 试听片段终点（原曲时间轴毫秒）；缺省/0 = 全曲可播 */
  trial_end_ms?: number
}

export type RepeatMode = 'none' | 'list' | 'single'

export interface QueueView {
  source: string
  /** 展示用标签：开着随机时报 "shuffle" */
  loop: RepeatMode | 'shuffle'
  /** 真实循环语义，与 shuffle 正交 */
  repeat: RepeatMode
  shuffle: boolean
  playing: boolean
  index: number
  total: number
  liked: boolean
  position_ms: number
  duration_ms: number
  /**
   * 设备不可达（推送静默超时）。为 true 时 playing / position_ms 都是
   * **冻结的最后真值**，界面应显示「未知」而不是「已暂停 + 某个进度」。
   */
  unreachable?: boolean
  current?: Track
  upcoming?: Track[]
}

export interface QueueFull extends QueueView {
  tracks: Track[]
}

export interface HotmodeState {
  on: boolean
  fails: number
  /** 累计续杯（重推）次数 */
  extended: number
  below: number
  /** 停止原因（手动停止 / 连续失败 / 已切换来源）；on=true 时为空 */
  stop_reason?: string
}

export interface PersistState {
  path: string
  had_queue: boolean
  saved_at?: number
}

export interface DeviceState {
  audio_id: string
  status: string
  playing: boolean
  position_ms: number
  duration_ms: number
  volume: number
  loop_type: number | string
  media_type: string
  global_id: number
  track_id: number
  last_ok: boolean
  poll_sec: number
}

export interface PollState {
  mode?: string
  probe_sec?: number
  burst_sec?: number
  rounds?: number
  pending?: number
  reason?: string
  last_ok?: boolean
  last_error?: string
  [k: string]: unknown
}

/** /api/state 与 SSE 的 `event: state` 帧共用同一个载荷 */
export interface StatePayload {
  ok: boolean
  speaker: string
  queue: QueueView
  poll?: PollState
  hotmode?: HotmodeState
  volume: number
  muted: boolean
  persist?: PersistState
  alarm_volume?: number
  device?: DeviceState
  /** 设备不可达（推送静默超时）：整个播放器冻结，读数按「未知」显示 */
  unreachable?: boolean
}

export interface LyricLine {
  t_ms: number
  text: string
}

export interface LyricSnapshot {
  song_id: number
  line: string
  next_line?: string
  index: number
  total: number
  position_ms: number
}

export interface LyricPayload {
  ok: boolean
  speaker?: string
  lyric: LyricSnapshot
  lines?: LyricLine[]
}

export interface Playlist {
  id: number
  name: string
  cover: string
  track_count: number
  play_count: number
}

// ---- B1 新端点（字段名以 8088 实测为准）----

export interface RecommendPlaylist {
  id: number
  name: string
  pic_url: string
  play_count: number
  track_count: number
  copywriter: string
}

export type RecommendSource = 'recommend' | 'personalized'

export interface ToplistItem {
  id: number
  name: string
  cover_url: string
  play_count: number
  type: string
}

export interface NewAlbum {
  id: number
  name: string
  pic_url: string
  artist: string
}

export interface SearchPlaylist {
  id: number
  name: string
  cover_url: string
  play_count: number
  track_count: number
}

export interface SearchArtist {
  id: number
  name: string
  pic_url: string
  alias?: string[]
  music_size: number
  album_size: number
}

export interface SearchAlbum {
  id: number
  name: string
  artist?: string
  pic_url: string
  songs_count?: number
}

/** /api/search 带 type 时三个键恒在，非当前类型为 null */
export interface SearchAllReply {
  ok: boolean
  type: string
  tracks?: Track[]
  playlists: SearchPlaylist[] | null
  artists: SearchArtist[] | null
  albums: SearchAlbum[] | null
}

export interface Speaker {
  id: string
  name: string
  control: string
  status_mode?: string
  /** 设备不可达（推送静默超时）：此时 playing/current 都是最后已知值 */
  unreachable?: boolean
  volume: number
  muted: boolean
  playing?: boolean
  status?: string
  current?: string
  queue_index?: number
  queue_total?: number
}

export interface Account {
  uid: number
  nickname: string
}

export interface SpeakerConfig {
  id: string
  name: string
  control: 'cloud' | 'ssh' | 'noop' | string
  status_mode?: 'cloud' | 'push' | 'ssh' | 'none' | string
  device_id?: string
  ssh_host?: string
  ssh_key?: string
  ubus_timeout?: number
  media?: string
  play_type?: number
  use_window?: boolean
  status_poll?: number
  poll_burst?: number
  poll_rounds?: number
  poll_drift_ms?: number
  /** 以下四项只在 status_mode='push' 有意义：服务端判定用的 daemon 节奏期望值，
   *  必须与音箱上 /data/etc/state_reporter.conf 一致 */
  report_interval?: number
  report_hb_play?: number
  report_hb_idle?: number
  report_seek_tol_ms?: number
}

export interface XiaomiConfig {
  user?: string
  password?: string
  device_id?: string
  token_file?: string
  key_file?: string
}

export interface AppConfig {
  listen: string
  public_base: string
  cookie_file: string
  cookie_key_file?: string
  quality: string
  window: number
  loop: string
  stream_mode: string
  hotmode_extend_below: number
  hotmode_extend_max_fail: number
  queue_persist: boolean
  xiaomi?: XiaomiConfig
  speakers: SpeakerConfig[]
}

export interface XiaomiDevice {
  device_id: string
  name: string
  hardware: string
  presence: string
  miot_did: string
}

export interface XiaomiAuthStatus {
  ok: boolean
  logged_in: boolean
  user_id?: string
  device_id?: string
  updated_at?: number
  token_file: string
}

export interface XiaomiQRStart {
  ok: boolean
  session_id: string
  login_url: string
  qr: string
  timeout: number
}

export interface XiaomiQRCheck {
  ok: boolean
  status: 'waiting' | 'authorized' | 'expired' | 'failed'
  user_id?: string
  error?: string
}

