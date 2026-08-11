<template>
  <div class="panel-grid">
    <div class="card card-full">
      <div class="card-header"><h3>服务状态</h3></div>
      <div class="card-body">
        <table class="data-table">
          <thead><tr><th>服务名称</th><th>状态</th><th>地址</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-for="svc in services" :key="svc.name">
              <td>{{ svc.name }}</td>
              <td><span :class="['status-badge', 'status-' + svc.status]">{{ statusText(svc.status) }}</span></td>
              <td class="url-cell">{{ publicUrl(svc.url) }}</td>
              <td><a :href="publicUrl(svc.url)" target="_blank" class="btn-link">打开</a></td>
            </tr>
            <tr v-if="services.length === 0"><td colspan="4" class="empty-cell">暂无服务状态数据</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="card card-full">
      <div class="card-header"><h3>快捷入口</h3></div>
      <div class="card-body">
        <div class="links-grid">
          <a v-for="(url, name) in links" :key="name" :href="publicUrl(url)" target="_blank" class="link-card">
            <span class="link-icon">{{ linkIcon(name) }}</span>
            <span class="link-name">{{ linkName(name) }}</span>
            <span class="link-url">{{ publicUrl(url) }}</span>
          </a>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useDashboardStore } from '../stores/dashboard'
const dashboard = useDashboardStore()
const services = computed(() => dashboard.services?.services || [])
const links = computed(() => dashboard.services?.links || {})

function publicUrl(url: any): string {
  if (!url) return '#'
  return String(url)
    .replace('http://127.0.0.1:', 'http://10.1.1.4:')
    .replace('http://localhost:', 'http://10.1.1.4:')
}

function statusText(s: string): string {
  if (s === 'healthy') return '正常'
  if (s === 'degraded') return '降级'
  if (s === 'down') return '宕机'
  return s
}

function linkIcon(name: string): string {
  const icons: Record<string, string> = {
    cluster: '🖧', benchmark: '⚡', searxng: '🔎', victoriametrics: '📈'
  }
  return icons[name] || '🔗'
}

function linkName(name: string): string {
  const names: Record<string, string> = {
    inference: '推理服务', new_api: 'New API', model_manager: '模型管理',
    searxng: 'SearXNG', victoriametrics: 'VictoriaMetrics'
  }
  return names[name] || name
}
</script>

<style scoped>
.data-table { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
.data-table th, .data-table td { padding: 8px 10px; text-align: left; border-bottom: 1px solid var(--border-color); }
.data-table th { color: var(--text-secondary); font-weight: 600; }
.url-cell { font-family: monospace; font-size: 0.75rem; color: var(--text-secondary); }
.status-badge { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 0.75rem; font-weight: 600; }
.status-healthy { background: rgba(16,185,129,0.2); color: #10b981; }
.status-degraded { background: rgba(245,158,11,0.2); color: #f59e0b; }
.status-down { background: rgba(239,68,68,0.2); color: #ef4444; }
.btn-link { color: var(--accent-color); text-decoration: none; font-size: 0.75rem; }
.btn-link:hover { text-decoration: underline; }
.empty-cell { text-align: center; color: var(--text-muted); padding: 18px 10px; }
.links-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 10px; }
.link-card { display: flex; flex-direction: column; padding: 12px; background: var(--bg-hover); border-radius: 8px; text-decoration: none; transition: all 0.2s; }
.link-card:hover { background: var(--bg-active); transform: translateY(-2px); }
.link-icon { font-size: 1.5rem; margin-bottom: 4px; }
.link-name { font-weight: 600; font-size: 0.85rem; color: var(--text-primary); }
.link-url { font-size: 0.7rem; color: var(--text-muted); margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
