<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import * as api from '../../api/client'
import type { XiaomiAuthStatus, XiaomiDevice, XiaomiQRStart } from '../../api/types'
import { player } from '../../stores/player'
import { fmtTime } from '../../utils/format'

// ---- 网易云状态与弹窗 ----
const neteaseLoading = ref(false)
const ncmQrDialog = ref(false)
const ncmQrImage = ref('')
const ncmQrKey = ref('')
const ncmQrStatus = ref('')
let ncmQrTimer: number | undefined

const ncmCookieDialog = ref(false)
const ncmCookieInput = ref('')
const ncmCookieSubmitting = ref(false)

// ---- 小米云端状态与弹窗 ----
const xiaomiStatus = ref<XiaomiAuthStatus | null>(null)
const xiaomiLoading = ref(false)
const xiaomiChecking = ref(false)
const xiaomiDevices = ref<XiaomiDevice[]>([])
const loadingDevices = ref(false)

const miQrDialog = ref(false)
const miQrSession = ref<XiaomiQRStart | null>(null)
const miQrStatus = ref('')
let miQrTimer: number | undefined

const copySuccess = ref(false)

// ---------- 网易云动作 ----------

async function doNeteaseLogout() {
  neteaseLoading.value = true
  try {
    await api.logoutNetease()
    await player.loadAccount()
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    neteaseLoading.value = false
  }
}

async function openNcmQR() {
  ncmQrDialog.value = true
  ncmQrStatus.value = '正在申请二维码…'
  ncmQrImage.value = ''
  stopNcmQrTimer()
  try {
    const { blob, key } = await api.loginQR()
    ncmQrKey.value = key
    ncmQrImage.value = URL.createObjectURL(blob)
    ncmQrStatus.value = '请使用网易云音乐 App 扫码登录'

    ncmQrTimer = window.setInterval(async () => {
      try {
        const res = await api.checkLoginQR(key)
        if (res.code === 800) {
          ncmQrStatus.value = '二维码已过期，请刷新'
          stopNcmQrTimer()
        } else if (res.code === 802) {
          ncmQrStatus.value = `已扫码 (${res.nickname || ''})，请在手机上点击确认`
        } else if (res.code === 803) {
          ncmQrStatus.value = '登录成功！'
          stopNcmQrTimer()
          await player.loadAccount()
          setTimeout(() => {
            ncmQrDialog.value = false
          }, 1200)
        }
      } catch {
        /* 继续轮询 */
      }
    }, 2000)
  } catch (e) {
    ncmQrStatus.value = (e as Error).message
  }
}

function stopNcmQrTimer() {
  if (ncmQrTimer) {
    clearInterval(ncmQrTimer)
    ncmQrTimer = undefined
  }
}

async function submitNcmCookie() {
  const val = ncmCookieInput.value.trim()
  if (!val) return
  ncmCookieSubmitting.value = true
  try {
    await api.loginWithCookie(val)
    await player.loadAccount()
    ncmCookieDialog.value = false
    ncmCookieInput.value = ''
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    ncmCookieSubmitting.value = false
  }
}

// ---------- 小米云端动作 ----------

async function loadXiaomiStatus() {
  xiaomiLoading.value = true
  try {
    xiaomiStatus.value = await api.getXiaomiStatus()
    if (xiaomiStatus.value.logged_in) {
      loadXiaomiDevices()
    }
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    xiaomiLoading.value = false
  }
}

