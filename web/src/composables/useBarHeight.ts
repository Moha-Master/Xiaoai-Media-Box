import { ref } from 'vue'

/**
 * PlayerBar 的实测高度（px）。PlayerBar 挂载后用 ResizeObserver 维护，
 * 布局预留（CSS 变量 --mb-bar-height）和 snackbar offset 跟着它走，
 * 免得给桌面 / 移动 / 安全区三套高度硬编码。
 */
export const barHeight = ref(84)

export function setBarHeight(px: number) {
  barHeight.value = Math.max(1, Math.round(px))
  document.documentElement.style.setProperty('--mb-bar-height', `${barHeight.value}px`)
}
