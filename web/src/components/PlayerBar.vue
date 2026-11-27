<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import QueuePane from './QueuePane.vue'
import { useHashRoute } from '../composables/useHashRoute'
import { setBarHeight } from '../composables/useBarHeight'
import { player } from '../stores/player'
import { fmtMs } from '../utils/format'

const { go } = useHashRoute()
const showQueue = ref(false)

// 实测自身高度，布局预留（wrap 的 padding-bottom）跟着走
const barRoot = ref<HTMLElement | null>(null)
let ro: ResizeObserver | undefined
onMounted(() => {
  if (barRoot.value) setBarHeight(barRoot.value.offsetHeight)
  ro = new ResizeObserver((entries) => {
    const h = entries[0]?.borderBoxSize?.[0]?.blockSize || barRoot.value?.offsetHeight || 0
    if (h) setBarHeight(h)
  })
  if (barRoot.value) ro.observe(barRoot.value)
})
onUnmounted(() => ro?.disconnect())

const cur = computed(() => player.current)
const dur = computed(() => player.durationMs || cur.value?.duration_ms || 0)
const pos = computed(() => Math.min(player.positionMs, dur.value || player.positionMs))
// 设备不可达：整个播放器显示「未知」——时间用 --:--、进度条禁用并抹掉填充，
// 而不是把冻结的旧值当成真值继续画（那会让人以为"还在播到 3:12"）。
const offline = computed(() => player.unreachable)

// ---- 试听片段进度条（原曲时间轴）----
// 底槽代表原曲总长；可播区间用 accent 色带高亮、区间外压暗。
// v-slider 的轨道两端各让出半个 thumb（thumb-size=11 → 5.5px），overlay 用同样的
// calc 几何对齐：left = 5.5px + (100% - 11px) * pct。
const isTrial = computed(() => player.isTrial)
const bandStyle = computed(() => {
  if (!dur.value || !isTrial.value) return { display: 'none' }
  const d = dur.value
  return {
    left: `calc(5.5px + (100% - 11px) * ${(player.trialStartMs / d).toFixed(4)})`,
    width: `calc((100% - 11px) * ${((player.playableEndMs - player.trialStartMs) / d).toFixed(4)})`,
  }
})
const dimLeftStyle = computed(() => {
  if (!dur.value || !isTrial.value) return { display: 'none' }
  return { left: '5.5px', width: `calc((100% - 11px) * ${(player.trialStartMs / dur.value).toFixed(4)})` }
})
const dimRightStyle = computed(() => {
  if (!dur.value || !isTrial.value) return { display: 'none' }
  const e = player.playableEndMs / dur.value
  return {
    left: `calc(5.5px + (100% - 11px) * ${e.toFixed(4)})`,
    width: `calc((100% - 11px) * ${(1 - e).toFixed(4)})`,
  }
})
const trialTitle = computed(() =>
  isTrial.value
    ? `VIP 试听片段：${fmtMs(player.trialStartMs)}–${fmtMs(player.playableEndMs)}（原曲 ${fmtMs(dur.value)}）`
    : undefined,
)

// ---- 心动模式开关（一眼能看出开没开、为什么停）----
const hotOn = computed(() => !!player.hotmode?.on)
const hotBusy = computed(() => player.busy === '心动模式' || player.busy === '心动停止')
const hotTitle = computed(() => {
  const h = player.hotmode
  if (h?.on) return `心动模式：无限续杯中，已续 ${h.extended ?? 0} 次（点击关闭）`
  if (h?.stop_reason) return `心动已停止：${h.stop_reason}（点击重新开启）`
  return '心动模式：按当前曲无限续杯'
})
function toggleHotmode() {
  if (hotOn.value) player.actions.stopHotmode()
  else player.actions.startHotmode('current')
}

