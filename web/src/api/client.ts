import type {
  Account,
  AppConfig,
  NewAlbum,
  Playlist,
  LyricPayload,
  QueueFull,
  QueueView,
  RecommendPlaylist,
  RecommendSource,
  SearchAllReply,
  Speaker,
  StatePayload,
  ToplistItem,
  Track,
  XiaomiAuthStatus,
  XiaomiDevice,
  XiaomiQRCheck,
  XiaomiQRStart,
} from './types'

/** 服务端所有接口的统一外壳：`{ok:false,error}` 时抛错，错误文本来自后端（中文，可直接展示） */
export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
    this.name = 'ApiError'
  }
}

const DEFAULT_TIMEOUT = 12_000
/** 开播/心动要拉歌 + 推整窗，后端自己留了 20~30s，这里别先断 */
const LONG_TIMEOUT = 30_000

async function request<T>(path: string, init: RequestInit = {}, timeout = DEFAULT_TIMEOUT): Promise<T> {
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), timeout)
  let res: Response
  try {
    res = await fetch(path, {
      ...init,
      signal: ctrl.signal,
      headers: { 'Content-Type': 'application/json', ...(init.headers || {}) },
    })
  } catch (e) {
    // AbortError 的原文案没用，换成看得懂的
    throw new ApiError(0, (e as Error).name === 'AbortError' ? '请求超时，服务可能没在跑' : `网络错误：${(e as Error).message}`)
  } finally {
    clearTimeout(timer)
  }

  const text = await res.text()
  let body: any = {}
  if (text) {
    try {
      body = JSON.parse(text)
    } catch {
      throw new ApiError(res.status, `响应不是 JSON（HTTP ${res.status}）：${text.slice(0, 120)}`)
    }
  }
  if (!res.ok || body.ok === false) {
    throw new ApiError(res.status, body.error || `HTTP ${res.status}`)
  }
  return body as T
}

const post = <T,>(path: string, body?: unknown, long = false) =>
  request<T>(path, { method: 'POST', body: JSON.stringify(body ?? {}) }, long ? LONG_TIMEOUT : DEFAULT_TIMEOUT)

/** 只有按音箱的接口需要 speaker；不传时服务端在「只有一台」时自动选 */
const withSpeaker = (path: string, speaker?: string) =>
  !speaker ? path : `${path}${path.includes('?') ? '&' : '?'}speaker=${encodeURIComponent(speaker)}`

// ---------- 只读 ----------

export const getSpeakers = () => request<{ ok: boolean; speakers: Speaker[] }>('/api/speakers')
export const getState = (speaker?: string) => request<StatePayload>(withSpeaker('/api/state', speaker))
export const getQueue = (speaker?: string) => request<{ ok: boolean; queue: QueueFull }>(withSpeaker('/api/queue', speaker))
export const getDevice = (speaker?: string) => request<{ ok: boolean; device: Record<string, unknown> }>(withSpeaker('/api/device', speaker))
export const getAccount = () => request<Account>('/api/account')
export const getDaily = () => request<{ ok: boolean; tracks: Track[] }>('/api/daily')
export const getPlaylists = () => request<{ ok: boolean; playlists: Playlist[] }>('/api/user/playlists')
export const getPlaylist = (id: number | string) => request<{ ok: boolean; tracks: Track[] }>(`/api/playlist/${id}`)
export const search = (q: string, limit = 20) =>
  request<{ ok: boolean; tracks: Track[] }>(`/api/search?q=${encodeURIComponent(q)}&limit=${limit}`)

/** 多类型搜索：song 返回 tracks，其它返回 playlists/artists/albums（非当前类型为 null） */
export type SearchType = 'song' | 'playlist' | 'artist' | 'album'
export const searchAll = (q: string, type: SearchType, limit = 30) =>
  request<SearchAllReply>(`/api/search?q=${encodeURIComponent(q)}&type=${type}&limit=${limit}`)

// ---- B1 发现页端点 ----

export const getRecommendPlaylists = () =>
  request<{ ok: boolean; playlists: RecommendPlaylist[]; source: RecommendSource }>('/api/recommend/playlists')
