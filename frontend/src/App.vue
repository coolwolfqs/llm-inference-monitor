<template>
  <div class="app-container">
    <header class="app-header">
      <div class="header-top">
        <h1 class="logo">InferenceHub <span class="version">v3</span></h1>
        <div class="status-bar">
          <span class="status-item" :class="{ connected: dashboard.sseConnected }">
            SSE {{ dashboard.sseConnected ? '已连接' : '未连接' }}
          </span>
          <span class="status-item health" :class="healthClass">
            健康度: {{ dashboard.healthScore }}
          </span>
          <span class="status-item" v-if="dashboard.lastUpdate">
            更新: {{ dashboard.lastUpdate.toLocaleTimeString('zh-CN') }}
          </span>
          <span class="status-item persist" @click="togglePersist" :title="'持久化: ' + persistMode">
            {{ persistIcon }} {{ persistLabel }}
          </span>
          <button class="theme-btn" @click="toggleTheme" :title="'切换' + (isDark ? '明亮' : '暗色') + '主题'">
            {{ isDark ? '☀️' : '🌙' }}
          </button>
          <div class="sys-dropdown">
            <button class="sys-btn" @click="showSysMenu = !showSysMenu" title="系统管理">⚙️</button>
            <div v-if="showSysMenu" class="sys-menu">
              <button class="sys-menu-item" @click="restartLlama">🔄 重启推理服务</button>
              <button class="sys-menu-item warn" @click="rebootSystem">⚠️ 重启系统</button>
              <button class="sys-menu-item danger" @click="shutdownSystem">🛑 关闭系统</button>
            </div>
          </div>
        </div>
      </div>

      <!-- 快捷导航栏 -->
      <nav class="quick-nav">
        <a v-for="link in quickLinks" :key="link.name" :href="link.url" :target="link.target || '_self'" class="nav-item" :class="{ active: link.active }">
          <span class="nav-dot" :style="{ background: link.dotColor }"></span>
          {{ link.label }}
          <span class="nav-url">{{ link.urlLabel }}</span>
        </a>
      </nav>

      <!-- 主导航 -->
      <nav class="nav-tabs">
        <router-link to="/overview" class="nav-tab">概览</router-link>
        <router-link to="/inference" class="nav-tab">推理</router-link>
        <router-link to="/compute" class="nav-tab">算力</router-link>
        <router-link to="/system" class="nav-tab">系统</router-link>
        <router-link to="/services" class="nav-tab">服务</router-link>
        <router-link to="/models" class="nav-tab">模型</router-link>
        <router-link to="/engines" class="nav-tab">引擎</router-link>
        <router-link to="/hardware" class="nav-tab">硬件</router-link>
        <router-link to="/alerts" class="nav-tab">告警</router-link>
      </nav>
    </header>

    <main class="app-main">
      <router-view></router-view>
    </main>

    <footer class="app-footer">
      <p>InferenceHub v3 - 运行时间: {{ dashboard.uptime || '加载中...' }}</p>
    </footer>

    <!-- 系统菜单遮罩 -->
    <div v-if="showSysMenu" class="sys-overlay" @click="showSysMenu = false"></div>

    <!-- 全局确认弹窗 -->
    <ConfirmDialog ref="confirmDialog" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { useDashboardStore } from './stores/dashboard'
import { systemAction } from './api'
import ConfirmDialog from './components/ConfirmDialog.vue'

const dashboard = useDashboardStore()
const confirmDialog = ref<InstanceType<typeof ConfirmDialog> | null>(null)

const healthClass = computed(() => {
  const score = dashboard.healthScore
  if (score >= 90) return 'good'
  if (score >= 70) return 'warning'
  return 'critical'
})

let refreshTimer: ReturnType<typeof setInterval> | null = null

// 快捷导航
const quickLinks = [
  { name: 'inference', label: '推理服务', url: 'http://10.1.1.4:8080/', urlLabel: ':8080', dotColor: '#3fb950' },
  { name: 'new-api', label: 'New API', url: 'http://10.1.1.4:3010/', urlLabel: ':3010', dotColor: '#58a6ff' },
  { name: 'model-mgr', label: '模型管理', url: 'http://10.1.1.4:8093/', urlLabel: ':8093', dotColor: '#bc8cff' },
  { name: 'cluster', label: '集群配置', url: 'http://10.1.1.4:8082/', urlLabel: ':8082', dotColor: '#a78bfa' },
  { name: 'benchmark', label: 'LLM测速', url: 'http://10.1.1.4:8090/', urlLabel: ':8090', dotColor: '#f0883e' },
  { name: 'searxng', label: 'SearXNG', url: 'http://10.1.1.4:8888/', urlLabel: ':8888', dotColor: '#10b981' },
]

