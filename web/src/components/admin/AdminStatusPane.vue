<script setup lang="ts">
import { ref } from 'vue'
import * as api from '../../api/client'
import type { AdminBackup, AdminConfigReply } from '../../api/client'
import { player } from '../../stores/player'
import { fmtBytes, fmtTime, fmtUptime } from '../../utils/format'
import { useHashRoute } from '../../composables/useHashRoute'

const { go } = useHashRoute()

const props = defineProps<{
  cfg: AdminConfigReply | null
  loading: boolean
}>()

const emit = defineEmits<{
  reload: []
  restart: []
}>()

const logs = ref<string[]>([])
const logsTotal = ref(0)
const loadingLogs = ref(false)
const busy = ref('')
const note = ref('')
const showRaw = ref(false)
const confirmRestore = ref<AdminBackup | null>(null)

async function loadLogs() {
  loadingLogs.value = true
  try {
    const r = await api.adminLogs(300)
    logs.value = r.lines
    logsTotal.value = r.total
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    loadingLogs.value = false
  }
}

async function doRestore(b: AdminBackup) {
  confirmRestore.value = null
  busy.value = b.name
  try {
    const r = await api.adminRestore(b.name)
    note.value = `已回滚到 ${b.name}${r.backup ? `（回滚前的版本已备份在 ${r.backup.split('/').pop()}）` : ''}，需要重启才生效`
    emit('reload')
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    busy.value = ''
  }
}

loadLogs()
</script>

