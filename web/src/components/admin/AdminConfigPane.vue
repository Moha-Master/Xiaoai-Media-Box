<script setup lang="ts">
import { ref, watch } from 'vue'
import * as api from '../../api/client'
import type { AdminConfigReply } from '../../api/client'
import type { AppConfig, SpeakerConfig, XiaomiDevice } from '../../api/types'
import { player } from '../../stores/player'

const props = defineProps<{
  cfg: AdminConfigReply | null
  loading: boolean
}>()

const emit = defineEmits<{
  saved: [reply: api.AdminPutReply]
}>()

// 表单本地可变状态
const form = ref<AppConfig>({
  listen: '0.0.0.0:8088',
  public_base: 'http://127.0.0.1:8088',
  cookie_file: 'cookie.json',
  quality: 'exhigh',
  window: 20,
  loop: 'list',
  stream_mode: 'redirect',
  hotmode_extend_below: 20,
  hotmode_extend_max_fail: 3,
  queue_persist: true,
  speakers: [],
})

// 音箱编辑弹窗
const speakerDialog = ref(false)
const editingIndex = ref(-1)
const speakerForm = ref<SpeakerConfig>({
  id: '',
  name: '',
  control: 'cloud',
  status_mode: 'cloud',
  device_id: '',
  ssh_host: '',
  ssh_key: '',
  ubus_timeout: 5,
  media: 'app_ios',
  use_window: true,
  status_poll: 0,
  poll_burst: 0,
  poll_rounds: 0,
})

const showAdvancedSpeaker = ref(false)
const saving = ref(false)
const xiaomiDevices = ref<XiaomiDevice[]>([])
const loadingDevices = ref(false)

const qualityOptions = [
  { title: '极高 (320kbps - exhigh)', value: 'exhigh' },
  { title: '较高 (192kbps - higher)', value: 'higher' },
  { title: '标准 (128kbps - standard)', value: 'standard' },
  { title: '无损 (FLAC - lossless)', value: 'lossless' },
  { title: 'Hi-Res (hires)', value: 'hires' },
  { title: '高清环绕声 (sky)', value: 'sky' },
  { title: '沉浸环绕声 (jyeffect)', value: 'jyeffect' },
  { title: '超清母带 (jymaster)', value: 'jymaster' },
]

const loopOptions = [
  { title: '列表循环 (list)', value: 'list' },
  { title: '单曲循环 (single)', value: 'single' },
  { title: '随机播放 (shuffle)', value: 'shuffle' },
  { title: '不循环 (none)', value: 'none' },
]

const streamModeOptions = [
  { title: '302 重定向到 CDN (redirect - 推荐，NAS 零带宽消耗)', value: 'redirect' },
  { title: '代理转发音频流 (proxy - 适合音箱与外网隔离环境)', value: 'proxy' },
]

const controlOptions = [
  { title: '小米云端控制 (cloud - 官方通道，免 SSH/免破解固件)', value: 'cloud' },
  { title: '局域网 SSH 控制 (ssh - ubus 指令，需音箱 root)', value: 'ssh' },
  { title: '虚拟音箱 (noop - 仅用于调试，不出声)', value: 'noop' },
]

const statusModeOptions = [
  { title: '设备主动推送 (push - 推荐已 root 音箱，毫秒级感知且零 SSH 轮询)', value: 'push' },
  { title: '小米云端轮询 (cloud - 官方通道，每 60 秒稳态回读)', value: 'cloud' },
  { title: 'SSH 定时轮询 (ssh - 传统 SSH 轮询通道)', value: 'ssh' },
  { title: '关闭状态感知 (none - 纯本地推算计时)', value: 'none' },
]