// 持久化模式
const persistMode = ref('auto')
const persistIcon = computed(() => persistMode.value === 'auto' ? '🔒' : '🔓')
const persistLabel = computed(() => persistMode.value === 'auto' ? '自动' : '手动')

async function togglePersist() {
  const newMode = persistMode.value === 'auto' ? 'manual' : 'auto'
  try {
    const res = await fetch('/api/settings/persist', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: newMode })
    })
    if (res.ok) {
      persistMode.value = newMode
      localStorage.setItem('persist_mode', newMode)
    }
  } catch (e) { console.error(e) }
}

// 主题
const isDark = ref(true)
function applyTheme() {
  document.documentElement.classList.toggle('light-theme', !isDark.value)
  document.documentElement.style.colorScheme = isDark.value ? 'dark' : 'light'
}

function toggleTheme() {
  isDark.value = !isDark.value
  applyTheme()
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

// 系统菜单
const showSysMenu = ref(false)

async function restartLlama() {
  const ok = await confirmDialog.value?.show({
    title: '重启推理服务',
    message: '确定要重启推理服务吗？当前推理任务将中断。',
    icon: '\u{1f504}',
    confirmText: '重启'
  })
  if (!ok) { showSysMenu.value = false; return }
  try { await systemAction('restart_llama') }
  catch (e: any) { alert('失败: ' + e.message) }
  showSysMenu.value = false
}

async function rebootSystem() {
  if (!confirm('⚠️ 确定重启系统？所有服务将中断！')) return
  if (!confirm('再次确认：真的要重启系统吗？')) return
  try {
    await systemAction('reboot')
  } catch (e: any) { alert('失败: ' + e.message) }
  showSysMenu.value = false
}

async function shutdownSystem() {
  if (!confirm('🛑 确定关闭系统？')) return
  if (!confirm('再次确认：真的要关闭系统吗？')) return
  try {
    await systemAction('shutdown')
  } catch (e: any) { alert('失败: ' + e.message) }
  showSysMenu.value = false
}

async function initAllPanels() {
  await Promise.all([
    dashboard.fetchPanelData('overview'),
    dashboard.fetchPanelData('inference'),
    dashboard.fetchPanelData('system'),
    dashboard.fetchPanelData('hardware'),
    dashboard.fetchPanelData('services'),
  ])
}

onMounted(async () => {
  await initAllPanels()
  dashboard.connectSSE()
  refreshTimer = setInterval(() => { initAllPanels() }, 5000)

  // 加载持久化模式
  const savedMode = localStorage.getItem('persist_mode')
  if (savedMode) persistMode.value = savedMode

  // 加载主题
  const savedTheme = localStorage.getItem('theme')
  if (savedTheme === 'light') {
    isDark.value = false
  }
  applyTheme()

  // 监听全局 toast 事件
  window.addEventListener('app-toast', ((e: Event) => {
    const detail = (e as CustomEvent).detail
    const msg = document.createElement('div')
    msg.className = 'global-toast ' + (detail.type || 'info')
    msg.textContent = detail.message
    document.body.appendChild(msg)
    setTimeout(() => msg.remove(), 3000)
  }) as EventListener)

  // 监听全局 confirm 事件
  window.addEventListener('app-confirm', ((e: Event) => {
    const detail = (e as CustomEvent).detail
    if (confirmDialog.value) {
      confirmDialog.value.show(detail).then((ok: boolean) => {
        detail.resolve(ok)
      })
    } else {
      detail.resolve(window.confirm(detail.message))
    }
  }) as EventListener)
})

onUnmounted(() => {
  dashboard.disconnectSSE()
  if (refreshTimer) clearInterval(refreshTimer)
})
</script>

<style scoped>
.app-container { display: flex; flex-direction: column; min-height: 100vh; background: var(--bg-primary); }
.app-header { background: var(--bg-secondary); border-bottom: 1px solid var(--border-color); padding: 12px 16px; }
.header-top { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; }
.logo { font-size: 1.25rem; font-weight: 700; color: var(--text-primary); margin: 0; }
.version { font-size: 0.7rem; color: var(--accent-color); vertical-align: super; }
.status-bar { display: flex; align-items: center; gap: 10px; font-size: 0.75rem; }
.status-item { color: var(--text-secondary); }
.status-item.connected { color: var(--success-color); }
.status-item.health.good { color: var(--success-color); }
.status-item.health.warning { color: var(--warning-color); }
.status-item.health.critical { color: var(--error-color); }
.status-item.persist { cursor: pointer; padding: 2px 6px; border-radius: 4px; }
.status-item.persist:hover { background: var(--bg-hover); }
.theme-btn { background: none; border: none; font-size: 1rem; cursor: pointer; padding: 2px 4px; }
.sys-dropdown { position: relative; }
.sys-btn { background: none; border: none; font-size: 1rem; cursor: pointer; padding: 2px 4px; }
.sys-menu { position: absolute; right: 0; top: 100%; background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 8px; box-shadow: 0 8px 24px rgba(0,0,0,0.4); z-index: 100; min-width: 160px; overflow: hidden; }
.sys-menu-item { display: block; width: 100%; padding: 8px 12px; text-align: left; background: none; border: none; color: var(--text-primary); font-size: 0.8rem; cursor: pointer; }
.sys-menu-item:hover { background: var(--bg-hover); }
.sys-menu-item.warn { color: var(--warning-color); }
.sys-menu-item.danger { color: var(--error-color); }
.sys-overlay { position: fixed; inset: 0; z-index: 99; }

/* 快捷导航 */
.quick-nav { display: flex; gap: 6px; overflow-x: auto; padding-bottom: 8px; margin-bottom: 8px; -webkit-overflow-scrolling: touch; scrollbar-width: none; }
.quick-nav::-webkit-scrollbar { display: none; }
.nav-item { display: inline-flex; align-items: center; gap: 4px; padding: 4px 8px; border-radius: 6px; color: var(--text-secondary); text-decoration: none; font-size: 0.75rem; white-space: nowrap; transition: all 0.2s; }
.nav-item:hover { color: var(--text-primary); background: var(--bg-hover); }
.nav-item.active { color: var(--accent-color); background: var(--bg-active); }
.nav-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--text-muted); }
.nav-url { font-size: 0.65rem; color: var(--text-muted); }

