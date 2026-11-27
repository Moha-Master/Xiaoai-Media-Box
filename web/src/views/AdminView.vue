<script setup lang="ts">
import { ref, watch } from 'vue'
import * as api from '../api/client'
import type { AdminConfigReply, AdminPutReply } from '../api/client'
import AdminAuthPane from '../components/admin/AdminAuthPane.vue'
import AdminConfigPane from '../components/admin/AdminConfigPane.vue'
import AdminStatusPane from '../components/admin/AdminStatusPane.vue'
import { useHashRoute } from '../composables/useHashRoute'
import { player } from '../stores/player'

const { routeQuery } = useHashRoute()
const activeTab = ref<'config' | 'auth' | 'status'>('config')

// 首页「我的歌单」未登录引导用 ?tab=auth 直达账号 tab
watch(
  () => routeQuery.value.get('tab'),
  (t) => {
    if (t === 'auth' || t === 'config' || t === 'status') activeTab.value = t
  },
  { immediate: true },
)

const loading = ref(true)
const cfg = ref<AdminConfigReply | null>(null)
const confirmRestart = ref(false)
const restartSuccessNote = ref('')

async function load() {
  loading.value = true
  try {
    cfg.value = await api.adminGetConfig()
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function doRestart() {
  confirmRestart.value = false
  try {
    await api.adminRestart()
    restartSuccessNote.value = '已触发原地自重启（PID 保持不变），各音箱状态已先行落盘。'
    setTimeout(() => {
      load()
      restartSuccessNote.value = ''
    }, 2500)
  } catch (e) {
    player.error = (e as Error).message
  }
}

function onConfigSaved(reply: AdminPutReply) {
  load()
  if (reply.requires_restart) {
    confirmRestart.value = true
  }
}

load()
</script>

<template>
  <div class="mb-admin-page">
    <v-progress-linear v-if="loading" indeterminate height="2" color="primary" />

    <v-alert v-if="restartSuccessNote" type="success" variant="tonal" class="mb-3">
      {{ restartSuccessNote }}
    </v-alert>

    <v-card variant="flat" color="surface" rounded="lg" class="mb-4">
      <v-tabs v-model="activeTab" density="comfortable" color="primary" slider-size="3">
        <v-tab value="config">
          <v-icon icon="$settings" size="18" start />
          配置与音箱
          <v-badge
            v-if="cfg?.requires_restart"
            color="warning"
            dot
            inline
            class="ml-1"
          />
        </v-tab>
        <v-tab value="auth">
          <v-icon icon="$account" size="18" start />
          账号与认证
        </v-tab>
        <v-tab value="status">
          <v-icon icon="$info" size="18" start />
          运维与状态
          <v-badge
            v-if="cfg?.requires_restart"
            color="warning"
            content="待重启"
            inline
            class="ml-1"
          />
        </v-tab>
      </v-tabs>

      <v-divider />

      <div class="pa-4">
        <v-window v-model="activeTab">
          <v-window-item value="config">
            <AdminConfigPane :cfg="cfg" :loading="loading" @saved="onConfigSaved" />
          </v-window-item>

          <v-window-item value="auth">
            <AdminAuthPane />
          </v-window-item>

          <v-window-item value="status">
            <AdminStatusPane :cfg="cfg" :loading="loading" @reload="load" @restart="confirmRestart = true" />
          </v-window-item>
        </v-window>
      </div>
    </v-card>

    <!-- 重启确认对话框 -->
    <v-dialog v-model="confirmRestart" max-width="460">
      <v-card title="重启服务以应用配置？" variant="flat" color="surface">
        <v-card-text>
          <p class="mb-2">
            音箱拓扑、监听地址、通道绑定与认证路径在服务启动期装配完成，需要重启服务生效。
          </p>
          <p class="text-caption mb-dim mb-0">
            重启走 <code>syscall.Exec</code> 原地替换：PID 不变，systemd 无感，根 context 会先取消并完整刷写播放队列快照。
          </p>
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="confirmRestart = false">稍后重启</v-btn>
          <v-btn color="primary" variant="flat" prepend-icon="$refresh" @click="doRestart">立即重启</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </div>
</template>

<style scoped>
.mb-admin-page {
  padding: 14px 16px 32px;
  max-width: 1500px;
  margin: 0 auto;
}
@media (max-width: 1100px) {
  .mb-admin-page {
    padding: 10px 10px 32px;
  }
}
</style>
