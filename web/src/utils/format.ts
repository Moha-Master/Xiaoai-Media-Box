/** 毫秒 → `3:07`（超过一小时带小时） */
export function fmtMs(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) ms = 0
  const total = Math.floor(ms / 1000)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  const ss = String(s).padStart(2, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`
}

/** 秒 → `3:07`（网易云接口里 duration 是秒，但我们的 Track 已经统一成毫秒） */
export function fmtSec(sec: number): string {
  return fmtMs((sec || 0) * 1000)
}

/** 时间戳（秒）→ `11-25 10:36` */
export function fmtTime(unixSec: number): string {
  if (!unixSec) return '—'
  const d = new Date(unixSec * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** 运行时长 → `2小时13分` */
export function fmtUptime(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return '—'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (h > 0) return `${h} 小时 ${m} 分`
  if (m > 0) return `${m} 分 ${Math.floor(sec % 60)} 秒`
  return `${Math.floor(sec)} 秒`
}

/** 播放量 → `2.5亿` / `96万` / `123`（网易云口径，万位起省略） */
export function fmtCount(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return ''
  if (n >= 100000000) {
    const v = (n / 100000000).toFixed(1).replace(/\.0$/, '')
    return `${v}亿`
  }
  if (n >= 10000) return `${Math.round(n / 10000)}万`
  return String(n)
}

/** 字节数 → `1.4 KB` */
export function fmtBytes(n: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}
