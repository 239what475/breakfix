import { createApp } from 'vue'
import naive, { createDiscreteApi } from 'naive-ui'
import App from './App.vue'
import './style.css'

async function bootstrap() {
  const app = createApp(App)
  app.use(naive)

  const { message } = createDiscreteApi(['message'])
  app.config.globalProperties.$message = message

  app.mount('#app')
}

void bootstrap()