async function loadXiaomiDevices() {
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

async function doXiaomiLogout() {
  xiaomiLoading.value = true
  try {
    await api.logoutXiaomi()
    xiaomiDevices.value = []
    await loadXiaomiStatus()
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    xiaomiLoading.value = false
  }
}

async function doXiaomiCheck() {
  xiaomiChecking.value = true
  try {
    const res = await api.checkXiaomiAuth()
    if (res.devices) {
      xiaomiDevices.value = res.devices
    }
    await loadXiaomiStatus()
  } catch (e) {
    player.error = (e as Error).message
  } finally {
    xiaomiChecking.value = false
  }
}

async function openXiaomiQR() {
  miQrDialog.value = true
  miQrStatus.value = '正在申请授权二维码…'
  miQrSession.value = null
  stopMiQrTimer()
  try {
    const res = await api.startXiaomiQR()
    miQrSession.value = res
    miQrStatus.value = '请在手机浏览器打开链接授权，或用小米手机相机扫码'

    miQrTimer = window.setInterval(async () => {
      try {
        const check = await api.checkXiaomiQR(res.session_id)
        if (check.status === 'authorized') {
          miQrStatus.value = `授权成功！User ID: ${check.user_id || ''}`
          stopMiQrTimer()
          await loadXiaomiStatus()
          setTimeout(() => {
            miQrDialog.value = false
          }, 1200)
        } else if (check.status === 'expired') {
          miQrStatus.value = '二维码已过期，请关闭重试'
          stopMiQrTimer()
        } else if (check.status === 'failed') {
          miQrStatus.value = check.error || '授权失败'
          stopMiQrTimer()
        }
      } catch {
        /* 继续轮询 */
      }
    }, 2500)
  } catch (e) {
    miQrStatus.value = (e as Error).message
  }
}

function stopMiQrTimer() {
  if (miQrTimer) {
    clearInterval(miQrTimer)
    miQrTimer = undefined
  }
}

function copyToClipboard(text: string) {
  navigator.clipboard.writeText(text)
  copySuccess.value = true
  setTimeout(() => (copySuccess.value = false), 2000)
}

onMounted(() => {
  loadXiaomiStatus()
})

onUnmounted(() => {
  stopNcmQrTimer()
  stopMiQrTimer()
})
</script>

<template>
  <div class="d-flex flex-column ga-4">
    <!-- 网易云账号卡片 -->
    <v-card variant="flat" color="surface" rounded="lg">
      <v-card-title class="d-flex align-center ga-2">
        <v-icon icon="$music" size="20" color="primary" /> 网易云音乐账号
        <v-spacer />
        <v-chip
          size="small"
          :color="player.account ? 'accent' : 'warning'"
          variant="tonal"
          :prepend-icon="player.account ? '$ok' : '$warn'"
        >
          {{ player.account ? '已登录' : '未登录' }}
        </v-chip>
      </v-card-title>
      <v-card-text>
        <div v-if="player.account" class="d-flex align-center justify-space-between flex-wrap ga-3 py-1">
          <div class="d-flex align-center ga-3">
            <v-avatar color="surface-variant" size="44">
              <v-icon icon="$account" size="28" />
            </v-avatar>
            <div>
              <div class="text-body-large font-weight-medium">{{ player.account.nickname }}</div>
              <div class="text-caption mb-dim mb-mono">UID: {{ player.account.uid }}</div>
            </div>
          </div>

          <div class="d-flex align-center ga-2">
            <v-btn variant="tonal" size="small" prepend-icon="$qrcode" @click="openNcmQR">重新扫码</v-btn>
            <v-btn variant="tonal" size="small" prepend-icon="$key" @click="ncmCookieDialog = true">导入 Cookie</v-btn>
            <v-btn color="error" variant="tonal" size="small" prepend-icon="$logout" :loading="neteaseLoading" @click="doNeteaseLogout">
              退出登录
            </v-btn>
          </div>
        </div>

        <div v-else class="py-2">
          <v-alert type="warning" variant="tonal" density="compact" class="mb-3">
            网易云未登录。登录后可拉取你的个人歌单、红心收藏、每日推荐以及获得完整高音质取流权限。
          </v-alert>
          <div class="d-flex ga-2">
            <v-btn color="primary" variant="flat" prepend-icon="$qrcode" @click="openNcmQR">网易云 App 扫码登录</v-btn>
            <v-btn variant="tonal" prepend-icon="$key" @click="ncmCookieDialog = true">导入 Cookie</v-btn>
          </div>
        </div>
      </v-card-text>
    </v-card>

    <!-- 小米账号卡片 -->
    <v-card variant="flat" color="surface" rounded="lg">
      <v-card-title class="d-flex align-center ga-2">
        <v-icon icon="$phone" size="20" color="primary" /> 小米账号（云端 MiNA 通道）
        <v-spacer />
        <v-chip
          size="small"
          :color="xiaomiStatus?.logged_in ? 'accent' : 'warning'"
          variant="tonal"
          :prepend-icon="xiaomiStatus?.logged_in ? '$ok' : '$warn'"
        >
          {{ xiaomiStatus?.logged_in ? '已授权凭据' : '未登录' }}
        </v-chip>
      </v-card-title>
      <v-card-text>
        <div v-if="xiaomiStatus?.logged_in" class="d-flex flex-column ga-3">
          <div class="d-flex align-center justify-space-between flex-wrap ga-3">
            <div>
              <div class="text-body-medium">
                用户 ID: <span class="mb-mono font-weight-medium">{{ xiaomiStatus.user_id || '（已加密）' }}</span>
              </div>
              <div class="text-caption mb-dim mb-mono">
                绑定设备 ID: {{ xiaomiStatus.device_id }} · 上次更新: {{ fmtTime(xiaomiStatus.updated_at || 0) }}
              </div>
            </div>

            <div class="d-flex align-center ga-2">
              <v-btn
                variant="tonal"
                size="small"
                prepend-icon="$shield"
                :loading="xiaomiChecking"
                @click="doXiaomiCheck"
              >
                体检凭据 / 刷新设备
              </v-btn>
              <v-btn variant="tonal" size="small" prepend-icon="$qrcode" @click="openXiaomiQR">
                重新授权
              </v-btn>
              <v-btn
                color="error"
                variant="tonal"
                size="small"
                prepend-icon="$logout"
                :loading="xiaomiLoading"
                @click="doXiaomiLogout"
              >
                清除凭据
              </v-btn>
            </div>
          </div>

          <!-- 小米名下小爱设备列表 -->
          <div class="mt-2">
            <div class="d-flex align-center justify-space-between mb-2">
              <span class="text-body-medium font-weight-medium">名下音箱列表 ({{ xiaomiDevices.length }} 台)</span>
              <v-btn size="x-small" variant="text" prepend-icon="$refresh" :loading="loadingDevices" @click="loadXiaomiDevices">
                刷新列表
              </v-btn>
            </div>

            <v-table density="compact" class="mb-device-table">
              <thead>
                <tr>
                  <th>音箱名称</th>
                  <th>在线状态</th>
                  <th>Device ID (UUID)</th>
                  <th>型号</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="d in xiaomiDevices" :key="d.device_id">
                  <td>{{ d.name }}</td>
                  <td>
                    <v-chip size="x-small" :color="d.presence === 'online' ? 'accent' : 'secondary'" variant="tonal">
                      {{ d.presence === 'online' ? '在线' : '离线' }}
                    </v-chip>
                  </td>
                  <td class="mb-mono text-caption">{{ d.device_id }}</td>
                  <td class="mb-dim text-caption">{{ d.hardware }}</td>
                  <td>
                    <v-btn
                      size="x-small"
                      variant="text"
                      icon="$copy"
                      title="复制 Device ID"
                      @click="copyToClipboard(d.device_id)"
                    />
                  </td>
                </tr>
                <tr v-if="!xiaomiDevices.length">
                  <td colspan="5" class="text-center mb-dim py-3">
                    {{ loadingDevices ? '正在加载设备列表…' : '暂未拉取到设备列表，可点击上方「体检凭据」刷新' }}
                  </td>
                </tr>
              </tbody>
            </v-table>
          </div>
        </div>

        <div v-else class="py-2">
          <v-alert type="info" variant="tonal" density="compact" class="mb-3">
            若您的音箱选择「小米云端控制 (cloud)」，需登录小米账号获取官方 ubus 代理调用权限（免密码、免 SSH 破解）。
          </v-alert>
          <v-btn color="primary" variant="flat" prepend-icon="$qrcode" @click="openXiaomiQR">
            扫码 / 浏览器一键授权
          </v-btn>
        </div>
      </v-card-text>
    </v-card>

    <!-- 网易云扫码弹窗 -->
    <v-dialog v-model="ncmQrDialog" max-width="360" @update:model-value="stopNcmQrTimer">
      <v-card title="网易云 App 扫码登录" variant="flat" color="surface">
        <v-card-text class="d-flex flex-column align-center ga-3 py-4">
          <div v-if="ncmQrImage" class="bg-white pa-2 rounded-lg">
            <img :src="ncmQrImage" width="220" height="220" alt="网易云二维码" style="display: block" />
          </div>
          <v-progress-circular v-else indeterminate color="primary" size="48" />
          <div class="text-body-medium text-center">{{ ncmQrStatus }}</div>
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="ncmQrDialog = false">关闭</v-btn>
          <v-btn color="primary" variant="tonal" @click="openNcmQR">刷新二维码</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <!-- 网易云 Cookie 弹窗 -->
    <v-dialog v-model="ncmCookieDialog" max-width="500">
      <v-card title="导入网易云 Cookie" variant="flat" color="surface">
        <v-card-text>
          <p class="text-caption mb-dim mb-2">
            请粘贴包含 <code>MUSIC_U=...</code> 的完整 Cookie 字符串。导入成功后将加密存储于本地。
          </p>
          <v-textarea
            v-model="ncmCookieInput"
            label="Cookie 内容"
            rows="4"
            density="compact"
            placeholder="MUSIC_U=...; __csrf=..."
          />
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="ncmCookieDialog = false">取消</v-btn>
          <v-btn color="primary" variant="flat" :loading="ncmCookieSubmitting" @click="submitNcmCookie">
            确认导入
          </v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <!-- 小米扫码弹窗 -->
    <v-dialog v-model="miQrDialog" max-width="400" @update:model-value="stopMiQrTimer">
      <v-card title="小米账号扫码 / 授权" variant="flat" color="surface">
        <v-card-text class="d-flex flex-column align-center ga-3 py-3">
          <div v-if="miQrSession?.qr" class="bg-white pa-2 rounded-lg">
            <img :src="miQrSession.qr" width="220" height="220" alt="小米授权二维码" style="display: block" />
          </div>
          <v-progress-circular v-else indeterminate color="primary" size="48" />

          <div class="text-body-medium text-center">{{ miQrStatus }}</div>

          <div v-if="miQrSession?.login_url" class="text-center mt-1">
            <div class="text-caption mb-dim mb-1">若当前浏览器已登录小米账号，可直接点击：</div>
            <v-btn
              color="primary"
              variant="tonal"
              size="small"
              :href="miQrSession.login_url"
              target="_blank"
              prepend-icon="$openInNew"
            >
              在浏览器中打开授权链接
            </v-btn>
          </div>
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="miQrDialog = false">取消</v-btn>
          <v-btn color="primary" variant="tonal" @click="openXiaomiQR">刷新二维码</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-snackbar v-model="copySuccess" timeout="2000" color="success">
      已复制 Device ID 到剪贴板
    </v-snackbar>
  </div>
</template>

<style scoped>
.mb-device-table :deep(th) {
  white-space: nowrap;
}
.mb-device-table :deep(td) {
  white-space: nowrap;
}
</style>
