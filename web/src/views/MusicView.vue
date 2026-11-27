<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import * as api from '../api/client'
import type { NewAlbum, Playlist, RecommendPlaylist, ToplistItem } from '../api/types'
import TrackListDialog from '../components/TrackListDialog.vue'
import { useHashRoute } from '../composables/useHashRoute'
import { player } from '../stores/player'
import { fmtCount } from '../utils/format'

const { go } = useHashRoute()

// ---- 顶部搜索 ----
const searchQ = ref('')
const hot = ref<string[]>([])
const hotIdx = ref(0)
let hotTimer: number | undefined
const placeholder = computed(() =>
  hot.value.length ? `大家都在搜：${hot.value[hotIdx.value]}` : '搜索歌曲、歌单、歌手',
)

function submitSearch() {
  const t = searchQ.value.trim()
  if (!t) return
  go('search', { q: t })
}

// ---- 入口卡片 ----
const busyFm = ref(false)
const dailyBusy = computed(() => player.busy === '播放')
const heartBusy = computed(() => player.busy === '心动模式')

async function playDaily() {
  await player.actions.playSource({ daily: true })
}

// 心动模式语义对齐 HA 集成：直接从红心歌单抽种子，不依赖「当前曲」
async function playHeart() {
  await player.actions.startHotmode('liked')
}

async function playFm() {
  busyFm.value = true
  try {
    const r = await api.getFm()
    const ids = (r.tracks || []).map((t) => t.id).filter(Boolean)
    if (!ids.length) {
      player.error = '私人 FM 没拿到曲目，稍后再试（未登录时不可用）'
      return
    }
    await player.actions.playSource({ song_ids: ids })
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    busyFm.value = false
  }
}

