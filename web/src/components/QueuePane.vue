<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { player } from '../stores/player'
import { fmtMs } from '../utils/format'
import type { Track } from '../api/types'

// 抽屉里由外层提供「播放队列 · N 首」标题栏，这里可以只出列表
const props = defineProps<{ hideHead?: boolean }>()

const tracks = computed<Track[]>(() => player.queue?.tracks || [])
const index = computed(() => player.queue?.index ?? player.state?.queue.index ?? 0)
const total = computed(() => player.queueTotal || tracks.value.length)

const dragFrom = ref(-1)
const dragOver = ref(-1)
const listEl = ref<HTMLElement | null>(null)

// 换曲后把当前行滚进视野（列表可能上千首，不然完全看不到在哪）
watch(
  () => index.value,
  async () => {
    await nextTick()
    listEl.value?.querySelector('[data-current="1"]')?.scrollIntoView({ block: 'nearest' })
  },
)

function onDrop(to: number) {
  if (dragFrom.value >= 0 && dragFrom.value !== to) player.actions.moveTo(dragFrom.value, to)
  resetDrag()
}

function resetDrag() {
  dragFrom.value = -1
  dragOver.value = -1
}

function nudge(i: number, delta: number) {
  const to = i + delta
  if (to < 0 || to >= total.value) return
  player.actions.moveTo(i, to)
}
</script>

<template>
  <div ref="listEl" class="mb-scroll mb-queue">
    <div v-if="!props.hideHead" class="mb-queue-head">
      <div class="d-flex align-center ga-2 flex-wrap">
        <v-icon size="18" icon="$queue" class="mb-dim" />
        <span class="text-body-medium">{{ player.queue?.source || '队列' }}</span>
        <span class="text-caption mb-dim mb-mono">{{ total }} 首</span>
        <v-chip v-if="player.queue?.shuffle" size="x-small" variant="tonal" color="primary">随机</v-chip>
        <v-chip v-if="player.hotmode?.on" size="x-small" variant="tonal" color="accent">
          ♾ 心动 · 已续杯 {{ player.hotmode.extended }} 次
        </v-chip>
        <v-chip
          v-else-if="player.hotmode?.stop_reason"
          size="x-small"
          variant="tonal"
          color="secondary"
          :title="`心动已停止：${player.hotmode.stop_reason}`"
        >
          ♾ 心动已停止
        </v-chip>
      </div>
      <div class="d-flex align-center ga-1">
        <v-btn size="small" variant="text" prepend-icon="$refresh" :loading="!player.queue" @click="player.loadQueue()">
          刷新
        </v-btn>
        <v-btn
          size="small"
          variant="text"
          color="error"
          prepend-icon="$remove"
          :disabled="!total"
          @click="player.actions.clear()"
        >
          清空
        </v-btn>
      </div>
    </div>

    <v-alert v-if="!total" type="info" variant="tonal" density="compact" class="ma-3">
      队列是空的。去「发现」里点歌单 / 每日推荐，或者用心动模式 ♾
    </v-alert>

    <div
      v-for="(t, i) in tracks"
      :key="`${t.id}-${i}`"
      class="mb-row"
      :class="{ 'mb-row--current': i === index, 'mb-row--over': dragOver === i, 'mb-dragging': dragFrom === i }"
      draggable="true"
      :data-current="i === index ? '1' : '0'"
      @dragstart="dragFrom = i"
      @dragover.prevent="dragOver = i"
      @dragleave="dragOver === i && (dragOver = -1)"
      @drop.prevent="onDrop(i)"
      @dragend="resetDrag"
      @dblclick="player.actions.jump(i)"
    >
      <div class="mb-row-idx mb-mono">
        <v-icon v-if="i === index && player.playing" size="16" color="accent" icon="$play" />
        <v-icon v-else-if="i === index" size="16" color="accent" icon="$pause" />
        <span v-else>{{ i + 1 }}</span>
      </div>

      <v-img :src="t.cover" cover class="mb-thumb" style="width: 34px; height: 34px" />

      <div class="mb-row-main min-w-0">
        <div class="text-body-medium text-truncate">
          {{ t.name }}
          <v-chip
            v-if="(t.trial_end_ms || 0) > 0 && (t.trial_end_ms || 0) < t.duration_ms"
            size="x-small"
            variant="tonal"
            color="warning"
            class="ml-1 mb-trial-chip"
            :title="`VIP 试听片段：${fmtMs(t.trial_start_ms || 0)}–${fmtMs(t.trial_end_ms || 0)}（原曲 ${fmtMs(t.duration_ms)}）`"
          >
            试听
          </v-chip>
        </div>
        <div class="text-caption mb-dim text-truncate">{{ t.artists }}<span v-if="t.album"> · {{ t.album }}</span></div>
      </div>

      <div class="mb-row-dur text-caption mb-dim mb-mono">{{ fmtMs(t.duration_ms) }}</div>

      <div class="mb-row-actions">
        <v-btn icon="$play" size="x-small" variant="text" title="播放这一首" @click.stop="player.actions.jump(i)" />
        <v-btn icon="$up" size="x-small" variant="text" title="上移" :disabled="i === 0" @click.stop="nudge(i, -1)" />
        <v-btn
          icon="$down"
          size="x-small"
          variant="text"
          title="下移"
          :disabled="i >= total - 1"
          @click.stop="nudge(i, 1)"
        />
        <v-btn
          icon="$remove"
          size="x-small"
          variant="text"
          color="error"
          title="从队列删除"
          @click.stop="player.actions.removeAt(i)"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.mb-thumb {
  flex: none;
  border-radius: 6px;
  background: rgb(var(--v-theme-surface-variant));
}

.mb-queue {
  /* 抽屉里 flex 填满剩余高度（F1 的 max-height 会让抽屉下部留白，已移除） */
  flex: 1 1 auto;
  min-height: 240px;
  padding-bottom: 12px;
}
.mb-queue-head {
  position: sticky;
  top: 0;
  z-index: 1;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 10px 14px;
  background: rgb(var(--v-theme-surface));
  border-bottom: 1px solid rgba(255, 255, 255, 0.07);
  flex-wrap: wrap;
}
.mb-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 14px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  cursor: grab;
}
.mb-row:hover {
  background: rgba(255, 255, 255, 0.04);
}
.mb-row--current {
  background: rgba(78, 161, 255, 0.1);
  box-shadow: inset 3px 0 0 rgb(var(--v-theme-accent));
}
.mb-row--over {
  box-shadow: inset 0 2px 0 rgb(var(--v-theme-primary));
}
.mb-row-idx {
  width: 26px;
  text-align: right;
  font-size: 12px;
  opacity: 0.7;
  flex: none;
}
.mb-row-main {
  flex: 1;
  min-width: 0;
}
.mb-row-dur {
  flex: none;
  width: 48px;
  text-align: right;
}
/* 试听 chip 压扁一点，别把行高撑起来 */
.mb-trial-chip {
  --v-chip-height: 16px;
  font-size: 10px;
}
.mb-row-actions {
  display: flex;
  gap: 2px;
  flex: none;
  opacity: 0;
}
.mb-row:hover .mb-row-actions,
.mb-row--current .mb-row-actions {
  opacity: 1;
}
@media (max-width: 720px) {
  .mb-row-dur {
    display: none;
  }
  .mb-row-actions {
    opacity: 1;
  }
}
</style>
