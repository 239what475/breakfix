<script setup lang="ts">
import { NConfigProvider, NGlobalStyle, NMessageProvider, darkTheme } from 'naive-ui'
import AppShell from './AppShell.vue'

const themeOverrides = {
  common: {
    primaryColor: '#58a6ff', primaryColorHover: '#79b8ff',
    bodyColor: '#0d1117', cardColor: '#161b22', modalColor: '#161b22', popoverColor: '#161b22',
    borderColor: '#30363d',
    textColorBase: '#e6edf3', textColor2: '#8b949e', textColor3: '#484f58',
    scrollbarColor: '#30363d', scrollbarColorHover: '#484f58',
    fontSize: '13px', borderRadius: '6px', heightMedium: '36px',
  },
  Tag: { borderRadius: '9999px' },
  Layout: { headerColor: '#161b22', headerBorderColor: '#30363d', siderColor: '#0d1117', siderBorderColor: '#30363d' },
}
</script>

<template>
  <n-config-provider :theme="darkTheme" :theme-overrides="themeOverrides">
    <n-message-provider>
      <n-global-style />
      <AppShell />
    </n-message-provider>
  </n-config-provider>
</template>