export const getToplist = () => request<{ ok: boolean; toplist: ToplistItem[] }>('/api/toplist')
export const getNewAlbums = () => request<{ ok: boolean; albums: NewAlbum[] }>('/api/newalbums')
export const getSearchHot = () => request<{ ok: boolean; searches: string[] }>('/api/search/hot')
export const getFm = () => request<{ ok: boolean; tracks: Track[] }>('/api/fm')
export const getLyric = (speaker?: string, full = false) =>
  request<LyricPayload>(withSpeaker(`/api/lyric${full ? '?full=1' : ''}`, speaker))

/** B1：封面取色。取色失败后端不报错，返回默认调色板 + fallback=true，前端永远有颜色可用 */
export interface PalettePayload {
  ok: boolean
  song_id: number
  cover: string
  vibrant: string
  dark: string
  muted: string
  fallback?: boolean
}
export const getPalette = (songId: number | string) => request<PalettePayload>(`/api/palette/${songId}`)

// ---------- 播放控制 ----------

/** /api/play 与 /api/enqueue 共用同一个来源描述体 */
export interface PlaySource {
  query?: string
  playlist_id?: string | number
  album_id?: string | number
  song_ids?: number[]
  daily?: boolean
  index?: number
}

export interface QueueActionReply {
  ok: boolean
  queue?: QueueView
  added?: number
  warning?: string
}

/**
 * 服务端的 playlist_id / album_id 是字符串字段（网易云 id 太长，别当数字传），
 * 前端调用方经常写成数字，这里统一转一次，省得每个调用点都记得 String()。
 */
function normalizeSource(src: PlaySource): PlaySource {
  const out: PlaySource = { ...src }
  if (src.playlist_id !== undefined) out.playlist_id = String(src.playlist_id)
  if (src.album_id !== undefined) out.album_id = String(src.album_id)
  return out
}

export const play = (src: PlaySource, speaker?: string) =>
  post<QueueActionReply>(withSpeaker('/api/play', speaker), normalizeSource(src), true)
export const enqueue = (src: PlaySource, next = false, speaker?: string) =>
  post<QueueActionReply>(withSpeaker(`/api/enqueue${next ? '?next=1' : ''}`, speaker), normalizeSource(src))
export const hotmode = (seed: 'current' | 'liked' | string, speaker?: string) =>
  post<{ ok: boolean; seed_label?: string; count?: number; infinite?: boolean; queue?: QueueView }>(
    withSpeaker('/api/hotmode', speaker),
    { seed },
    true,
  )
/** 手动退出无限心动（队列与播放不受影响，只是停止续杯） */
export const hotmodeStop = (speaker?: string) =>
  post<{ ok: boolean; extended?: number }>(withSpeaker('/api/hotmode/stop', speaker))
export const jump = (index: number, speaker?: string) => post<QueueActionReply>(withSpeaker('/api/jump', speaker), { index })
export const next = (speaker?: string) => post<QueueActionReply>(withSpeaker('/api/next', speaker))
export const prev = (speaker?: string) => post<QueueActionReply>(withSpeaker('/api/prev', speaker))
export const pause = (speaker?: string) => post<{ ok: boolean }>(withSpeaker('/api/pause', speaker))
export const resume = (speaker?: string) => post<{ ok: boolean }>(withSpeaker('/api/resume', speaker), undefined, true)
export const stop = (speaker?: string) => post<{ ok: boolean }>(withSpeaker('/api/stop', speaker))
export const seek = (positionMs: number, speaker?: string) =>
  post<{ ok: boolean; position_ms: number }>(withSpeaker('/api/seek', speaker), { position_ms: positionMs })
export const setVolume = (value: number, speaker?: string) =>
  post<{ ok: boolean; volume: number; muted: boolean }>(withSpeaker('/api/volume', speaker), { value })
export const setMute = (value: boolean, speaker?: string) =>
  post<{ ok: boolean; muted: boolean; volume: number }>(withSpeaker('/api/mute', speaker), { value })
export const setMode = (opts: { repeat?: 'none' | 'list' | 'single'; shuffle?: boolean }, speaker?: string) =>
  post<{ ok: boolean; loop: string; shuffle: boolean }>(withSpeaker('/api/mode', speaker), opts)
export const setLike = (songId: number, like: boolean, speaker?: string) =>
  post<{ ok: boolean; song_id: number; liked: boolean }>(withSpeaker('/api/like', speaker), { song_id: songId, like })
export const setAlarmVolume = (value: number, speaker?: string) =>
  post<{ ok: boolean; alarm_volume: number }>(withSpeaker('/api/alarm_volume', speaker), { value })

