import './styles/main.css'
import { createApp } from 'vue'
import App from './App.vue'
import vuetify from './plugins/vuetify'

const app = createApp(App)
app.use(vuetify)
app.mount('#app')

// 骨架屏撤掉：挂载成功就说明前端真的起来了
document.getElementById('boot')?.remove()