/* 主导航 */
.nav-tabs { display: flex; gap: 4px; overflow-x: auto; -webkit-overflow-scrolling: touch; scrollbar-width: none; }
.nav-tabs::-webkit-scrollbar { display: none; }
.nav-tab { padding: 6px 12px; border-radius: 6px; color: var(--text-secondary); text-decoration: none; font-weight: 500; font-size: 0.85rem; white-space: nowrap; transition: all 0.2s; }
.nav-tab:hover { color: var(--text-primary); background: var(--bg-hover); }
.nav-tab.router-link-active { color: var(--accent-color); background: var(--bg-active); }
.app-main { flex: 1; padding: 16px; }
.app-footer { padding: 10px 16px; text-align: center; color: var(--text-muted); font-size: 0.75rem; border-top: 1px solid var(--border-color); }
@media (max-width: 768px) {
  .app-header { padding: 8px 12px; }
  .header-top { flex-direction: column; align-items: flex-start; gap: 6px; }
  .logo { font-size: 1.1rem; }
  .status-bar { font-size: 0.7rem; flex-wrap: wrap; }
  .app-main { padding: 12px; }
}
.global-toast { position: fixed; bottom: 24px; right: 24px; padding: 10px 18px; border-radius: 8px; color: white; font-size: 0.85rem; font-weight: 500; z-index: 3000; animation: slideInToast 0.2s ease; box-shadow: 0 4px 12px rgba(0,0,0,0.3); }
.global-toast.success { background: #10b981; }
.global-toast.error { background: #ef4444; }
.global-toast.info { background: #3b82f6; }
@keyframes slideInToast { from { transform: translateX(100%); opacity: 0; } to { transform: translateX(0); opacity: 1; } }
</style>
