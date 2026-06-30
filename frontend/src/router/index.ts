import { createRouter, createWebHashHistory } from 'vue-router'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: '/',
      name: 'challenges',
      component: () => import('../views/ChallengeList.vue'),
    },
    {
      path: '/terminal/:id',
      name: 'terminal',
      component: () => import('../views/TerminalView.vue'),
    },
  ],
})

export default router