const modeIcon = computed(() => {
  if (player.repeat === 'none') return '$repeatOff'
  if (player.repeat === 'single') return '$repeatOne'
  return player.shuffle ? '$shuffle' : '$repeat'
})
const modeOn = computed(() => player.repeat !== 'list' || player.shuffle)
const modeTitle = computed(() => {
  if (player.repeat === 'none') return '不循环'
  if (player.repeat === 'single') return '单曲循环'
  return player.shuffle ? '随机播放' : '列表循环'
})
const volIcon = computed(() => (player.muted || player.volume === 0 ? '$volMute' : player.volume < 40 ? '$volLow' : '$volHigh'))
const busyPlay = computed(() => player.busy === '继续' || player.busy === '暂停')
</script>

<template>
  <div ref="barRoot" class="mb-bar">
    <div class="mb-bar-inner">
      <!-- 左：曲目信息（封面 = 圆形仿唱片，播放时旋转） -->
      <div class="mb-bar-col mb-bar-info">
        <div
          v-if="cur"
          class="bar-disc"
          :class="{ 'bar-disc--paused': !player.playing }"
          title="查看正在播放"
          @click="go('now')"
        >
          <v-img :src="cur.cover" cover class="bar-disc-img" />
          <div class="bar-disc-label"><div class="bar-disc-hole" /></div>
        </div>
        <div v-else class="mb-bar-placeholder">
          <v-icon icon="$musicNote" size="24" class="mb-dim" />
        </div>
        <div class="mb-bar-text">
          <div
            class="text-body-medium text-truncate"
            :class="{ 'mb-track-link': cur }"
            :title="cur ? `${cur.name} · 查看正在播放` : undefined"
            @click="cur && go('now')"
          >
            {{ cur ? cur.name : '没有播放中的内容' }}
            <v-chip v-if="player.hotmode?.on" size="x-small" variant="tonal" color="accent" class="ml-1">
              ♾ 心动
            </v-chip>
            <v-chip
              v-else-if="player.hotmode?.stop_reason"
              size="x-small"
              variant="tonal"
              color="secondary"
              class="ml-1"
              :title="`心动已停止：${player.hotmode.stop_reason}`"
            >
              ♾ 心动已停止
            </v-chip>
            <v-chip v-if="isTrial" size="x-small" variant="tonal" color="warning" class="ml-1" :title="trialTitle">
              试听
            </v-chip>
            <v-chip
              v-if="offline"
              size="x-small"
              variant="flat"
              color="error"
              class="ml-1"
              title="设备主动上报静默超时：状态、进度、歌词全部冻结显示为未知，恢复上报后自动继续"
            >
              设备离线
            </v-chip>
          </div>
          <div class="text-caption mb-dim text-truncate">
            <template v-if="cur">{{ cur.artists }}<span v-if="cur.album"> · {{ cur.album }}</span></template>
            <template v-else>去「首页」挑一首，或点上面的搜索</template>
          </div>
        </div>
      </div>

      <!-- 中：传输控制 + 进度（红心放在控制排最左） -->
      <div class="mb-bar-col mb-bar-center">
        <div class="d-flex align-center justify-center ga-1">
          <v-btn
            :icon="player.liked ? '$heart' : '$heartOutline'"
            size="small"
            variant="text"
            :color="player.liked ? 'error' : 'secondary'"
            :title="player.liked ? '取消红心' : '红心收藏'"
            :disabled="!cur"
            @click="player.actions.toggleLike()"
          />
          <v-btn icon="$prev" variant="text" size="small" title="上一首" :disabled="!cur" @click="player.actions.prev()" />
          <v-btn
            :icon="player.playing ? '$pause' : '$play'"
            variant="flat"
            rounded="circle"
            size="large"
            color="primary"
            :loading="busyPlay"
            :title="player.playing ? '暂停' : '播放'"
            @click="player.actions.toggle()"
          />
          <v-btn icon="$next" variant="text" size="small" title="下一首" :disabled="!cur" @click="player.actions.next()" />
          <v-btn icon="$stop" variant="text" size="small" title="停止并清空队列" :disabled="!cur" @click="player.actions.stop()" />
          <v-btn
            :icon="modeIcon"
            variant="text"
            size="small"
            :color="modeOn ? 'primary' : 'secondary'"
            :title="`${modeTitle}（点击切换）`"
            @click="player.actions.cycleMode()"
          />
          <v-btn
            icon="$hotmode"
            variant="text"
            size="small"
            :color="hotOn ? 'accent' : 'secondary'"
            :loading="hotBusy"
            :title="hotTitle"
            :disabled="!cur"
            @click="toggleHotmode()"
          />
          <!-- 移动端右侧状态列是隐藏的，队列入口放在控制排里 -->
          <v-btn icon="$queue" variant="text" size="small" title="播放队列" class="d-md-none" @click="showQueue = !showQueue" />
        </div>
        <div class="mb-bar-progress">
          <span class="text-caption mb-dim mb-mono">{{ offline ? '--:--' : fmtMs(pos) }}</span>
          <div class="mb-seek-wrap">
            <v-slider
              :model-value="dur ? (pos / dur) * 100 : 0"
              :max="100"
              :step="0.1"
              color="primary"
              density="compact"
              hide-details
              thumb-size="11"
              :disabled="offline"
              class="mb-seek"
              @update:model-value="(v: number) => dur && player.actions.seekTo(Math.round((v / 100) * dur))"
            />
            <!-- 可播区段 overlay：落在区间外压暗，区间内 accent 色带高亮 -->
            <div class="mb-seek-dim" :style="dimLeftStyle" />
            <div class="mb-seek-dim" :style="dimRightStyle" />
            <div class="mb-seek-band" :style="bandStyle" />
          </div>
          <span class="text-caption mb-dim mb-mono">{{ offline ? '--:--' : fmtMs(dur) }}</span>
        </div>
      </div>

      <!-- 右：音量 + 队列。SSE 断线时降级为音量按钮上的小红点（错误 snackbar / 侧栏状态仍在） -->
      <div class="mb-bar-col mb-bar-right">
        <div class="mb-vol-wrap">
          <v-btn
            :icon="volIcon"
            size="small"
            variant="text"
            :title="player.muted ? '取消静音' : '静音'"
            @click="player.actions.toggleMute()"
          />
          <span v-if="!player.online" class="mb-offline-dot" title="连接中断，轮询兜底中" />
        </div>
        <v-slider
          :model-value="player.muted ? 0 : player.volume"
          :max="100"
          :step="1"
          color="primary"
          density="compact"
          hide-details
          thumb-size="11"
          style="width: 110px; flex: none"
          @update:model-value="(v: number) => player.actions.setVolume(Math.round(v))"
        />
        <v-btn icon="$queue" size="small" variant="text" title="播放队列" @click="showQueue = !showQueue" />
      </div>
    </div>
  </div>

  <!-- 队列抽屉：右侧滑出，标题/清空/关闭在外层，列表由 QueuePane 提供 -->
  <v-navigation-drawer v-model="showQueue" location="right" temporary :width="380" color="surface">
    <div class="mb-drawer-body">
      <div class="mb-drawer-head">
        <v-icon icon="$queue" size="18" class="mb-dim" />
        <span class="text-body-large font-weight-medium">播放队列 · {{ player.queueTotal }} 首</span>
        <v-spacer />
        <v-btn
          size="small"
          variant="text"
          color="error"
          prepend-icon="$remove"
          :disabled="!player.hasQueue"
          @click="player.actions.clear()"
        >
          清空
        </v-btn>
        <v-btn icon="$close" size="small" variant="text" title="关闭" @click="showQueue = false" />
      </div>
      <v-divider />
      <QueuePane hide-head class="mb-drawer-list" />
      <v-divider />
      <div class="mb-drawer-foot">
        <v-btn block variant="tonal" color="primary" prepend-icon="$disc" @click="showQueue = false; go('now')">
          打开播放页
        </v-btn>
      </div>
    </div>
  </v-navigation-drawer>