<template>
  <div class="d-flex flex-column ga-4">
    <!-- 待重启提示 -->
    <v-alert
      v-if="cfg?.requires_restart"
      type="warning"
      variant="tonal"
      class="elevation-1"
      :title="`有 ${cfg.pending_changes.length} 项改动尚未生效：${cfg.pending_changes.join(', ')}`"
    >
      <div class="d-flex align-center justify-space-between flex-wrap ga-2 mt-1">
        <span>配置已保存到磁盘，需要重启服务使音箱与监听生效。</span>
        <v-btn color="primary" variant="flat" size="small" prepend-icon="$refresh" @click="emit('restart')">
          立即重启服务
        </v-btn>
      </div>
    </v-alert>

    <div v-if="note" class="text-caption px-1" style="color: rgb(var(--v-theme-accent))">
      {{ note }}
    </div>

    <div class="mb-admin-grid">
      <!-- 左栏：运行状态 + 备份 -->
      <div class="d-flex flex-column ga-4">
        <v-card variant="flat" color="surface" rounded="lg">
          <v-card-title class="d-flex align-center ga-2">
            <v-icon icon="$info" size="20" color="primary" /> 运行状态
            <v-spacer />
            <v-btn size="small" variant="text" prepend-icon="$refresh" :loading="loading" @click="emit('reload')">
              重载
            </v-btn>
          </v-card-title>
          <v-card-text v-if="cfg">
            <v-table density="compact">
              <tbody>
                <tr>
                  <td class="mb-dim">配置文件</td>
                  <td class="mb-mono">{{ cfg.path }}</td>
                </tr>
                <tr>
                  <td class="mb-dim">工作目录</td>
                  <td class="mb-mono">{{ cfg.base_dir }}</td>
                </tr>
                <tr>
                  <td class="mb-dim">版本 / API</td>
                  <td class="mb-mono">v{{ cfg.runtime.version }}（api {{ cfg.runtime.api }}）· pid {{ cfg.runtime.pid }}</td>
                </tr>
                <tr>
                  <td class="mb-dim">已运行</td>
                  <td class="mb-mono">{{ fmtUptime(cfg.runtime.uptime_sec) }}</td>
                </tr>
                <tr>
                  <td class="mb-dim">监听 / 设备直连地址</td>
                  <td class="mb-mono">{{ cfg.runtime.listen }} · {{ cfg.runtime.public_base }}</td>
                </tr>
                <tr>
                  <td class="mb-dim">运行音箱</td>
                  <td class="mb-mono">{{ cfg.runtime.speakers }} 台（配置记录 {{ cfg.config.speakers?.length || 0 }} 台）</td>
                </tr>
                <tr>
                  <td class="mb-dim">敏感凭据</td>
                  <td class="mb-mono">{{ cfg.secrets_set.length ? cfg.secrets_set.join(', ') : '无（凭据在加密文件存储）' }}</td>
                </tr>
              </tbody>
            </v-table>

            <div class="d-flex ga-2 mt-4 flex-wrap">
              <v-btn color="primary" variant="flat" prepend-icon="$refresh" @click="emit('restart')">
                重启服务
              </v-btn>
              <v-btn variant="tonal" prepend-icon="$device" @click="go('device')">
                设备调试面板
              </v-btn>
              <v-btn variant="tonal" prepend-icon="$file" @click="showRaw = !showRaw">
                {{ showRaw ? '收起原始 JSON' : '查看原始 JSON' }}
              </v-btn>
            </div>

            <pre v-if="showRaw" class="mb-json mb-scroll mt-3">{{ JSON.stringify(cfg.config, null, 2) }}</pre>
          </v-card-text>
        </v-card>

        <v-card variant="flat" color="surface" rounded="lg">
          <v-card-title class="d-flex align-center ga-2">
            <v-icon icon="$restore" size="20" color="primary" /> 配置备份
            <span class="text-caption mb-dim">自动保留最近 10 份</span>
          </v-card-title>
          <v-card-text>
            <div v-if="!cfg?.backups.length" class="mb-dim text-body-medium py-2">
              暂无历史备份（首次在网页保存或回滚后自动生成）
            </div>
            <div v-for="b in cfg?.backups || []" :key="b.name" class="mb-bak">
              <div class="min-w-0 flex-1">
                <div class="text-body-medium text-truncate mb-mono">{{ b.name }}</div>
                <div class="text-caption mb-dim">{{ fmtTime(b.modified_at) }} · {{ fmtBytes(b.size) }}</div>
              </div>
              <v-btn
                size="small"
                variant="tonal"
                prepend-icon="$restore"
                :loading="busy === b.name"
                @click="confirmRestore = b"
              >
                回滚
              </v-btn>
            </div>
          </v-card-text>
        </v-card>
      </div>

      <!-- 右栏：最近日志 -->
      <div>
        <v-card variant="flat" color="surface" rounded="lg">
          <v-card-title class="d-flex align-center ga-2">
            <v-icon icon="$logs" size="20" color="primary" /> 最近日志
            <span class="text-caption mb-dim">内存环形缓冲（最新 500 行）</span>
            <v-spacer />
            <v-btn size="small" variant="text" prepend-icon="$refresh" :loading="loadingLogs" @click="loadLogs">
              刷新
            </v-btn>
          </v-card-title>
          <v-card-text>
            <div class="text-caption mb-dim mb-2">共 {{ logsTotal }} 行，展示最近 {{ logs.length }} 行</div>
            <pre class="mb-logs mb-scroll">{{ logs.join('\n') || '（暂无日志）' }}</pre>
          </v-card-text>
        </v-card>
      </div>
    </div>

    <!-- 确认回滚对话框 -->
    <v-dialog :model-value="!!confirmRestore" max-width="440" @update:model-value="confirmRestore = null">
      <v-card title="确认回滚配置？" variant="flat" color="surface">
        <v-card-text>
          确定将配置还原至备份 <code>{{ confirmRestore?.name }}</code> 吗？当前磁盘上的配置会自动留存一份新备份，回滚后需重启生效。
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="confirmRestore = null">取消</v-btn>
          <v-btn color="primary" variant="flat" @click="confirmRestore && doRestore(confirmRestore)">
            确认回滚
          </v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </div>
</template>

<style scoped>
.mb-admin-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr);
  gap: 14px;
  align-items: start;
}
@media (max-width: 1100px) {
  .mb-admin-grid {
    grid-template-columns: 1fr;
  }
}
.mb-bak {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
}
.mb-bak:last-child {
  border-bottom: 0;
}
.mb-logs,
.mb-json {
  margin: 0;
  padding: 10px 12px;
  border-radius: 10px;
  background: rgb(var(--v-theme-surface-variant));
  font-family: ui-monospace, 'SFMono-Regular', Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 60vh;
}
</style>
