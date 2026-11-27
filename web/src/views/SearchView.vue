<script setup lang="ts">
import { ref, watch } from 'vue'
import * as api from '../api/client'
import type { SearchType } from '../api/client'
import type { SearchAlbum, SearchAllReply, SearchArtist, SearchPlaylist, Track } from '../api/types'
import TrackListDialog from '../components/TrackListDialog.vue'
import TrackRow from '../components/TrackRow.vue'
import { useHashRoute } from '../composables/useHashRoute'
import { player } from '../stores/player'
import { fmtCount } from '../utils/format'

const { routeQuery, go } = useHashRoute()

const TABS: { value: SearchType; label: string; icon: string }[] = [
  { value: 'song', label: '单曲', icon: '$song' },
  { value: 'playlist', label: '歌单', icon: '$playlist' },
  { value: 'artist', label: '歌手', icon: '$account' },
  { value: 'album', label: '专辑', icon: '$album' },
]

const q = ref('')
const type = ref<SearchType>('song')
const searched = ref('') // 最近一次真正提交的词
const loading = ref(false)
const tracks = ref<Track[]>([])
const playlists = ref<SearchPlaylist[]>([])
const artists = ref<SearchArtist[]>([])
const albums = ref<SearchAlbum[]>([])
const hot = ref<string[]>([])

api.getSearchHot().then((r) => (hot.value = (r.searches || []).slice(0, 10))).catch(() => {})

function submit() {
  const t = q.value.trim()
  if (!t) return
  go('search', { q: t })
}

function searchHot(word: string) {
  q.value = word
  go('search', { q: word })
}

/** tab 结果缓存：q+type → 响应。切 tab 直接回填缓存，换词才重新请求 */
const cache = new Map<string, SearchAllReply>()

function applyResult(r: SearchAllReply) {
  tracks.value = r.tracks || []
  playlists.value = r.playlists || []
  artists.value = r.artists || []
  albums.value = r.albums || []
}

async function ensure(t: SearchType) {
  const term = searched.value
  if (!term) return
  const key = `${term}\u0000${t}`
  const hit = cache.get(key)
  if (hit) {
    applyResult(hit)
    return
  }
  loading.value = true
  try {
    const r = await api.searchAll(term, t, 30)
    cache.set(key, r)
    applyResult(r)
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    loading.value = false
  }
}

// hash 里的 ?q= 变了就重搜（含首次带词直达）；同词不重复请求
watch(
  () => routeQuery.value.get('q'),
  (nq) => {
    const term = (nq || '').trim()
    if (!term) return
    q.value = term
    if (searched.value === term) return
    searched.value = term
    ensure(type.value)
  },
  { immediate: true },
)

watch(type, (t) => ensure(t))

function playAllSongs() {
  player.actions.playSource({ song_ids: tracks.value.map((t) => t.id) })
}

// ---- 歌单详情弹窗 ----
const detailOpen = ref(false)
const detail = ref<{ id: number | string; name: string } | null>(null)
function openPl(p: SearchPlaylist) {
  detail.value = { id: p.id, name: p.name }
  detailOpen.value = true
}

function playAlbum(a: SearchAlbum) {
  player.actions.playSource({ album_id: a.id })
}
</script>