</template>

<style scoped>
.mb-bar {
  /* 独立于 Vuetify layout 系统：钉死在视口底部、横跨全宽（含侧栏区域） */
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 500;
  display: block;
  background: rgb(var(--v-theme-surface));
  border-top: 1px solid rgba(255, 255, 255, 0.08);
  padding: 0 0 env(safe-area-inset-bottom);
}
.mb-bar-inner {
  max-width: 1500px;
  margin: 0 auto;
  padding: 8px 16px;
  display: grid;
  grid-template-columns: minmax(220px, 1.1fr) minmax(320px, 2fr) minmax(230px, 1fr);
  align-items: center;
  gap: 16px;
}
.mb-bar-info {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.mb-bar-text {
  min-width: 0;
  flex: 1;
}
.mb-track-link {
  cursor: pointer;
  transition: color 0.15s;
}
.mb-track-link:hover {
  color: rgb(var(--v-theme-primary));
}
.mb-bar-placeholder {
  width: 48px;
  height: 48px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgb(var(--v-theme-surface-variant));
}

/* ---- 圆形仿唱片封面：与沉浸页 disc 同思路（旋转 + 中心浅色标签 + 中孔），48px 简化版 ---- */
.bar-disc {
  position: relative;
  flex: none;
  width: 48px;
  height: 48px;
  border-radius: 50%;
  overflow: hidden;
  cursor: pointer;
  box-shadow:
    0 4px 14px rgba(0, 0, 0, 0.4),
    0 0 0 1px rgba(255, 255, 255, 0.1);
  animation: bar-disc-spin 18s linear infinite;
  animation-play-state: running;
  will-change: transform;
}
.bar-disc--paused {
  animation-play-state: paused;
}
@keyframes bar-disc-spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}
.bar-disc-img {
  position: absolute;
  inset: 0;
  border-radius: 50%;
}
.bar-disc-label {
  position: absolute;
  left: 50%;
  top: 50%;
  width: 20%;
  height: 20%;
  transform: translate(-50%, -50%);
  border-radius: 50%;
  background: rgba(238, 240, 244, 0.88);
  display: flex;
  align-items: center;
  justify-content: center;
}
.bar-disc-hole {
  width: 26%;
  height: 26%;
  border-radius: 50%;
  background: rgb(var(--v-theme-surface));
}

