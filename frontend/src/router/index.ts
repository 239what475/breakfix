import { createRouter, createWebHashHistory } from 'vue-router'
import App from '../App.vue'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', component: App },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

export default router
