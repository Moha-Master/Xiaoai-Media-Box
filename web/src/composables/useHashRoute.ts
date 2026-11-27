import { ref } from 'vue'

/**
 * 五个页面（音乐 / 搜索 / 正在播放 / 设置 / 设备调试）不值得引入 vue-router：
 * 用 hash 切换既省一个依赖，也让 Go 那边不需要为深链接做 history 回退。
 */
const KNOWN = ['music', 'search', 'now', 'settings', 'device'] as const
export type RouteName = (typeof KNOWN)[number]

// 旧路由别名：#/player、#/admin 继续可用（书签 / 旧入口）
const ALIAS: Record<string, RouteName> = { player: 'music', admin: 'settings' }

// 路由名 → hash 路径。now 是 music 下的沉浸播放子页，hash 用 music/now。
const PATHS: Record<RouteName, string> = {
  music: 'music',
  search: 'search',
  now: 'music/now',
  settings: 'settings',
  device: 'device',
}

function parse(seg: string): RouteName | null {
  const aliased = ALIAS[seg]
  if (aliased) return aliased
  return (KNOWN as readonly string[]).includes(seg) ? (seg as RouteName) : null
}

/** 当前 hash 的 query 部分（#/search?q=xx → URLSearchParams('q=xx')），随 hashchange 更新 */
export const routeQuery = ref<URLSearchParams>(new URLSearchParams())

function current(): RouteName {
  const raw = location.hash.replace(/^#\/?/, '')
  const [pathPart, queryPart] = raw.split('?')
  routeQuery.value = new URLSearchParams(queryPart || '')
  const h = pathPart
  if (h === 'music/now') return 'now'
  const fromHash = parse(h.split('/')[0])
  if (fromHash) return fromHash
  // 也认干净的路径（/settings、/music/now）：Go 对未知路径一律回退到入口页，深链接不会 404
  const p = location.pathname.replace(/^\/+/, '').replace(/\/+$/, '')
  if (p === 'music/now') return 'now'
  const fromPath = parse(p.split('/')[0])
  if (fromPath) return fromPath
  return 'music'
}

const route = ref<RouteName>(current())

function onHashChange() {
  route.value = current()
}

if (typeof window !== 'undefined') {
  window.addEventListener('hashchange', onHashChange)
}

export function useHashRoute() {
  // 注意：hashchange 监听是模块级单例，绝不能在组件 unmount 时 removeEventListener——
  // 任意一个调用过 useHashRoute 的组件卸载都会把唯一的监听摘掉，
  // 导致之后所有 hash 跳转（浏览器前进/后退、手改 URL、go 之外的路径）全部失灵。
  const go = (name: RouteName, query?: Record<string, string>) => {
    let target = `#/${PATHS[name]}`
    if (query) {
      const qs = new URLSearchParams(query).toString()
      if (qs) target += `?${qs}`
      // hash 没变时（比如重复提交同一个搜索词）不会触发 hashchange，手动同步
      routeQuery.value = new URLSearchParams(qs)
    } else {
      routeQuery.value = new URLSearchParams()
    }
    if (location.hash !== target) location.hash = target
    route.value = name
  }
  return { route, routeQuery, go }
}
