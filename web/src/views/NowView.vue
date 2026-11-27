<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useTheme } from 'vuetify'
import { getPalette } from '../api/client'
import { useHashRoute } from '../composables/useHashRoute'
import { player } from '../stores/player'
import { fmtMs } from '../utils/format'

const { go } = useHashRoute()
const theme = useTheme()

// ---- 取色背景 ----
// 与后端 defaultPalette（internal/app/recommend.go）保持一致的蓝灰系兜底。
const FALLBACK = { vibrant: '#4ea1ff', dark: '#1a2433', muted: '#2a3542' }
const vibrant = ref(FALLBACK.vibrant)
const palDark = ref(FALLBACK.dark)

/** 渐变收尾色 = 播放器栏 surface，跟随亮暗主题（衔接处无色带） */
const surfaceColor = computed(() => theme.global.current.value.colors.surface || '#171c23')

/** hex → rgb，解析失败返回 null */
function hexRgb(hex: string): [number, number, number] | null {
  const m = /^#?([0-9a-f]{6})$/i.exec((hex || '').trim())
  if (!m) return null
  const n = parseInt(m[1], 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

/** 调暗 factor（0.7 = 暗 30%） */
function darken(hex: string, factor: number): string {
  const c = hexRgb(hex)
  if (!c) return hex
  return `rgb(${c.map((v) => Math.round(v * factor)).join(',')})`
}

// 渐变不能靠 transition: background 平滑过渡（多数浏览器不插值 gradient），
// 用两层同尺寸背景交叉淡入：新渐变画到隐藏层 → 翻转 opacity，0.8s 交叉过渡。
const bgA = ref('')
const bgB = ref('')
const flip = ref(false)

function gradientOf(v: string, d: string): string {
  return `linear-gradient(180deg, ${darken(v, 0.7)} 0%, ${d} 55%, ${surfaceColor.value} 100%)`
}

let palSeq = 0
watch(
  () => player.current?.id,
  async (id) => {
    if (!id) return
    const seq = ++palSeq
    try {
      const p = await getPalette(id)
      if (seq !== palSeq) return // 换曲竞态：只认最后一单
      const next = gradientOf(p.vibrant || FALLBACK.vibrant, p.dark || FALLBACK.dark)
      vibrant.value = p.vibrant || FALLBACK.vibrant
      palDark.value = p.dark || FALLBACK.dark
      if (!bgA.value && !bgB.value) {
        bgA.value = next // 首次直接铺底，不做交叉
        flip.value = true
      } else if (flip.value) {
        bgB.value = next
        flip.value = false
      } else {
        bgA.value = next
        flip.value = true
      }
    } catch {
      /* 静默：保留当前颜色（后端失败也会给 fallback 调色板） */
    }
  },
  { immediate: true },
)

// 切亮暗主题时，用当前色 + 新 surface 重画正在显示的那层渐变
watch(
  () => theme.global.name.value,
  () => {
    const next = gradientOf(vibrant.value, palDark.value)
    if (flip.value) bgA.value = next
    else bgB.value = next
  },
)

// ---- 唱片 ----
const cur = computed(() => player.current)
const hotBusy = computed(() => player.busy === '心动模式' || player.busy === '心动停止')
const hotOn = computed(() => !!player.hotmode?.on)

// ---- 进度条（原曲时间轴 + 试听区段高亮）----
const dur = computed(() => player.durationMs || cur.value?.duration_ms || 0)
const pos = computed(() => Math.min(player.positionMs, dur.value || player.positionMs))
// 设备不可达：整个页面按「未知」显示——时间 --:--、进度条清空且不可点、
// 歌词区换成离线提示。冻结值不是真值，画出来就是谎报进度。
const offline = computed(() => player.unreachable)
const isTrial = computed(() => player.isTrial)
const bandStyle = computed(() => {
  if (!dur.value || !isTrial.value) return { display: 'none' }
  const d = dur.value
  return {
    left: `${((player.trialStartMs / d) * 100).toFixed(3)}%`,
    width: `${(((player.playableEndMs - player.trialStartMs) / d) * 100).toFixed(3)}%`,
  }
})
const dimLeftStyle = computed(() => {
  if (!dur.value || !isTrial.value) return { display: 'none' }
  return { width: `${((player.trialStartMs / dur.value) * 100).toFixed(3)}%` }
})
const dimRightStyle = computed(() => {
  if (!dur.value || !isTrial.value) return { display: 'none' }
  const e = (player.playableEndMs / dur.value) * 100
  return { left: `${e.toFixed(3)}%`, width: `${(100 - e).toFixed(3)}%` }
})
/** 点击进度条：原曲轴换算 + 钳进可播区间（服务端也会钳一次） */
function onSeekClick(e: MouseEvent) {
  if (!dur.value || offline.value) return
  const el = e.currentTarget as HTMLElement
  const rect = el.getBoundingClientRect()
  const pct = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
  player.actions.seekTo(Math.round(pct * dur.value))
}
const trialTitle = computed(() =>
  isTrial.value
    ? `VIP 试听片段：${fmtMs(player.trialStartMs)}–${fmtMs(player.playableEndMs)}（原曲 ${fmtMs(dur.value)}）`
    : undefined,
)

function toggleHotmode() {
  if (hotOn.value) player.actions.stopHotmode()
  else player.actions.startHotmode('current')
}

// ---- 歌词 ----
const lines = computed(() => player.lyric?.lines || [])
const active = computed(() => {
  const pos = player.positionMs
  let i = -1
  for (let k = 0; k < lines.value.length; k++) {
    if (lines.value[k].t_ms <= pos + 300) i = k
    else break
  }
  return i
})

/** 按离当前行的距离衰减透明度 */
function lineOpacity(i: number): number {
  const d = Math.abs(i - active.value)
  if (d === 0) return 1
  return Math.max(0.18, 0.55 - 0.12 * (d - 1))
}

const lyricEl = ref<HTMLElement | null>(null)
const userHold = ref(false)
let holdTimer: number | undefined

/** 用户手动滚动（滚轮/触摸/按住滚动条）后 5 秒内不自动跟随。
 *  pointerdown 落在歌词行上（点击 seek）不算手动滚动。 */
function onUserScrollIntent(e?: Event) {
  if (!lines.value.length) return
  if (e && e.type === 'pointerdown') {
    const t = e.target as HTMLElement
    if (t?.closest?.('.now-lyric-line')) return
  }
  userHold.value = true
  if (holdTimer) clearTimeout(holdTimer)
  holdTimer = window.setTimeout(() => (userHold.value = false), 5000)
}

async function scrollToActive() {
  await nextTick()
  lyricEl.value?.querySelector('[data-on="1"]')?.scrollIntoView({ block: 'center', behavior: 'smooth' })
}

function resumeFollow() {
  userHold.value = false
  if (holdTimer) clearTimeout(holdTimer)
  scrollToActive()
}

// 进度驱动的行变化才滚动；换歌（lines 整体更换）也回正
watch(active, () => {
  if (!userHold.value && active.value >= 0) scrollToActive()
})
watch(
  () => player.current?.id,
  () => {
    userHold.value = false
    if (holdTimer) clearTimeout(holdTimer)
    scrollToActive()
  },
)
onMounted(scrollToActive)
</script>

<template>
  <div class="now-root">
    <!-- 取色背景：两层交叉淡入，内容层浮在上面 -->
    <div class="now-bg" aria-hidden="true">
      <div class="now-bg-layer" :class="{ 'now-bg-layer--on': flip }" :style="{ background: bgA }" />
      <div class="now-bg-layer" :class="{ 'now-bg-layer--on': !flip }" :style="{ background: bgB }" />
    </div>

    <div class="now-grid">
      <!-- 返回首页：内容区右上角悬浮 -->
      <v-btn class="now-back" icon="$down" variant="text" size="large" title="返回首页" @click="go('music')" />

      <!-- 左：唱片 + 曲目信息 + 操作 -->
      <div class="now-left mb-scroll">
        <button class="disc-btn" :title="player.playing ? '点击暂停' : '点击播放'" :disabled="!cur" @click="player.actions.toggle()">
          <div class="disc" :class="{ 'disc--paused': !player.playing }">
            <v-img v-if="cur?.cover" :src="cur.cover" cover class="disc-img" />
            <div v-else class="disc-img disc-img--empty d-flex align-center justify-center">
              <v-icon icon="$disc" size="46" class="mb-dim" />
            </div>
            <div class="disc-label"><div class="disc-hole" /></div>
          </div>
          <div class="disc-hover">
            <v-icon :icon="player.playing ? '$pause' : '$play'" size="34" />
            <span>{{ player.playing ? '点击暂停' : '点击播放' }}</span>
          </div>
        </button>

        <div class="now-meta">
          <div class="now-title">
            {{ cur ? cur.name : '没有播放中的歌曲' }}
            <v-chip v-if="isTrial" size="x-small" variant="tonal" color="warning" class="ml-2" :title="trialTitle">
              试听
            </v-chip>
            <v-chip
              v-if="offline"
              size="x-small"
              variant="flat"
              color="error"
              class="ml-2"
              title="设备主动上报静默超时，状态与进度冻结为未知"
            >
              设备离线 · 状态未知
            </v-chip>
          </div>
          <div class="now-sub">
            <template v-if="cur">{{ cur.artists }}<span v-if="cur.album"> · {{ cur.album }}</span></template>
            <template v-else>去「首页」或搜索挑一首</template>
          </div>
          <!-- 进度条：底槽 = 原曲总长，试听区段高亮、区间外压暗（时间轴 = 原曲轴） -->
          <div v-if="cur" class="now-progress">
            <span class="mb-mono">{{ offline ? '--:--' : fmtMs(pos) }}</span>
            <div class="now-seek" :class="{ 'now-seek--off': offline }" @click="onSeekClick">
              <div
                v-if="!offline"
                class="now-seek-fill"
                :style="{ width: dur ? `${((pos / dur) * 100).toFixed(2)}%` : '0' }"
              />
              <div class="now-seek-dim" :style="dimLeftStyle" />
              <div class="now-seek-dim" :style="dimRightStyle" />
              <div class="now-seek-band" :style="bandStyle" />
            </div>
            <span class="mb-mono">{{ offline ? '--:--' : fmtMs(dur) }}</span>
          </div>
          <div v-if="player.hotmode?.on" class="now-hot">
            <v-icon icon="$hotmode" size="14" />
            ♾ 心动续杯中 · 已续 {{ player.hotmode.extended }} 次
          </div>
          <div v-else-if="player.hotmode?.stop_reason" class="now-hot now-hot--off" :title="player.hotmode.stop_reason">
            <v-icon icon="$hotmode" size="14" />
            心动已停止：{{ player.hotmode.stop_reason }}
          </div>
        </div>

        <div class="now-actions">
          <v-btn
            :icon="player.liked ? '$heart' : '$heartOutline'"
            variant="tonal"
            :color="player.liked ? 'error' : 'secondary'"
            :title="player.liked ? '取消红心' : '红心收藏'"
            :disabled="!cur"
            @click="player.actions.toggleLike()"
          />
          <v-btn
            icon="$hotmode"
            variant="tonal"
            :color="hotOn ? 'accent' : 'secondary'"
            :title="hotOn ? `心动模式：无限续杯中，已续 ${player.hotmode?.extended ?? 0} 次（点击关闭）` : '以当前曲为种子无限续杯'"
            :disabled="!cur"
            :loading="hotBusy"
            @click="toggleHotmode"
          />
        </div>
      </div>

      <!-- 右：歌词流 -->
      <div class="now-right">
        <div v-if="!cur" class="now-lyric-empty">
          <v-icon icon="$disc" size="40" class="mb-dim" />
          <div class="mt-3">没有播放中的歌曲</div>
        </div>
        <div v-else-if="offline" class="now-lyric-empty">
          <v-icon icon="$musicNote" size="40" class="mb-dim" />
          <div class="mt-3 text-body-large">设备离线，歌词未知</div>
          <div class="text-caption mb-dim mt-1">音箱恢复上报后自动继续（不会拿冻结位置硬猜行号）</div>
        </div>
        <div v-else-if="!lines.length" class="now-lyric-empty">
          <v-icon icon="$musicNote" size="40" class="mb-dim" />
          <div class="mt-3 text-body-large">暂无歌词</div>
          <div v-if="cur.album" class="text-caption mb-dim mt-1">专辑 · {{ cur.album }}</div>
        </div>
        <template v-else>
          <div ref="lyricEl" class="mb-scroll now-lyric" @wheel.passive="onUserScrollIntent($event)" @touchstart.passive="onUserScrollIntent($event)" @pointerdown="onUserScrollIntent($event)">
            <div
              v-for="(l, i) in lines"
              :key="i"
              class="now-lyric-line"
              :class="{ 'now-lyric-line--on': i === active }"
              :style="{ opacity: lineOpacity(i) }"
              :data-on="i === active ? '1' : '0'"
              role="button"
              tabindex="0"
              @click="player.actions.seekTo(l.t_ms)"
              @keyup.enter="player.actions.seekTo(l.t_ms)"
            >
              <span class="now-lyric-time mb-mono">{{ fmtMs(l.t_ms) }}</span>
              <span class="now-lyric-text">{{ l.text }}</span>
            </div>
          </div>
          <v-btn v-if="userHold" class="now-lyric-back" size="small" variant="flat" color="primary" rounded="pill" prepend-icon="$next" @click="resumeFollow">
            回到当前歌词
          </v-btn>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.now-root {
  position: relative;
  height: 100%;
  overflow: hidden;
  /* 兜底底色：100% 处用主题 surface，与底部播放器栏同色（渐变层很快盖上来） */
  background: linear-gradient(180deg, #17202c 0%, #1a2433 55%, rgb(var(--v-theme-surface)) 100%);
  color: #fff;
}
.now-bg {
  position: absolute;
  inset: 0;
}
.now-bg-layer {
  position: absolute;
  inset: 0;
  opacity: 0;
  transition: opacity 0.8s ease;
}
.now-bg-layer--on {
  opacity: 1;
}
.now-grid {
  position: relative;
  z-index: 1;
  height: 100%;
  display: grid;
  grid-template-columns: 2fr 3fr; /* 左 40% / 右 60% */
  gap: 8px;
  padding: 20px 24px 20px;
}

/* ---- 返回首页按钮 ---- */
.now-back {
  position: absolute;
  top: 10px;
  right: 18px;
  z-index: 2;
}

/* ---- 左栏 ---- */
.now-left {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 14px;
  min-height: 0;
  padding: 8px;
}

.disc-btn {
  position: relative;
  width: min(340px, 80%, 42vh);
  aspect-ratio: 1;
  padding: 0;
  border: 0;
  background: none;
  border-radius: 50%;
  cursor: pointer;
  flex: none;
}
.disc-btn:disabled {
  cursor: default;
}
.disc {
  position: absolute;
  inset: 0;
  border-radius: 50%;
  overflow: hidden;
  /* 细微高光环 + 投影 */
  box-shadow:
    0 16px 48px rgba(0, 0, 0, 0.55),
    0 0 0 1px rgba(255, 255, 255, 0.1),
    0 0 30px rgba(255, 255, 255, 0.07);
  animation: disc-spin 18s linear infinite;
  animation-play-state: running;
  will-change: transform;
}
.disc--paused {
  animation-play-state: paused;
}
@keyframes disc-spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}
.disc-img {
  position: absolute;
  inset: 0;
  border-radius: 50%;
}
.disc-img--empty {
  background: rgba(255, 255, 255, 0.06);
}
/* 仿唱片标签：浅色圆心 + 中孔 */
.disc-label {
  position: absolute;
  left: 50%;
  top: 50%;
  width: 18%;
  height: 18%;
  transform: translate(-50%, -50%);
  border-radius: 50%;
  background: rgba(238, 240, 244, 0.88);
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 0 10px rgba(0, 0, 0, 0.4);
}
.disc-hole {
  width: 26%;
  height: 26%;
  border-radius: 50%;
  background: #10131a;
}
.disc-hover {
  position: absolute;
  inset: 0;
  border-radius: 50%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  font-size: 13px;
  color: #fff;
  background: rgba(0, 0, 0, 0.45);
  opacity: 0;
  transition: opacity 0.18s ease;
}
.disc-btn:not(:disabled):hover .disc-hover {
  opacity: 1;
}

.now-meta {
  text-align: center;
  max-width: 100%;
  min-width: 0;
}
.now-title {
  font-size: 22px;
  font-weight: 700;
  line-height: 1.3;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}
.now-sub {
  margin-top: 4px;
  font-size: 14px;
  opacity: 0.72;
}
.now-hot {
  margin-top: 8px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  padding: 3px 10px;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.1);
  opacity: 0.9;
  max-width: 100%;
}
.now-hot--off {
  opacity: 0.65;
}
/* 进度条：底槽代表原曲总长；试听区段 accent 高亮、区间外压暗 */
.now-progress {
  margin-top: 10px;
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
  opacity: 0.9;
}
.now-seek {
  position: relative;
  flex: 1;
  min-width: 0;
  height: 6px;
  border-radius: 3px;
  background: rgba(255, 255, 255, 0.22);
  cursor: pointer;
  overflow: hidden;
}
/* 设备离线：进度条只作为"底槽"存在，不可点、不画任何填充 */
.now-seek--off {
  cursor: not-allowed;
  opacity: 0.45;
}
.now-seek-fill {
  position: absolute;
  inset: 0 auto 0 0;
  background: rgb(var(--v-theme-primary));
  border-radius: 3px;
}
.now-seek-band {
  position: absolute;
  top: 0;
  bottom: 0;
  background: rgb(var(--v-theme-accent));
  opacity: 0.55;
  pointer-events: none;
}
.now-seek-dim {
  position: absolute;
  top: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.45);
  pointer-events: none;
}
.now-actions {
  display: flex;
  gap: 12px;
}

