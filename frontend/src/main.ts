import { createApp } from 'vue'
import naive, { createDiscreteApi } from 'naive-ui'
import App from './App.vue'
import router from './router'

const app = createApp(App)
app.use(naive)
app.use(router)

// Make message available globally (before components mount)
const { message } = createDiscreteApi(['message'])
app.config.globalProperties.$message = message

app.mount('#app')
