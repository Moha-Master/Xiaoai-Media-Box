<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import type { Component } from 'vue'
import { useDisplay, useTheme } from 'vuetify'
import PlayerBar from './components/PlayerBar.vue'
import AdminView from './views/AdminView.vue'
import DeviceView from './views/DeviceView.vue'
import MusicView from './views/MusicView.vue'
import NowView from './views/NowView.vue'
import SearchView from './views/SearchView.vue'
import { useHashRoute, type RouteName } from './composables/useHashRoute'
import { barHeight } from './composables/useBarHeight'
import { player } from './stores/player'
import type { Speaker } from './api/types'

const { route, go } = useHashRoute()
const display = useDisplay()
const theme = useTheme()

const drawer = ref(display.mdAndUp.value)

const VIEWS: Record<RouteName, Component> = {
  music: MusicView,
  search: SearchView,
  now: NowView,
  settings: AdminView,
  device: DeviceView,
}
const TITLES: Record<RouteName, string> = {
  music: '网易云音乐',
  search: '搜索',
  now: '正在播放',
  settings: '设置',
  device: '设备调试',
}
const view = computed(() => VIEWS[route.value])
const title = computed(() => TITLES[route.value])

/** 侧栏音箱条目的状态文本：曲名优先，其次未知，最后空闲 */
function speakerStatus(sp: Speaker): string {
  // 设备不可达优先于一切：这时 sp.current 是"最后已知"，不能当成现状报出去
  if (sp.unreachable) return '设备离线 · 状态未知'
  if (sp.current) return sp.current
  if (sp.playing === undefined) return '状态未知'
  return '空闲'
}

// 换曲 / 播放态 / 可达性变化时刷新侧栏副标题：current/playing/unreachable 只在
// /api/speakers 里，SSE 帧先到，跟着补一拍列表刷新（noop 关回读时没有 playing 字段，只有曲名）
// unreachable 必须进 key：否则设备掉线后侧栏会一直挂着"正在播某首"直到下次换曲。
watch(
  () =>
    Object.entries(player.perSpeaker)
      .map(
        ([id, s]) =>
          `${id}:${s.queue.current?.id ?? ''}:${s.queue.playing ? 1 : 0}:${player.playing ? 1 : 0}:${s.unreachable ? 1 : 0}`,
      )
      .join('|'),
  () => player.loadSpeakers(),
)

// ---- 错误提示 ----
const showError = ref(false)
const errorText = ref('')
let errTimer: number | undefined
let offlineTimer: number | undefined

watch(
  () => player.error,
  (msg) => {
    if (!msg || msg === errorText.value) return
    errorText.value = msg
    showError.value = true
    if (errTimer) clearTimeout(errTimer)
    errTimer = window.setTimeout(() => (showError.value = false), 6500)
  },
)

onMounted(() => {
  player.start()
  document.addEventListener('visibilitychange', onVisible)
  // SSE 断了 EventSource 会自己重连；这里再补一层低频轮询，别让页面永远停在旧状态
  offlineTimer = window.setInterval(() => {
    if (!player.online && !document.hidden) player.loadState()
  }, 5000)
})

onUnmounted(() => {
  player.stop()
  document.removeEventListener('visibilitychange', onVisible)
  if (errTimer) clearTimeout(errTimer)
  if (offlineTimer) clearInterval(offlineTimer)
})

function onVisible() {
  if (!document.hidden) player.refreshAll()
}

function toggleTheme() {
  theme.global.name.value = theme.global.current.value.dark ? 'light' : 'dark'
}
</script>

<template>
  <v-app>
    <v-navigation-drawer
      v-model="drawer"
      :temporary="display.mdAndDown.value"
      :width="256"
      color="surface"
      class="mb-drawer-main"
    >
      <div class="d-flex flex-column" style="min-height: 100%">
        <div class="d-flex align-center ga-2 px-4 pt-4 pb-1">
          <v-icon icon="$music" color="primary" size="26" />
          <div class="text-no-wrap">
            <div class="text-body-large font-weight-medium">小爱盒子</div>
            <div class="text-caption mb-dim">xiaoai-mb</div>
          </div>
        </div>

        <v-divider class="mt-3" />

        <v-list density="compact" nav shaped>
          <v-list-item
            :active="route === 'music'"
            prepend-icon="$music"
            title="网易云音乐"
            value="music"
            @click="go('music')"
          />
          <v-list-item
            :active="route === 'settings'"
            prepend-icon="$settings"
            title="设置"
            value="settings"
            @click="go('settings')"
          />
        </v-list>

        <v-spacer />

        <v-divider />

        <div class="text-caption mb-dim px-4 pt-3 pb-1">音箱</div>
        <v-list density="compact" nav shaped class="pb-3">
          <v-list-item
            v-for="sp in player.speakers"
            :key="sp.id"
            :value="sp.id"
            :active="sp.id === player.speakerId"
            @click="player.speakerId = sp.id"
          >
            <template #prepend>
              <v-avatar size="22" :color="sp.playing ? 'accent' : 'surface-variant'">
                <v-icon size="14" :icon="sp.control === 'ssh' ? '$device' : '$musicNote'" />
              </v-avatar>
            </template>
            <v-list-item-title>{{ sp.name }}</v-list-item-title>
            <v-list-item-subtitle class="d-flex align-center text-truncate">
              <span v-if="sp.playing" class="mb-dot" />
              <span class="text-truncate">{{ speakerStatus(sp) }}</span>
            </v-list-item-subtitle>
          </v-list-item>
          <v-list-item v-if="!player.speakers.length">
            <v-list-item-subtitle>还没有音箱，去「设置」加一台</v-list-item-subtitle>
          </v-list-item>
        </v-list>
      </div>
    </v-navigation-drawer>

    <v-app-bar density="compact" color="surface" flat>
      <v-app-bar-nav-icon @click="drawer = !drawer" />
      <v-app-bar-title class="text-body-large font-weight-medium">{{ title }}</v-app-bar-title>
      <template #append>
        <v-chip v-if="player.busy" size="small" variant="tonal" color="primary" prepend-icon="$refresh">
          {{ player.busy }}
        </v-chip>
        <v-btn
          icon="$sun"
          size="small"
          variant="text"
          :title="theme.global.current.value.dark ? '切到浅色' : '切到深色'"
          @click="toggleTheme"
        />
      </template>
    </v-app-bar>

    <v-main>
      <component :is="view" />
    </v-main>

    <PlayerBar />

    <v-snackbar v-model="showError" color="error" location="bottom" :offset="barHeight + 12" timeout="6500">
      {{ errorText }}
      <template #actions>
        <v-btn variant="text" @click="showError = false">知道了</v-btn>
      </template>
    </v-snackbar>
  </v-app>
</template>

<style scoped>
/* 播放器钉在视口底部且横跨全宽，侧栏底边止于播放器上沿。
   Vuetify 会给 drawer 写内联 height/z-index，所以这里必须 !important 压过内联；
   但移动端 temporary 抽屉是 overlay，要保持原生层级，只改常驻那份。 */
.mb-drawer-main:not(.v-navigation-drawer--temporary) {
  height: calc(100% - var(--mb-bar-height, 84px)) !important;
  z-index: 1 !important;
}

/* 在播音箱的小绿点 */
.mb-dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: rgb(var(--v-theme-accent));
  margin-right: 6px;
  flex: none;
}
</style>
