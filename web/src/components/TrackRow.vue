<script setup lang="ts">
import { fmtMs } from '../utils/format'
import type { Track } from '../api/types'

defineProps<{ track: Track; playing?: boolean }>()
const emit = defineEmits<{ play: []; enqueue: []; next: [] }>()
</script>

<template>
  <div class="mb-trow" :class="{ 'mb-trow--on': playing }">
    <v-img :src="track.cover" cover class="mb-thumb" style="width: 36px; height: 36px" />
    <div class="min-w-0 flex-1">
      <div class="text-body-medium text-truncate">
        {{ track.name }}
        <v-chip
          v-if="(track.trial_end_ms || 0) > 0 && (track.trial_end_ms || 0) < track.duration_ms"
          size="x-small"
          variant="tonal"
          color="warning"
          class="ml-1 mb-trial-chip"
          :title="`VIP 试听片段：${fmtMs(track.trial_start_ms || 0)}–${fmtMs(track.trial_end_ms || 0)}（原曲 ${fmtMs(track.duration_ms)}）`"
        >
          试听
        </v-chip>
      </div>
      <div class="text-caption mb-dim text-truncate">
        {{ track.artists }}<span v-if="track.album"> · {{ track.album }}</span>
      </div>
    </div>
    <div class="text-caption mb-dim mb-mono flex-none">{{ fmtMs(track.duration_ms) }}</div>
    <div class="d-flex ga-1 flex-none">
      <v-btn size="x-small" variant="text" icon="$play" title="立即播放（替换队列）" @click="emit('play')" />
      <v-btn size="x-small" variant="text" icon="$plus" title="加到队尾" @click="emit('enqueue')" />
      <v-btn size="x-small" variant="text" icon="$playlist" title="下一首播放" @click="emit('next')" />
    </div>
  </div>
</template>

<style scoped>
.mb-thumb {
  flex: none;
  border-radius: 6px;
  background: rgb(var(--v-theme-surface-variant));
}

.mb-trow {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 14px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
}
.mb-trow:hover {
  background: rgba(255, 255, 255, 0.04);
}
.mb-trow--on {
  background: rgba(78, 161, 255, 0.1);
}
.mb-trial-chip {
  --v-chip-height: 16px;
  font-size: 10px;
}
</style>
