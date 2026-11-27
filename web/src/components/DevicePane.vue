<script setup lang="ts">
import { onMounted, ref } from 'vue'
import * as api from '../api/client'
import { player } from '../stores/player'
import { fmtMs, fmtTime } from '../utils/format'

const device = ref<Record<string, any> | null>(null)
const loading = ref(false)
const err = ref('')

async function load() {
  loading.value = true
  err.value = ''
  try {
    device.value = (await api.getDevice(player.speakerId)).device
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

onMounted(load)
player.loadState()

// row('名字', 值)：设备字段缺项时统一显示成 —
function row(value: unknown) {
  if (value === undefined || value === null || value === '') return '—'
  return String(value)
}
</script>

<template>
  <div class="mb-scroll mb-device pa-3">
    <div class="d-flex align-center ga-2 mb-2 flex-wrap">
      <v-btn size="small" variant="text" prepend-icon="$refresh" :loading="loading" @click="load">重新回读设备</v-btn>
      <v-chip size="small" variant="tonal" :color="player.online ? 'primary' : 'warning'">
        {{ player.online ? 'SSE 已连接' : 'SSE 断开' }}
      </v-chip>
      <span class="text-caption mb-dim">{{ row(player.speakerId) }}</span>
    </div>

    <v-alert v-if="err" type="error" density="compact" variant="tonal" class="mb-2">{{ err }}</v-alert>

    <v-table density="compact" class="mb-4">
      <thead>
        <tr>
          <th colspan="2">
            <template v-if="device?.stale">
              设备回读 · 最后一次已知（{{ device?.age_sec }}s 前，设备当前不可达）
            </template>
            <template v-else>设备回读（探针看到的真相）</template>
          </th>
        </tr>
      </thead>
      <tbody>
        <tr>
          <td class="mb-dim">状态 / 播放</td>
          <td class="mb-mono">{{ row(device?.status) }} / {{ row(device?.playing) }}</td>
        </tr>
        <tr>
          <td class="mb-dim">audio_id / track_id</td>
          <td class="mb-mono">{{ row(device?.audio_id) }} / {{ row(device?.track_id) }}</td>
        </tr>
        <tr>
          <td class="mb-dim">位置 / 时长</td>
          <td class="mb-mono">
            {{ device?.position_ms !== undefined ? fmtMs(device.position_ms) : '—' }} /
            {{ device?.duration_ms ? fmtMs(device.duration_ms) : '—' }}
          </td>
        </tr>
        <tr>
          <td class="mb-dim">设备音量 / 循环 / 通道</td>
          <td class="mb-mono">
            {{ row(device?.volume) }} / {{ row(device?.loop_type) }} /
            {{ row(device?.media_type) }}
          </td>
        </tr>
        <tr>
          <td class="mb-dim">回读间隔</td>
          <td class="mb-mono">
            <span v-if="player.state?.poll?.mode === 'push'">已关闭轮询（设备主动推送）</span>
            <span v-else>{{ row(device?.poll_sec) }} 秒</span>
          </td>
        </tr>
        <tr>
          <td class="mb-dim">状态感知 / 探针模式</td>
          <td class="mb-mono">
            <template v-if="player.state?.poll?.mode === 'push'">
              <v-chip size="x-small" variant="tonal" :color="player.state?.poll?.healthy ? 'accent' : 'error'" class="mr-1">
                {{ player.state?.poll?.healthy ? 'push · 健康' : 'push · 离线' }}
              </v-chip>
              <span v-if="player.state?.poll?.last_age_sec !== undefined && (player.state.poll.last_age_sec as number) >= 0" class="text-caption mb-dim">
                ({{ player.state.poll.last_age_sec }}s 前上报 · 静默 {{ player.state.poll.offline_after }}s 判离线)
              </span>
              <div class="text-caption mb-dim">
                daemon 节奏：采样 {{ player.state.poll.sample_sec }}s · 心跳
                {{ player.state.poll.hb_play_sec }}s/{{ player.state.poll.hb_idle_sec }}s
                · seek 容差 {{ player.state.poll.seek_tol_ms }}ms
              </div>
            </template>
            <template v-else>
              {{ row(player.state?.poll?.mode) }} / {{ row(player.state?.poll?.reason) }}
            </template>
          </td>
        </tr>
        <tr>
          <td class="mb-dim">最近探针错误</td>
          <td class="mb-mono">{{ row(player.state?.poll?.last_error) }}</td>
        </tr>
      </tbody>
    </v-table>

    <v-table density="compact" class="mb-4">
      <thead>
        <tr>
          <th colspan="2">服务端权威状态</th>
        </tr>
      </thead>
      <tbody>
        <tr>
          <td class="mb-dim">队列</td>
          <td class="mb-mono">
            第 {{ (player.state?.queue.index ?? 0) + 1 }} / {{ player.state?.queue.total ?? 0 }} 首 ·
            {{ row(player.queue?.source) }}
          </td>
        </tr>
        <tr>
          <td class="mb-dim">位置（服务端估算）</td>
          <td class="mb-mono">{{ fmtMs(player.positionMs) }}</td>
        </tr>
        <tr>
          <td class="mb-dim">循环 / 随机</td>
          <td class="mb-mono">{{ player.repeat }} / {{ player.shuffle ? '开' : '关' }}</td>
        </tr>
        <tr>
          <td class="mb-dim">心动续杯</td>
          <td class="mb-mono">
            <template v-if="player.hotmode">
              {{ player.hotmode.on ? '进行中' : `已停${player.hotmode.stop_reason ? `（${player.hotmode.stop_reason}）` : ''}` }}
              · 已续 {{ player.hotmode.extended }} 次 · 接口失败 {{ player.hotmode.fails }} · 补货阈值 {{ player.hotmode.below }}
            </template>
            <template v-else>未开启</template>
          </td>
        </tr>
        <tr>
          <td class="mb-dim">队列快照</td>
          <td class="mb-mono">
            <template v-if="player.state?.persist">
              {{ player.state.persist.path }}<br />
              {{ player.state.persist.had_queue ? `上次落盘 ${fmtTime(player.state.persist.saved_at || 0)}` : '还没落过盘' }}
            </template>
            <template v-else>持久化已关闭（queue_persist=false）</template>
          </td>
        </tr>
      </tbody>
    </v-table>

    <div v-if="player.state?.alarm_volume !== undefined" class="d-flex align-center ga-3">
      <span class="text-caption mb-dim" style="width: 88px">闹钟音量</span>
      <v-slider
        :model-value="player.state.alarm_volume"
        :max="100"
        hide-details
        density="compact"
        @update:model-value="(v: number) => player.actions.setAlarmVolume(Math.round(v))"
      />
      <span class="text-caption mb-mono">{{ player.state.alarm_volume }}</span>
    </div>
    <div v-else class="text-caption mb-dim">这台音箱没有可读的闹钟音量（需要小米云端凭据）</div>
  </div>
</template>

<style scoped>
.mb-device {
  max-height: calc(100dvh - 300px);
  min-height: 240px;
}
.mb-device :deep(td) {
  font-size: 13px;
  /* 表格窄屏时靠横向滚动，别让 audio_id 这种长串被裁掉 */
  white-space: nowrap;
}
.mb-device :deep(th) {
  white-space: nowrap;
}
.mb-device :deep(.v-table__wrapper) {
  overflow-x: auto;
}
</style>