function scrollTo(id: string) {
  document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

// ---- 我的歌单（登录态，/api/user/playlists）----
const myList = ref<Playlist[]>([])
const myLoading = ref(false)
const myExpanded = ref(false)
const shownMy = computed(() => (myExpanded.value ? myList.value : myList.value.slice(0, 10)))

async function loadMy() {
  myLoading.value = true
  try {
    const r = await api.getPlaylists()
    myList.value = r.playlists || []
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    myLoading.value = false
  }
}

// ---- 推荐歌单 ----
const recList = ref<RecommendPlaylist[]>([])
const recSource = ref('')
const recLoading = ref(false)

async function loadRec() {
  recLoading.value = true
  try {
    const r = await api.getRecommendPlaylists()
    recList.value = r.playlists || []
    recSource.value = r.source || ''
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    recLoading.value = false
  }
}

// ---- 排行榜 ----
const toplists = ref<ToplistItem[]>([])
const topLoading = ref(false)
const detailOpen = ref(false)
const detail = ref<{ id: number; name: string } | null>(null)

async function loadTop() {
  topLoading.value = true
  try {
    const r = await api.getToplist()
    toplists.value = (r.toplist || []).slice(0, 12)
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    topLoading.value = false
  }
}

function openTop(t: ToplistItem) {
  detail.value = { id: t.id, name: t.name }
  detailOpen.value = true
}

// ---- 新碟上架 ----
const albums = ref<NewAlbum[]>([])
const albumLoading = ref(false)

async function loadAlbums() {
  albumLoading.value = true
  try {
    const r = await api.getNewAlbums()
    albums.value = r.albums || []
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    albumLoading.value = false
  }
}

function playAlbum(a: NewAlbum) {
  player.actions.playSource({ album_id: a.id })
}

onMounted(() => {
  loadMy()
  loadRec()
  loadTop()
  loadAlbums()
  api.getSearchHot().then((r) => (hot.value = r.searches || [])).catch(() => {})
  hotTimer = window.setInterval(() => {
    if (hot.value.length) hotIdx.value = (hotIdx.value + 1) % hot.value.length
  }, 5000)
})
onUnmounted(() => window.clearInterval(hotTimer))
</script>

<template>
  <div class="mb-home">
    <div v-if="!player.speakers.length" class="px-4 pt-4">
      <v-alert type="warning" variant="tonal" title="还没有可用的音箱">
        配置里 <code>speakers[]</code> 为空，或所有音箱初始化失败。去「设置」加一台，或看
        <code>GET /api/admin/logs</code> 里的失败原因。
      </v-alert>
    </div>

    <!-- 大搜索框 -->
    <div class="home-search px-1">
      <v-text-field
        v-model="searchQ"
        :placeholder="placeholder"
        prepend-inner-icon="$search"
        density="comfortable"
        variant="solo-filled"
        flat
        hide-details
        clearable
        rounded="lg"
        @keyup.enter="submitSearch"
        @click:prepend-inner="submitSearch"
      />
    </div>

    <!-- 入口卡片行 -->
    <div class="entry-row px-1">
      <button class="entry-card grad-red" :disabled="dailyBusy" @click="playDaily">
        <v-icon icon="$daily" size="32" />
        <span class="entry-title">每日推荐</span>
        <span class="entry-sub">{{ dailyBusy ? '正在开播…' : '30 首私人订制' }}</span>
      </button>
      <button
        class="entry-card grad-orange"
        :disabled="heartBusy"
        title="从红心歌单出发，无限续杯"
        @click="playHeart"
      >
        <v-icon icon="$hotmode" size="32" />
        <span class="entry-title">心动模式</span>
        <span class="entry-sub">{{ heartBusy ? '启动中…' : '从红心歌单出发' }}</span>
      </button>
      <button class="entry-card grad-purple" :disabled="busyFm" @click="playFm">
        <v-icon icon="$radio" size="32" />
        <span class="entry-title">私人 FM</span>
        <span class="entry-sub">{{ busyFm ? '拉取曲目中…' : '随机私荐电台' }}</span>
      </button>
      <button class="entry-card grad-blue" @click="scrollTo('sec-toplist')">
        <v-icon icon="$chart" size="32" />
        <span class="entry-title">排行榜</span>
        <span class="entry-sub">官方权威榜单</span>
      </button>
      <button class="entry-card grad-cyan" @click="scrollTo('sec-albums')">
        <v-icon icon="$album" size="32" />
        <span class="entry-title">新碟上架</span>
        <span class="entry-sub">最新发行抢鲜听</span>
      </button>
    </div>

    <!-- 我的歌单：登录态展示，未登录给引导 -->
    <section id="sec-my" class="home-sec">
      <div class="sec-head">
        <h2>我的歌单</h2>
        <span v-if="player.account && myList.length" class="text-caption mb-dim">{{ myList.length }} 个收藏歌单</span>
        <v-spacer />
        <v-btn
          v-if="myList.length > 10"
          size="small"
          variant="text"
          :prepend-icon="myExpanded ? '$up' : '$down'"
          @click="myExpanded = !myExpanded"
        >
          {{ myExpanded ? '收起' : '展开全部' }}
        </v-btn>
        <v-btn v-if="player.account" size="small" variant="text" prepend-icon="$refresh" :loading="myLoading" @click="loadMy">刷新</v-btn>
      </div>
      <v-progress-linear v-if="myLoading" indeterminate height="2" color="primary" class="mb-2" />
      <div v-if="player.account && myList.length" class="rec-grid">
        <div v-for="p in shownMy" :key="p.id" class="rec-card" @click="player.actions.playSource({ playlist_id: p.id })">
          <div class="rec-cover-wrap">
            <v-img :src="p.cover" cover class="rec-cover" />
            <div class="rec-hover">
              <v-btn
                size="x-small"
                icon="$play"
                variant="flat"
                color="primary"
                title="播放整个歌单"
                @click.stop="player.actions.playSource({ playlist_id: p.id })"
              />
              <v-btn
                size="x-small"
                icon="$playlist"
                variant="flat"
                color="surface-variant"
                title="下一首播放"
                @click.stop="player.actions.enqueueSource({ playlist_id: p.id }, true)"
              />
            </div>
          </div>
          <div class="rec-name">{{ p.name }}</div>
          <div class="rec-meta mb-dim">
            <v-icon icon="$play" size="12" /> {{ fmtCount(p.play_count) }} · {{ p.track_count }} 首
          </div>
        </div>
      </div>
      <div v-else-if="player.account && !myLoading" class="pa-4 text-center mb-dim">还没有可展示的收藏歌单</div>
      <div v-else-if="!player.account && !myLoading" class="my-login">
        <v-icon icon="$account" size="26" class="mb-dim" />
        <div>
          <div class="text-body-medium">登录网易云后可看「我的歌单」</div>
          <div class="text-caption mb-dim mt-1">当前未登录，去设置页的「账号与认证」扫码即可</div>
        </div>
        <v-btn size="small" variant="tonal" color="primary" prepend-icon="$login" @click="go('settings', { tab: 'auth' })">
          去登录
        </v-btn>
      </div>
    </section>

    <!-- 推荐歌单 -->
    <section id="sec-rec" class="home-sec">
      <div class="sec-head">
        <h2>推荐歌单</h2>
        <v-chip v-if="recSource === 'personalized'" size="small" variant="tonal" color="accent">个性推荐</v-chip>
        <v-chip v-else-if="recSource === 'recommend'" size="small" variant="tonal" color="primary">热门推荐</v-chip>
        <v-spacer />
        <v-btn size="small" variant="text" prepend-icon="$refresh" :loading="recLoading" @click="loadRec">刷新</v-btn>
      </div>
      <v-progress-linear v-if="recLoading" indeterminate height="2" color="primary" class="mb-2" />
      <div v-if="recList.length" class="rec-grid">
        <div v-for="p in recList" :key="p.id" class="rec-card" @click="player.actions.playSource({ playlist_id: p.id })">
          <div class="rec-cover-wrap">
            <v-img :src="p.pic_url" cover class="rec-cover" />
            <div class="rec-hover">
              <v-btn
                size="x-small"
                icon="$play"
                variant="flat"
                color="primary"
                title="播放整个歌单"
                @click.stop="player.actions.playSource({ playlist_id: p.id })"
              />
              <v-btn
                size="x-small"
                icon="$playlist"
                variant="flat"
                color="surface-variant"
                title="下一首播放"
                @click.stop="player.actions.enqueueSource({ playlist_id: p.id }, true)"
              />
            </div>
          </div>
          <div class="rec-name">{{ p.name }}</div>
          <div class="rec-meta mb-dim">
            <v-icon icon="$play" size="12" /> {{ fmtCount(p.play_count) }}
            <span v-if="p.copywriter" class="rec-copy">{{ p.copywriter }}</span>
          </div>
        </div>
      </div>
      <div v-else-if="!recLoading" class="pa-4 text-center mb-dim">推荐歌单拿不到（未登录也不影响下面板块）</div>
    </section>

    <!-- 排行榜 -->
    <section id="sec-toplist" class="home-sec">
      <div class="sec-head">
        <h2>排行榜</h2>
        <span class="text-caption mb-dim">点开看完整曲目</span>
        <v-spacer />
        <v-btn size="small" variant="text" prepend-icon="$refresh" :loading="topLoading" @click="loadTop">刷新</v-btn>
      </div>
      <v-progress-linear v-if="topLoading" indeterminate height="2" color="primary" class="mb-2" />
      <div v-if="toplists.length" class="top-grid">
        <div v-for="t in toplists" :key="t.id" class="top-card" @click="openTop(t)">
          <v-img :src="t.cover_url" cover class="top-cover" />
          <div class="min-w-0 flex-1">
            <div class="text-body-medium text-truncate">{{ t.name }}</div>
            <div class="text-caption mb-dim">{{ fmtCount(t.play_count) }} 次播放</div>
          </div>
          <v-icon icon="$next" size="18" class="flex-none mb-dim" />
        </div>
      </div>
    </section>

    <!-- 新碟上架 -->
    <section id="sec-albums" class="home-sec">
      <div class="sec-head">
        <h2>新碟上架</h2>
        <span class="text-caption mb-dim">点封面直接播整张</span>
        <v-spacer />
        <v-btn size="small" variant="text" prepend-icon="$refresh" :loading="albumLoading" @click="loadAlbums">刷新</v-btn>
      </div>
      <v-progress-linear v-if="albumLoading" indeterminate height="2" color="primary" class="mb-2" />
      <div v-if="albums.length" class="album-row mb-scroll">
        <div v-for="a in albums" :key="a.id" class="album-card" @click="playAlbum(a)">
          <v-img :src="a.pic_url" cover class="album-cover" />
          <div class="album-name text-truncate">{{ a.name }}</div>
          <div class="text-caption mb-dim text-truncate">{{ a.artist || '未知歌手' }}</div>
        </div>
      </div>
    </section>

    <!-- 歌单 / 榜单详情 -->
    <TrackListDialog v-model="detailOpen" :playlist-id="detail?.id" :title="detail?.name" />
  </div>
</template>

<style scoped>
.mb-home {
  padding: 14px 16px 28px;
  max-width: 1500px;
  margin: 0 auto;
}
@media (max-width: 1100px) {
  .mb-home {
    padding: 10px 10px 28px;
  }
}

/* ---- 搜索框 ---- */
.home-search {
  margin-bottom: 14px;
}

/* ---- 入口卡片 ---- */
.entry-row {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 12px;
  margin-bottom: 22px;
}
@media (max-width: 900px) {
  .entry-row {
    grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
  }
}
.entry-card {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 3px;
  padding: 14px 14px 12px;
  border: 0;
  border-radius: 14px;
  color: #fff;
  cursor: pointer;
  transition: transform 0.15s ease, box-shadow 0.15s ease;
}
.entry-card:hover:not(:disabled) {
  transform: translateY(-3px);
  box-shadow: 0 10px 22px rgba(0, 0, 0, 0.35);
}
.entry-card:disabled {
  opacity: 0.55;
  cursor: default;
}
.grad-red {
  background: linear-gradient(135deg, #ff5f6d, #ff9670);
}
.grad-orange {
  background: linear-gradient(135deg, #ff9d44, #ffc55c);
}
.grad-purple {
  background: linear-gradient(135deg, #8e6cf0, #b58cf2);
}
.grad-blue {
  background: linear-gradient(135deg, #4e9bff, #6fc3ff);
}
.grad-cyan {
  background: linear-gradient(135deg, #35c8b2, #6fdccf);
}
.entry-card :deep(.v-icon) {
  margin-bottom: 4px;
  filter: drop-shadow(0 2px 4px rgba(0, 0, 0, 0.2));
}
.entry-title {
  font-size: 15px;
  font-weight: 600;
  line-height: 1.2;
}
.entry-sub {
  font-size: 12px;
  opacity: 0.85;
}

/* ---- 板块通用 ---- */
.home-sec {
  margin-bottom: 26px;
}
.sec-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.sec-head h2 {
  font-size: 18px;
  font-weight: 700;
  margin: 0;
}

/* ---- 推荐歌单 ---- */
.rec-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 14px;
}
.rec-card {
  cursor: pointer;
}
.rec-cover-wrap {
  position: relative;
  border-radius: 10px;
  overflow: hidden;
}
.rec-cover {
  aspect-ratio: 1;
  background: rgb(var(--v-theme-surface-variant));
}
.rec-meta {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 12px;
  margin-top: 2px;
  max-width: 100%;
}
.rec-copy {
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* ---- 我的歌单：未登录引导 ---- */
.my-login {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 16px;
  border-radius: 12px;
  background: rgb(var(--v-theme-surface));
}
.my-login > div {
  flex: 1;
  min-width: 0;
}
.rec-hover {
  position: absolute;
  right: 6px;
  bottom: 6px;
  display: flex;
  gap: 6px;
  opacity: 0;
  transition: opacity 0.15s ease;
}
.rec-card:hover .rec-hover {
  opacity: 1;
}
.rec-name {
  margin-top: 6px;
  font-size: 13px;
  line-height: 1.35;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

/* ---- 排行榜 ---- */
.top-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px;
}
.top-card {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 12px;
  background: rgb(var(--v-theme-surface));
  cursor: pointer;
}
.top-card:hover {
  background: rgb(var(--v-theme-surface-bright));
}
.top-cover {
  flex: none;
  width: 48px;
  height: 48px;
  border-radius: 8px;
  background: rgb(var(--v-theme-surface-variant));
}

/* ---- 新碟上架 ---- */
.album-row {
  display: flex;
  gap: 14px;
  overflow-x: auto;
  padding-bottom: 6px;
}
.album-card {
  flex: none;
  width: 132px;
  cursor: pointer;
}
.album-cover {
  width: 132px;
  height: 132px;
  border-radius: 10px;
  background: rgb(var(--v-theme-surface-variant));
}
.album-name {
  margin-top: 6px;
  font-size: 13px;
}
</style>