/* ---- 音量按钮上的离线小红点 ---- */
.mb-vol-wrap {
  position: relative;
  display: inline-flex;
}
.mb-offline-dot {
  position: absolute;
  top: 4px;
  right: 2px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: rgb(var(--v-theme-warning));
  pointer-events: none;
}
.mb-bar-center {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.mb-bar-progress {
  display: flex;
  align-items: center;
  gap: 8px;
}
/* 试听区段 overlay：与 v-slider 轨道几何对齐（轨道两端各让出半个 11px thumb） */
.mb-seek-wrap {
  position: relative;
  flex: 1;
  display: flex;
  min-width: 0;
}
.mb-seek {
  flex: 1;
  margin-top: 0;
}
.mb-seek-band,
.mb-seek-dim {
  position: absolute;
  top: 50%;
  transform: translateY(-50%);
  pointer-events: none;
}
.mb-seek-band {
  height: 7px;
  border-radius: 4px;
  background: rgb(var(--v-theme-accent));
  opacity: 0.55;
}
.mb-seek-dim {
  height: 3px;
  border-radius: 2px;
  background: rgba(0, 0, 0, 0.4);
}
.mb-bar-right {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  flex-wrap: nowrap;
}
@media (max-width: 960px) {
  .mb-bar-inner {
    grid-template-columns: 1fr;
    gap: 6px;
    padding: 8px 12px;
  }
  .mb-bar-right {
    display: none;
  }
}

/* ---- 队列抽屉：头/列表/底三段，列表填满剩余高度 ---- */
.mb-drawer-body {
  height: 100%;
  display: flex;
  flex-direction: column;
}
.mb-drawer-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 14px;
  flex: none;
}
.mb-drawer-list {
  flex: 1 1 auto;
  min-height: 0;
}
.mb-drawer-foot {
  padding: 10px 14px calc(10px + env(safe-area-inset-bottom));
  flex: none;
}
</style>