<template>
  <div class="mb-search">
    <div class="search-head">
      <div class="search-row">
        <v-btn icon="$down" variant="text" size="large" title="返回首页" @click="go('music')" />
        <v-text-field
          v-model="q"
          placeholder="搜索歌曲 / 歌单 / 歌手 / 专辑"
          prepend-inner-icon="$search"
          density="comfortable"
          variant="solo-filled"
          flat
          hide-details
          clearable
          rounded="lg"
          class="search-field"
          @keyup.enter="submit"
          @click:prepend-inner="submit"
        />
      </div>
      <div v-if="hot.length" class="hot-chips">
        <v-chip
          v-for="w in hot"
          :key="w"
          size="small"
          variant="tonal"
          color="surface-variant"
          class="hot-chip"
          @click="searchHot(w)"
        >
          {{ w }}
        </v-chip>
      </div>
    </div>

    <v-tabs v-model="type" density="comfortable" color="primary" slider-size="3">
      <v-tab v-for="t in TABS" :key="t.value" :value="t.value">
        <v-icon :icon="t.icon" size="18" start />
        {{ t.label }}
      </v-tab>
    </v-tabs>
    <v-divider />
    <v-progress-linear v-if="loading" indeterminate height="2" color="primary" />

    <div class="search-body">
      <!-- 空态 -->
      <div v-if="!searched" class="search-empty">
        <v-icon icon="$search" size="44" class="mb-dim" />
        <div class="text-body-large mt-2">输入关键词开始搜索</div>
        <div class="text-body-medium mb-dim mt-1">支持歌曲、歌单、歌手、专辑四种类型</div>
      </div>

      <template v-else>
        <!-- 单曲 -->
        <template v-if="type === 'song'">
          <div class="result-head">
            <span class="text-body-medium">“{{ searched }}”的单曲 · {{ tracks.length }} 首</span>
            <v-spacer />
            <v-btn
              size="small"
              variant="tonal"
              color="primary"
              prepend-icon="$play"
              :disabled="!tracks.length"
              @click="playAllSongs"
            >
              播放全部
            </v-btn>
          </div>
          <TrackRow
            v-for="(t, i) in tracks"
            :key="`${t.id}-${i}`"
            :track="t"
            :playing="t.id === player.current?.id"
            @play="player.actions.playSource({ song_ids: [t.id] })"
            @enqueue="player.actions.enqueueSource({ song_ids: [t.id] })"
            @next="player.actions.enqueueSource({ song_ids: [t.id] }, true)"
          />
          <div v-if="!loading && !tracks.length" class="pa-6 text-center mb-dim">没搜到相关单曲</div>
        </template>

        <!-- 歌单 -->
        <template v-else-if="type === 'playlist'">
          <div class="pl-grid">
            <div v-for="p in playlists" :key="p.id" class="pl-card" @click="openPl(p)">
              <div class="pl-cover-wrap">
                <v-img :src="p.cover_url" cover class="pl-cover" />
                <span class="pl-count"><v-icon icon="$play" size="12" /> {{ fmtCount(p.play_count) }}</span>
              </div>
              <div class="pl-name">{{ p.name }}</div>
              <div class="text-caption mb-dim">{{ p.track_count }} 首</div>
            </div>
          </div>
          <div v-if="!loading && !playlists.length" class="pa-6 text-center mb-dim">没搜到相关歌单</div>
        </template>

        <!-- 歌手：B1 暂无歌手详情端点，仅展示不可点 -->
        <template v-else-if="type === 'artist'">
          <div class="result-head">
            <span class="text-body-medium mb-dim">“{{ searched }}”的歌手 · {{ artists.length }} 位（详情页待后端支持）</span>
          </div>
          <div v-for="a in artists" :key="a.id" class="artist-row">
            <v-img :src="a.pic_url" cover class="artist-avatar" />
            <div class="min-w-0 flex-1">
              <div class="text-body-medium text-truncate">
                {{ a.name }}
                <span v-if="a.alias?.length" class="text-caption mb-dim">{{ a.alias.join(' / ') }}</span>
              </div>
              <div class="text-caption mb-dim">{{ a.music_size }} 首歌 · {{ a.album_size }} 张专辑</div>
            </div>
          </div>
          <div v-if="!loading && !artists.length" class="pa-6 text-center mb-dim">没搜到相关歌手</div>
        </template>

        <!-- 专辑：无详情端点，点卡片直接播整张 -->
        <template v-else>
          <div class="result-head">
            <span class="text-body-medium mb-dim">“{{ searched }}”的专辑 · {{ albums.length }} 张（点封面播整张）</span>
          </div>
          <div class="pl-grid">
            <div v-for="a in albums" :key="a.id" class="pl-card" @click="playAlbum(a)">
              <div class="pl-cover-wrap">
                <v-img :src="a.pic_url" cover class="pl-cover" />
              </div>
              <div class="pl-name">{{ a.name }}</div>
              <div class="text-caption mb-dim text-truncate">{{ a.artist || '未知歌手' }}</div>
            </div>
          </div>
          <div v-if="!loading && !albums.length" class="pa-6 text-center mb-dim">没搜到相关专辑</div>
        </template>
      </template>
    </div>

    <TrackListDialog v-model="detailOpen" :playlist-id="detail?.id" :title="detail?.name" />
  </div>
</template>

<style scoped>
.mb-search {
  padding: 14px 16px 28px;
  max-width: 1500px;
  margin: 0 auto;
}
@media (max-width: 1100px) {
  .mb-search {
    padding: 10px 10px 28px;
  }
}
.search-head {
  margin-bottom: 10px;
}
.search-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.search-field {
  flex: 1;
  min-width: 0;
}
.hot-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
}
.hot-chip {
  cursor: pointer;
}
.search-body {
  padding-top: 10px;
}
.search-empty {
  padding: 56px 0 40px;
  text-align: center;
}
.result-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 4px 10px;
}
.pl-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 14px;
}
.pl-card {
  cursor: pointer;
}
.pl-cover-wrap {
  position: relative;
  border-radius: 10px;
  overflow: hidden;
}
.pl-cover {
  aspect-ratio: 1;
  background: rgb(var(--v-theme-surface-variant));
}
.pl-count {
  position: absolute;
  top: 6px;
  right: 6px;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 2px 7px;
  border-radius: 999px;
  font-size: 11px;
  color: #fff;
  background: rgba(0, 0, 0, 0.45);
}
.pl-name {
  margin-top: 6px;
  font-size: 13px;
  line-height: 1.35;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.artist-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 6px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
}
.artist-avatar {
  flex: none;
  width: 44px;
  height: 44px;
  border-radius: 50%;
  background: rgb(var(--v-theme-surface-variant));
}
</style>