// ---------- 队列编辑 ----------

export const removeTrack = (index: number, speaker?: string) =>
  post<{ ok: boolean; warning?: string; current?: boolean; total: number; index: number; playing: boolean }>(
    withSpeaker('/api/queue/remove', speaker),
    { index },
  )
export const clearQueue = (speaker?: string) =>
  post<{ ok: boolean; cleared: boolean; total: number }>(withSpeaker('/api/queue/remove', speaker), { all: true })
export const moveTrack = (from: number, to: number, speaker?: string) =>
  post<{ ok: boolean; warning?: string; total: number; index: number; playing: boolean }>(
    withSpeaker('/api/queue/move', speaker),
    { from, to },
  )

// ---------- 管理面（/api/admin/*）----------

export interface AdminBackup {
  name: string
  size: number
  modified_at: number
}

export interface AdminRuntime {
  version: string
  api: number
  pid: number
  uptime_sec: number
  listen: string
  public_base: string
  speakers: number
}

export interface AdminConfigReply {
  ok: boolean
  path: string
  base_dir: string
  config: AppConfig
  secrets_set: string[]
  pending_changes: string[]
  requires_restart: boolean
  backups: AdminBackup[]
  runtime: AdminRuntime
}

export interface AdminPutReply {
  ok: boolean
  changed?: string[]
  unchanged?: boolean
  speakers_changed?: { added: string[]; removed: string[]; modified: string[] }
  requires_restart: boolean
  backup?: string
  path?: string
  note?: string
}

export const adminGetConfig = () => request<AdminConfigReply>('/api/admin/config')
export const adminPutConfig = (body: Record<string, unknown>) =>
  request<AdminPutReply>('/api/admin/config', { method: 'PUT', body: JSON.stringify(body) }, LONG_TIMEOUT)
export const adminBackups = () =>
  request<{ ok: boolean; dir: string; backups: AdminBackup[] }>('/api/admin/backups')
export const adminRestore = (name: string) =>
  request<{ ok: boolean; restored: string; backup: string; changed: string[]; requires_restart: boolean }>(
    '/api/admin/config/restore',
    { method: 'POST', body: JSON.stringify({ name }) },
  )
export const adminRestart = () => request<{ ok: boolean; restarting: boolean }>('/api/admin/restart', { method: 'POST' })
export const adminLogs = (n = 200) => request<{ ok: boolean; lines: string[]; total: number }>(`/api/admin/logs?n=${n}`)

// ---------- 认证管理（网易云 & 小米云端）----------

export const logoutNetease = () => post<{ ok: boolean }>('/api/logout')

export const getXiaomiStatus = () => request<XiaomiAuthStatus>('/api/admin/auth/xiaomi/status')
export const logoutXiaomi = () => post<{ ok: boolean }>('/api/admin/auth/xiaomi/logout')
export const startXiaomiQR = () => post<XiaomiQRStart>('/api/admin/auth/xiaomi/qr')
export const checkXiaomiQR = (sessionId: string) =>
  request<XiaomiQRCheck>(`/api/admin/auth/xiaomi/qr/check?session_id=${encodeURIComponent(sessionId)}`)
export const getXiaomiDevices = () => request<{ ok: boolean; devices: XiaomiDevice[] }>('/api/admin/auth/xiaomi/devices')
export const checkXiaomiAuth = () =>
  post<{ ok: boolean; message: string; devices?: XiaomiDevice[] }>('/api/admin/auth/xiaomi/check')



export interface QrCheck {
  ok: boolean
  code: number
  message: string
  uid?: number
  nickname?: string
}

/** 返回 PNG 二进制，unikey 在响应头里 */
// ---------- 网易云登录 ----------

export async function loginQR(): Promise<{ blob: Blob; key: string }> {
  const res = await fetch('/api/login/qr', { cache: 'no-store' })
  if (!res.ok) throw new ApiError(res.status, '申请二维码失败，稍后再试')
  return { blob: await res.blob(), key: res.headers.get('X-Unikey') || '' }
}
export const checkLoginQR = (key: string) => request<QrCheck>(`/api/login/qr/check?key=${encodeURIComponent(key)}`)
export const loginWithCookie = (cookie: string) => post<Account>('/api/login/cookie', { cookie })
