<script setup lang="ts">
import { ref, watch } from 'vue'
import * as api from '../api/client'
import type { Track } from '../api/types'
import TrackRow from './TrackRow.vue'
import { player } from '../stores/player'

/**
 * 歌单 / 排行榜详情弹窗：两者都是「拉 /api/playlist/{id} 出曲目列表」，
 * 播放全部 / 入队直接把 playlist_id 交给后端解析，前端不用先等 tracks 拉完。
 */
const props = defineProps<{
  modelValue: boolean
  playlistId?: number | string | null
  title?: string
}>()
const emit = defineEmits<{ 'update:modelValue': [boolean] }>()

const loading = ref(false)
const err = ref('')
const tracks = ref<Track[]>([])

watch(
  () => [props.modelValue, props.playlistId] as const,
  async ([open, id]) => {
    if (!open || !id) return
    loading.value = true
    err.value = ''
    tracks.value = []
    try {
      tracks.value = (await api.getPlaylist(id)).tracks || []
    } catch (e) {
      err.value = (e as Error).message
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

function playAll() {
  if (props.playlistId) player.actions.playSource({ playlist_id: props.playlistId })
}
function enqueueAll() {
  if (props.playlistId) player.actions.enqueueSource({ playlist_id: props.playlistId })
}
</script>

<template>
  <v-dialog :model-value="modelValue" max-width="640" scrollable @update:model-value="emit('update:modelValue', $event)">
    <v-card variant="flat" color="surface">
      <v-card-title class="d-flex align-center ga-2 pr-2">
        <div class="min-w-0 flex-1 text-truncate">
          {{ title || '曲目列表' }}
          <span v-if="tracks.length" class="text-caption mb-dim ml-1">{{ tracks.length }} 首</span>
        </div>
        <v-btn size="small" variant="tonal" color="primary" prepend-icon="$play" :disabled="!tracks.length" @click="playAll">
          播放全部
        </v-btn>
        <v-btn size="small" variant="text" prepend-icon="$plus" :disabled="!tracks.length" title="整单加到队尾" @click="enqueueAll">
          入队
        </v-btn>
        <v-btn size="small" variant="text" icon="$close" @click="emit('update:modelValue', false)" />
      </v-card-title>
      <v-divider />
      <v-progress-linear v-if="loading" indeterminate height="2" color="primary" />
      <v-card-text class="pa-0">
        <div v-if="err" class="pa-4">
          <v-alert type="error" variant="tonal" density="compact">{{ err }}</v-alert>
        </div>
        <TrackRow
          v-for="(t, i) in tracks"
          v-else
          :key="`${t.id}-${i}`"
          :track="t"
          :playing="t.id === player.current?.id"
          @play="player.actions.playSource({ song_ids: [t.id] })"
          @enqueue="player.actions.enqueueSource({ song_ids: [t.id] })"
          @next="player.actions.enqueueSource({ song_ids: [t.id] }, true)"
        />
        <div v-if="!loading && !err && !tracks.length" class="pa-6 text-center mb-dim">这个歌单没有曲目</div>
      </v-card-text>
    </v-card>
  </v-dialog>
</template>