/* ---- 右栏：歌词 ---- */
.now-right {
  position: relative;
  min-width: 0;
  min-height: 0;
  display: flex;
}
.now-lyric {
  flex: 1;
  min-height: 0;
  padding: 24px 12px 45vh;
  /* 上下渐隐遮罩 */
  mask-image: linear-gradient(180deg, transparent 0, #000 9%, #000 91%, transparent 100%);
  -webkit-mask-image: linear-gradient(180deg, transparent 0, #000 9%, #000 91%, transparent 100%);
}
.now-lyric-line {
  display: flex;
  gap: 12px;
  padding: 8px 12px;
  border-radius: 10px;
  cursor: pointer;
  line-height: 1.7;
  font-size: 16px;
  transition: opacity 0.25s ease;
}
.now-lyric-line:hover {
  background: rgba(255, 255, 255, 0.08);
}
.now-lyric-time {
  flex: none;
  width: 44px;
  padding-top: 3px;
  font-size: 12px;
  opacity: 0.5;
}
.now-lyric-line--on {
  font-size: 20px;
  font-weight: 700;
  color: v-bind('vibrant');
}
.now-lyric-line--on .now-lyric-time {
  opacity: 0.8;
}
.now-lyric-empty {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  text-align: center;
  opacity: 0.75;
}
.now-lyric-back {
  position: absolute;
  left: 50%;
  bottom: 18px;
  transform: translateX(-50%);
  box-shadow: 0 8px 20px rgba(0, 0, 0, 0.4);
}

/* ---- 窄屏：上下堆叠 ---- */
@media (max-width: 959px) {
  .now-grid {
    grid-template-columns: 1fr;
    grid-template-rows: auto 1fr;
    padding: 14px 12px 12px;
    gap: 6px;
  }
  .now-left {
    justify-content: flex-start;
    gap: 10px;
    padding: 4px;
    overflow: visible;
  }
  .disc-btn {
    width: min(180px, 44vw);
  }
  .now-title {
    font-size: 17px;
    -webkit-line-clamp: 1;
  }
  .now-sub {
    font-size: 12px;
  }
  .now-lyric {
    padding: 8px 8px 30vh;
  }
  .now-lyric-line {
    font-size: 15px;
  }
  .now-lyric-line--on {
    font-size: 18px;
  }
}
</style>