watch(
  () => props.cfg,
  (c) => {
    if (!c) return
    const raw = c.config
    form.value = {
      listen: raw.listen || '0.0.0.0:8088',
      public_base: raw.public_base || 'http://127.0.0.1:8088',
      cookie_file: raw.cookie_file || 'cookie.json',
      cookie_key_file: raw.cookie_key_file,
      quality: raw.quality || 'exhigh',
      window: raw.window || 20,
      loop: raw.loop || 'list',
      stream_mode: raw.stream_mode || 'redirect',
      hotmode_extend_below: raw.hotmode_extend_below ?? 20,
      hotmode_extend_max_fail: raw.hotmode_extend_max_fail ?? 3,
      queue_persist: raw.queue_persist ?? true,
      speakers: (raw.speakers || []).map((s: SpeakerConfig) => ({ ...s })),
    }
  },
  { immediate: true },
)

async function loadCloudDevices() {
  loadingDevices.value = true
  try {
    const res = await api.getXiaomiDevices()
    xiaomiDevices.value = res.devices || []
  } catch {
    xiaomiDevices.value = []
  } finally {
    loadingDevices.value = false
  }
}

function openAddSpeaker() {
  editingIndex.value = -1
  const nextIdx = form.value.speakers.length + 1
  speakerForm.value = {
    id: `spk${nextIdx}`,
    name: `音箱 ${nextIdx}`,
    control: 'cloud',
    status_mode: 'cloud',
    device_id: '',
    ssh_host: 'root@192.168.1.20',
    ssh_key: '',
    ubus_timeout: 5,
    media: 'app_ios',
    use_window: true,
    status_poll: 0,
    poll_burst: 0,
    poll_rounds: 0,
  }
  showAdvancedSpeaker.value = false
  speakerDialog.value = true
  loadCloudDevices()
}

function openEditSpeaker(idx: number) {
  editingIndex.value = idx
  const target = form.value.speakers[idx]
  speakerForm.value = {
    ...target,
    status_mode: target.status_mode || (target.control === 'ssh' ? 'ssh' : 'cloud'),
    use_window: target.use_window ?? true,
    status_poll: target.status_poll ?? 0,
    poll_burst: target.poll_burst ?? 0,
    poll_rounds: target.poll_rounds ?? 0,
  }
  showAdvancedSpeaker.value = false
  speakerDialog.value = true
  loadCloudDevices()
}

function saveSpeaker() {
  const sp = { ...speakerForm.value }
  sp.id = sp.id.trim()
  sp.name = sp.name.trim() || sp.id
  if (sp.control === 'ssh') {
    sp.media = sp.media || 'common'
  } else if (sp.control === 'cloud') {
    sp.media = sp.media || 'app_ios'
  }

  if (editingIndex.value >= 0) {
    form.value.speakers[editingIndex.value] = sp
  } else {
    form.value.speakers.push(sp)
  }
  speakerDialog.value = false
}

function removeSpeaker(idx: number) {
  if (form.value.speakers.length <= 1) {
    player.error = '至少需要保留一台音箱'
    return
  }
  form.value.speakers.splice(idx, 1)
}

function moveSpeaker(idx: number, delta: number) {
  const target = idx + delta
  if (target < 0 || target >= form.value.speakers.length) return
  const item = form.value.speakers.splice(idx, 1)[0]
  form.value.speakers.splice(target, 0, item)
}

