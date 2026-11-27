import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import vuetify from 'vite-plugin-vuetify'

// 构建产物直接落到 Go 包目录里，由 internal/app/web.go 的 go:embed 内嵌。
// 前端不经过任何反向代理：生产环境同源，dev 用下面的 proxy 打到 :8088。
export default defineConfig({
  plugins: [
    vue(),
    vuetify({ autoImport: true }),
  ],
  build: {
    outDir: '../internal/app/web/dist',
    emptyOutDir: true,
    target: 'es2022',
    // 内嵌进二进制的东西不该带 sourcemap；要调试时临时打开
    sourcemap: false,
  },
  // Vuetify 4 用 useStack 单例算 overlay 的 z-index。Vite dev 的预打包可能把
  // overlay 相关模块拆成两份实例，表现为对话框里的菜单/下拉被压在下面（生产不受影响）。
  // 官方给的解法是强制一起预打包，见 docs → Tooling → overlay z-index。
  optimizeDeps: {
    include: [
      'vuetify/components/VOverlay',
      'vuetify/components/VDialog',
      'vuetify/components/VMenu',
      'vuetify/components/VSelect',
      'vuetify/components/VTooltip',
    ],
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8088',
      '/healthz': 'http://127.0.0.1:8088',
      '/s': 'http://127.0.0.1:8088',
    },
  },
})
