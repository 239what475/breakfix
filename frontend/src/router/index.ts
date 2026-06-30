import { createRouter, createWebHashHistory } from 'vue-router'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('../views/LoginView.vue'),
    },
    {
      path: '/register',
      name: 'register',
      component: () => import('../views/RegisterView.vue'),
    },
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