async function submitConfig() {
  // 基础校验
  if (!form.value.speakers.length) {
    player.error = '音箱列表不能为空'
    return
  }
  const seenIds = new Set<string>()
  for (const sp of form.value.speakers) {
    if (!sp.id) {
      player.error = '音箱 ID 不能为空'
      return
    }
    if (seenIds.has(sp.id)) {
      player.error = `音箱 ID 重复: ${sp.id}`
      return
    }
    seenIds.add(sp.id)
    if (sp.control === 'ssh' && !sp.ssh_host?.trim()) {
      player.error = `音箱 ${sp.name} 选择了 SSH 控制，但未填写 SSH 目标主机`
      return
    }
  }

  saving.value = true
  try {
    const res = await api.adminPutConfig(form.value as unknown as Record<string, unknown>)
    emit('saved', res)
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="d-flex flex-column ga-4">
    <!-- 全局常规设置卡片 -->
    <v-card variant="flat" color="surface" rounded="lg">
      <v-card-title class="d-flex align-center ga-2">
        <v-icon icon="$settings" size="20" color="primary" /> 全局服务设置
      </v-card-title>
      <v-card-text>
        <v-row dense>
          <v-col cols="12" md="6">
            <v-text-field
              v-model="form.listen"
              label="监听地址 (listen)"
              density="compact"
              hint="例如 0.0.0.0:8088 或 127.0.0.1:8088"
              persistent-hint
            />
          </v-col>
          <v-col cols="12" md="6">
            <v-text-field
              v-model="form.public_base"
              label="设备访问基地址 (public_base)"
              density="compact"
              hint="音箱拉取歌曲流访问的 HTTP 地址，如 http://192.168.1.10:8088"
              persistent-hint
            />
          </v-col>

          <v-col cols="12" md="6">
            <v-select
              v-model="form.quality"
              label="音质级别 (quality)"
              :items="qualityOptions"
              item-title="title"
              item-value="value"
              density="compact"
              hint="网易云解析时的目标音质"
              persistent-hint
            />
          </v-col>
          <v-col cols="12" md="6">
            <v-select
              v-model="form.loop"
              label="默认循环模式 (loop)"
              :items="loopOptions"
              item-title="title"
              item-value="value"
              density="compact"
              hint="初始播放列表模式"
              persistent-hint
            />
          </v-col>

          <v-col cols="12" md="6">
            <v-select
              v-model="form.stream_mode"
              label="音频取流通道 (stream_mode)"
              :items="streamModeOptions"
              item-title="title"
              item-value="value"
              density="compact"
            />
          </v-col>
          <v-col cols="12" md="6">
            <v-text-field
              v-model.number="form.window"
              type="number"
              label="设备播放窗口大小 (window)"
              density="compact"
              min="1"
              max="200"
              hint="一次下发给设备的曲目数，默认 20 首（与小爱硬件容量一致）"
              persistent-hint
            />
          </v-col>

          <v-col cols="12" md="6">
            <v-text-field
              v-model.number="form.hotmode_extend_below"
              type="number"
              label="心动续杯触发阈值 (hotmode_extend_below)"
              density="compact"
              hint="当前曲之后剩余歌曲不足该数值时自动追加一批推荐，0=跟随 window"
              persistent-hint
            />
          </v-col>
          <v-col cols="12" md="6">
            <v-text-field
              v-model.number="form.hotmode_extend_max_fail"
              type="number"
              label="心动续杯最大重试 (hotmode_extend_max_fail)"
              density="compact"
              hint="推荐 API 连续失败达此前缀次后退出续杯，默认 3"
              persistent-hint
            />
          </v-col>

          <v-col cols="12" md="6">
            <v-switch
              v-model="form.queue_persist"
              label="播放队列快照持久化 (queue_persist)"
              color="primary"
              density="compact"
              hint="重启后自动恢复列表与进度（恢复时不自动出声）"
              persistent-hint
            />
          </v-col>
          <v-col cols="12" md="6">
            <v-text-field
              v-model="form.cookie_file"
              label="网易云 Cookie 存储路径 (cookie_file)"
              density="compact"
              hint="加密存储的认证文件，默认 cookie.json"
              persistent-hint
            />
          </v-col>
        </v-row>
      </v-card-text>
    </v-card>

    <!-- 音箱列表管理卡片 -->
    <v-card variant="flat" color="surface" rounded="lg">
      <v-card-title class="d-flex align-center ga-2">
        <v-icon icon="$device" size="20" color="primary" /> 音箱配置列表 (speakers)
        <v-spacer />
        <v-btn color="primary" variant="tonal" size="small" prepend-icon="$plus" @click="openAddSpeaker">
          添加音箱
        </v-btn>
      </v-card-title>
      <v-card-text>
        <div class="d-flex flex-column ga-2">
          <v-card
            v-for="(sp, idx) in form.speakers"
            :key="sp.id || idx"
            variant="outlined"
            class="pa-3 mb-speaker-card"
          >
            <div class="d-flex align-center justify-space-between flex-wrap ga-2">
              <div class="d-flex align-center ga-2 min-w-0">
                <v-avatar size="28" :color="sp.control === 'ssh' ? 'accent' : sp.control === 'cloud' ? 'primary' : 'surface-variant'">
                  <v-icon size="16" :icon="sp.control === 'ssh' ? '$device' : sp.control === 'cloud' ? '$phone' : '$musicNote'" />
                </v-avatar>
                <div>
                  <div class="text-body-medium font-weight-medium">
                    {{ sp.name }}
                    <span class="text-caption mb-dim font-weight-regular ml-1">({{ sp.id }})</span>
                  </div>
                  <div class="text-caption mb-dim d-flex align-center flex-wrap ga-1 mt-0-5">
                    <span v-if="sp.control === 'cloud'">
                      云端通道 · {{ sp.device_id || '（自动匹配在线音箱）' }} · 通道: {{ sp.media || 'app_ios' }}
                    </span>
                    <span v-else-if="sp.control === 'ssh'">
                      SSH 局域网 · 目标: {{ sp.ssh_host || '—' }} · 通道: {{ sp.media || 'common' }}
                    </span>
                    <span v-else>
                      虚拟模式 (noop)
                    </span>
                    <v-chip
                      size="x-small"
                      variant="tonal"
                      class="ml-1"
                      :color="sp.status_mode === 'push' ? 'accent' : sp.status_mode === 'none' ? 'surface-variant' : 'primary'"
                    >
                      {{ sp.status_mode === 'push' ? '主动推送' : sp.status_mode === 'none' ? '关闭回读' : sp.status_mode === 'ssh' ? 'SSH轮询' : '云端轮询' }}
                    </v-chip>
                  </div>
                </div>
              </div>

              <div class="d-flex align-center ga-1">
                <v-btn icon="$up" size="x-small" variant="text" :disabled="idx === 0" @click="moveSpeaker(idx, -1)" />
                <v-btn
                  icon="$down"
                  size="x-small"
                  variant="text"
                  :disabled="idx === form.speakers.length - 1"
                  @click="moveSpeaker(idx, 1)"
                />
                <v-btn icon="$edit" size="x-small" variant="text" color="primary" @click="openEditSpeaker(idx)" />
                <v-btn
                  icon="$remove"
                  size="x-small"
                  variant="text"
                  color="error"
                  :disabled="form.speakers.length <= 1"
                  @click="removeSpeaker(idx)"
                />
              </div>
            </div>
          </v-card>
        </div>
      </v-card-text>
    </v-card>

    <!-- 保存按钮条 -->
    <div class="d-flex align-center justify-end ga-3 py-2">
      <span class="text-caption mb-dim">修改配置后点击保存，需要重启服务生效</span>
      <v-btn color="primary" variant="flat" size="large" prepend-icon="$ok" :loading="saving" @click="submitConfig">
        保存配置
      </v-btn>
    </div>

    <!-- 编辑音箱对话框 -->
    <v-dialog v-model="speakerDialog" max-width="600" persistent>
      <v-card :title="editingIndex >= 0 ? `编辑音箱: ${speakerForm.name}` : '添加新音箱'" variant="flat" color="surface">
        <v-card-text>
          <v-row dense>
            <v-col cols="12" md="6">
              <v-text-field
                v-model="speakerForm.id"
                label="音箱 ID (唯一标识)"
                density="compact"
                hint="例如 living、bedroom，不可包含空格或特殊斜杠"
                persistent-hint
              />
            </v-col>
            <v-col cols="12" md="6">
              <v-text-field
                v-model="speakerForm.name"
                label="显示名称"
                density="compact"
                hint="例如 客厅、书房小爱"
                persistent-hint
              />
            </v-col>

            <v-col cols="12">
              <v-select
                v-model="speakerForm.control"
                label="控制方式 (control)"
                :items="controlOptions"
                item-title="title"
                item-value="value"
                density="compact"
              />
            </v-col>

            <v-col cols="12">
              <v-select
                v-model="speakerForm.status_mode"
                label="状态感知方式 (status_mode)"
                :items="statusModeOptions"
                item-title="title"
                item-value="value"
                density="compact"
                :hint="speakerForm.status_mode === 'push'
                  ? '推荐：音箱端运行 scripts/state_reporter.sh 推送状态，免除 SSH 轮询开销，机身按键 1~2s 跟手'
                  : speakerForm.status_mode === 'cloud'
                  ? '云端每 60 秒轮询一次状态快照'
                  : speakerForm.status_mode === 'ssh'
                  ? '服务端定时通过 SSH 发起 ubus 轮询'
                  : '彻底关闭状态感知，仅依赖服务端本地计时'"
                persistent-hint
              />
            </v-col>

            <!-- 云端专属配置 -->
            <template v-if="speakerForm.control === 'cloud'">
              <v-col cols="12">
                <v-combobox
                  v-model="speakerForm.device_id"
                  label="小米云端 Device ID (UUID)"
                  :items="xiaomiDevices.map((d) => ({ title: `${d.name} (${d.device_id}) [${d.presence}]`, value: d.device_id }))"
                  item-title="title"
                  item-value="value"
                  :return-object="false"
                  density="compact"
                  hint="可从下拉列表选择名下音箱，或直接粘贴 UUID；留空在只有 1 台在线时自动选"
                  persistent-hint
                  :loading="loadingDevices"
                >
                  <template #append>
                    <v-btn size="x-small" variant="text" icon="$refresh" :loading="loadingDevices" @click="loadCloudDevices" />
                  </template>
                </v-combobox>
              </v-col>
              <v-col cols="12" md="6">
                <v-text-field
                  v-model="speakerForm.media"
                  label="音频播放通道 (media)"
                  density="compact"
                  hint="云端默认 app_ios"
                  persistent-hint
                />
              </v-col>
            </template>

            <!-- SSH 专属配置 -->
            <template v-if="speakerForm.control === 'ssh'">
              <v-col cols="12" md="8">
                <v-text-field
                  v-model="speakerForm.ssh_host"
                  label="SSH 连接目标 (ssh_host)"
                  density="compact"
                  hint="例如 root@192.168.1.20"
                  persistent-hint
                />
              </v-col>
              <v-col cols="12" md="4">
                <v-text-field
                  v-model.number="speakerForm.ubus_timeout"
                  type="number"
                  label="ubus 超时秒数"
                  density="compact"
                  hint="默认 5 秒"
                  persistent-hint
                />
              </v-col>
              <v-col cols="12" md="8">
                <v-text-field
                  v-model="speakerForm.ssh_key"
                  label="SSH 私钥路径 (可选)"
                  density="compact"
                  hint="留空使用 ~/.ssh/id_rsa 或当前用户默认密钥"
                  persistent-hint
                />
              </v-col>
              <v-col cols="12" md="4">
                <v-text-field
                  v-model="speakerForm.media"
                  label="音频通道 (media)"
                  density="compact"
                  hint="SSH 默认 common"
                  persistent-hint
                />
              </v-col>
            </template>

            <!-- 高级选项折叠 -->
            <v-col cols="12">
              <v-btn
                variant="text"
                size="small"
                :prepend-icon="showAdvancedSpeaker ? '$up' : '$down'"
                class="px-0 mt-2"
                @click="showAdvancedSpeaker = !showAdvancedSpeaker"
              >
                {{ showAdvancedSpeaker ? '收起高级回读设置' : '展开高级回读设置' }}
              </v-btn>
            </v-col>

            <template v-if="showAdvancedSpeaker">
              <v-col cols="12" md="6">
                <v-switch
                  v-model="speakerForm.use_window"
                  label="整窗下发 (use_window)"
                  color="primary"
                  density="compact"
                  hint="开启后一次下发整组播放列表（推荐开启）"
                  persistent-hint
                />
              </v-col>
              <v-col cols="12" md="6">
                <v-text-field
                  v-model.number="speakerForm.status_poll"
                  type="number"
                  label="稳态回读间隔秒数 (status_poll)"
                  density="compact"
                  :disabled="speakerForm.status_mode === 'push' || speakerForm.status_mode === 'none'"
                  :hint="speakerForm.status_mode === 'push'
                    ? '主动推送模式下无需配置回读间隔（由音箱心跳保活）'
                    : speakerForm.status_mode === 'none'
                    ? '已关闭回读'
                    : '0=跟随通道默认 (60s)，-1=彻底关闭探测'"
                  persistent-hint
                />
              </v-col>
              <v-col v-if="speakerForm.status_mode !== 'push' && speakerForm.status_mode !== 'none'" cols="12" md="6">
                <v-text-field
                  v-model.number="speakerForm.poll_burst"
                  type="number"
                  label="操作后加密回读间隔秒数 (poll_burst)"
                  density="compact"
                  hint="0=默认 5 秒；仅轮询模式生效"
                  persistent-hint
                />
              </v-col>
              <v-col v-if="speakerForm.status_mode !== 'push' && speakerForm.status_mode !== 'none'" cols="12" md="6">
                <v-text-field
                  v-model.number="speakerForm.poll_rounds"
                  type="number"
                  label="加密回读轮数 (poll_rounds)"
                  density="compact"
                  hint="0=默认 3 轮；仅轮询模式生效"
                  persistent-hint
                />
              </v-col>

              <!-- 推送模式：这四项是「服务端判定用的期望值」，必须与音箱上
                   /data/etc/state_reporter.conf 一致，否则会把正常心跳误判成离线 -->
              <template v-if="speakerForm.status_mode === 'push'">
                <v-col cols="12">
                  <div class="text-caption mb-dim">
                    设备上报节奏（需与音箱 <code>/data/etc/state_reporter.conf</code> 一致；静默阈值由心跳推出 =
                    max(3×播放心跳, 2×空闲心跳)）
                  </div>
                </v-col>
                <v-col cols="12" md="6">
                  <v-text-field
                    v-model.number="speakerForm.report_interval"
                    type="number"
                    label="daemon 采样间隔秒数 (report_interval)"
                    density="compact"
                    hint="0=默认 5 秒"
                    persistent-hint
                  />
                </v-col>
                <v-col cols="12" md="6">
                  <v-text-field
                    v-model.number="speakerForm.report_hb_play"
                    type="number"
                    label="播放中心跳秒数 (report_hb_play)"
                    density="compact"
                    hint="0=默认 15 秒"
                    persistent-hint
                  />
                </v-col>
                <v-col cols="12" md="6">
                  <v-text-field
                    v-model.number="speakerForm.report_hb_idle"
                    type="number"
                    label="暂停/空闲心跳秒数 (report_hb_idle)"
                    density="compact"
                    hint="0=默认 30 秒"
                    persistent-hint
                  />
                </v-col>
                <v-col cols="12" md="6">
                  <v-text-field
                    v-model.number="speakerForm.report_seek_tol_ms"
                    type="number"
                    label="位置异常容差毫秒 (report_seek_tol_ms)"
                    density="compact"
                    hint="0=默认 8000；seek 复核的允许偏差"
                    persistent-hint
                  />
                </v-col>
              </template>
            </template>
          </v-row>
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="speakerDialog = false">取消</v-btn>
          <v-btn color="primary" variant="flat" @click="saveSpeaker">确定</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </div>
</template>

<style scoped>
.mb-speaker-card {
  border-color: rgba(255, 255, 255, 0.08);
  background: rgba(255, 255, 255, 0.02);
}
</style>
