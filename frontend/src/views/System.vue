<template>
  <div class="panel-grid">
    <!-- OS 信息 -->
    <div class="card card-full">
      <div class="card-header"><h3>🖥️ 操作系统</h3></div>
      <div class="card-body">
        <div class="os-grid">
          <div class="os-item"><span class="os-label">主机名</span><span class="os-val">{{ sys?.hostname || '-' }}</span></div>
          <div class="os-item"><span class="os-label">操作系统</span><span class="os-val">{{ sys?.os_name || sys?.os || '-' }}</span></div>
          <div class="os-item"><span class="os-label">内核</span><span class="os-val">{{ sys?.kernel || '-' }}</span></div>
          <div class="os-item"><span class="os-label">架构</span><span class="os-val">{{ sys?.arch || '-' }}</span></div>
          <div class="os-item"><span class="os-label">时区</span><span class="os-val">{{ sys?.timezone || '-' }}</span></div>
          <div class="os-item"><span class="os-label">运行时间</span><span class="os-val">{{ sys?.uptime || '-' }}</span></div>
          <div class="os-item"><span class="os-label">在线用户</span><span class="os-val">{{ sys?.users || 0 }}</span></div>
        </div>
      </div>
    </div>

    <!-- 系统资源 -->
    <div class="card card-half">
      <div class="card-header"><h3>CPU</h3></div>
      <div class="card-body">
        <div class="metric-row"><span class="metric-label">使用率</span><span class="metric-value">{{ fmt(sys?.cpu_util) }}%</span></div>
        <div class="metric-row"><span class="metric-label">负载 1/5/15</span><span class="metric-value">{{ fmt2(sys?.load_1) }} / {{ fmt2(sys?.load_5) }} / {{ fmt2(sys?.load_15) }}</span></div>
        <div class="metric-row"><span class="metric-label">进程数</span><span class="metric-value">{{ sys?.process_count || 0 }}</span></div>
        <div class="metric-row"><span class="metric-label">文件句柄</span><span class="metric-value">{{ sys?.file_handles_used || '-' }}</span></div>
      </div>
    </div>

    <div class="card card-half">
      <div class="card-header"><h3>内存</h3></div>
      <div class="card-body">
        <div class="metric-row"><span class="metric-label">总量</span><span class="metric-value">{{ gb(sys?.mem_total) }} GB</span></div>
        <div class="metric-row"><span class="metric-label">已用</span><span class="metric-value">{{ gb(sys?.mem_used) }} GB ({{ fmt(sys?.mem_used_pct) }}%)</span></div>
        <div class="metric-row"><span class="metric-label">可用</span><span class="metric-value">{{ gb(sys?.mem_available) }} GB</span></div>
        <div class="metric-row"><span class="metric-label">Swap</span><span class="metric-value">{{ fmt(sys?.swap_used_pct) }}%</span></div>
      </div>
    </div>

    <div class="card card-full">
      <div class="card-header"><h3>磁盘</h3></div>
      <div class="card-body">
        <table class="data-table">
          <thead><tr><th>设备</th><th>挂载点</th><th>总量</th><th>已用</th><th>剩余</th><th>使用率</th></tr></thead>
          <tbody>
            <tr v-for="d in sys?.disks || []" :key="d.mountpoint">
              <td>{{ d.device }}</td><td>{{ d.mountpoint }}</td><td>{{ fmtBytes(d.total) }}</td><td>{{ fmtBytes(d.used) }}</td><td>{{ fmtBytes(d.free) }}</td>
              <td><div class="progress-bar"><div class="progress-fill" :style="{ width: d.used_pct + '%', background: diskColor(d.used_pct) }"></div><span class="progress-text">{{ d.used_pct?.toFixed(1) }}%</span></div></td>
            </tr>
            <tr v-if="!(sys?.disks && sys.disks.length)"><td colspan="6" class="empty-cell">暂无磁盘分区数据</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 服务状态 -->
    <div class="card card-full">
      <div class="card-header"><h3>🔧 服务状态</h3></div>
      <div class="card-body">
        <div v-if="serviceList.length === 0" class="empty">暂无服务数据</div>
        <div v-else class="service-list">
          <div v-for="svc in serviceList" :key="svc.name" class="service-item" :class="{ expanded: expandedService === svc.name }">
            <div class="service-header" @click="toggleService(svc.name)">
              <span class="service-status-dot" :class="svc.status"></span>
              <span class="service-name">{{ svc.name }}</span>
              <span class="service-detail-preview" v-if="svc.name === 'llama-server' && svc.running_model">{{ svc.running_model }}</span>
              <span class="service-status-text">{{ serviceStatusText(svc.status) }}</span>
              <span class="service-expand-icon">{{ expandedService === svc.name ? '▲' : '▼' }}</span>
            </div>
            <div v-if="expandedService === svc.name && svc.name === 'llama-server' && svc.params" class="service-detail-panel">
              <div class="detail-section-title">运行模型</div>
              <div class="detail-model-name">{{ svc.running_model || '-' }}</div>
              <div class="detail-grid">
                <div class="detail-item" v-for="(val, key) in svc.params" :key="key">
                  <span class="detail-label">{{ formatLabel(key) }}</span>
                  <span class="detail-value">{{ formatValue(key, val) }}</span>
                </div>
              </div>
              <div v-if="svc.deploy_config" class="detail-section-title" style="margin-top:8px">部署配置</div>
              <div v-if="svc.deploy_config" class="detail-grid">
                <div class="detail-item" v-for="(val, key) in svc.deploy_config" :key="'dc-'+key">
                  <span class="detail-label">{{ formatLabel(key) }}</span>
                  <span class="detail-value">{{ formatValue(key, val) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useDashboardStore } from '../stores/dashboard'

const dashboard = useDashboardStore()
const sys = computed(() => dashboard.system?.system || {})
const services = computed(() => dashboard.system?.services || dashboard.services?.services || null)
const expandedService = ref<string | null>(null)
const overviewParams = ref<any>(null)

function toggleService(name: string) {
  expandedService.value = expandedService.value === name ? null : name
}

// Fetch overview for params
import { getModels } from '../api'
async function loadOverviewParams() {
  try {
    const res = await getModels()
    const cfg = res.data?.current_config
    if (cfg) {
      overviewParams.value = cfg
    }
  } catch (e) { /* ignore */ }
}
loadOverviewParams()
const serviceList = computed(() => {
  const s = services.value
  if (!s) return []
  let list = Array.isArray(s) ? s : Object.entries(s).map(([name, info]: [string, any]) => ({
    name,
    status: info.status || 'unknown',
    port: info.port,
    url: info.port ? `http://127.0.0.1:${info.port}` : '',
    detail: info.detail || '',
    config: info.config || null,
    engine: info.engine || '',
    llama_version: info.llama_version || '',
    pid: info.pid || 0,
    params: info.params || null,
    deploy_config: info.deploy_config || null,
    running_model: info.running_model || null,
    active_engine: info.active_engine || null,
  }))
  // Enrich llama-server with overview params if available
  if (overviewParams.value) {
    list = list.map(svc => {
      if (svc.name === 'llama-server' && !svc.params) {
        return {
          ...svc,
          params: overviewParams.value,
          running_model: overviewParams.value.model_path?.split('/').pop() || overviewParams.value.model || '',
        }
      }
      return svc
    })
  }
  return list
})

function formatLabel(key: string): string {
  const labels: Record<string, string> = {
    ctx_size: '上下文大小', ngl: 'GPU层数', batch: 'Batch',
    ubatch: 'UBatch', threads: '线程数', threads_http: 'HTTP线程',
    np: '并发数', concurrency: '并发数', temp: '温度',
    cache_type_k: 'K Cache', cache_type_v: 'V Cache',
    k_cache_type: 'K Cache', v_cache_type: 'V Cache',
    flash_attn: 'Flash Attn', chunked_batch: '连续批处理',
    reasoning: '推理模式', mmproj: '视觉插件',
    spec_draft_n_max: 'Spec Draft', split_mode: 'Split Mode',
    fit: 'Fit', cache_ram: 'Cache RAM', sleep_idle_seconds: '空闲休眠',
    tensor_split: 'GPU分配', gpu: 'GPU',
    draft_k_cache_type: 'Draft K Cache', draft_v_cache_type: 'Draft V Cache',
    n_ctx_per_slot: '每Slot上下文', total_slots: '总槽位数',
    temperature: 'Temperature', top_k: 'Top K', top_p: 'Top P',
    min_p: 'Min P', chat_format: '对话格式', host: '主机', port: '端口',
    model: '模型', model_path: '模型路径', alias: '别名',
    active_engine: '引擎', mmproj_file: '视觉模块',
  }
  return labels[key] || key
}

function formatValue(key: string, val: any): string {
  if (val === null || val === undefined) return '-'
  if (key === 'flash_attn' || key === 'chunked_batch' || key === 'mmproj' || key === 'reasoning') {
    return val === 'on' || val === true ? '✅ 开启' : '❌ 关闭'
  }
  if (key === 'fit') {
    return val === 'on' ? '✅ 开启' : '❌ 关闭'
  }
  if (key === 'ctx_size' || key === 'n_ctx_per_slot') {
    const n = parseInt(val)
    if (n >= 1024) return (n / 1024).toFixed(0) + 'K tokens'
    return String(n) + ' tokens'
  }
  if (key === 'cache_ram') return val + ' MiB'
  if (key === 'sleep_idle_seconds') return val + ' 秒'
  if (key === 'gpu') {
    if (val === 'all') return '3080+3060 (全部)'
    return val
  }
  if (key === 'model_path' || key === 'model') {
    const s = String(val)
    return s.split('/').pop() || s
  }
  if (typeof val === 'string' && val.length > 60) {
    return '...' + val.slice(-57)
  }
  return String(val)
}

function diskColor(pct: number): string {
  if (pct > 90) return '#ef4444'
  if (pct > 70) return '#f59e0b'
  return 'var(--accent-color)'
}

function serviceStatusText(status: string): string {
  if (status === 'healthy' || status === 'active') return '正常'
  if (status === 'degraded') return '降级'
  if (status === 'down' || status === 'inactive' || status === 'stopped') return '异常'
  return status || '未知'
}

function fmt(v: any): string { return v != null ? Number(v).toFixed(1) : '0' }
function fmt2(v: any): string { return v != null ? Number(v).toFixed(2) : '0' }
function gb(v: any): string { return v != null ? (Number(v) / 1073741824).toFixed(1) : '0' }
function fmtBytes(b: number): string {
  if (!b) return '-'
  if (b >= 1073741824) return (b/1073741824).toFixed(1)+' GB'
  if (b >= 1048576) return (b/1048576).toFixed(1)+' MB'
  return (b/1024).toFixed(1)+' KB'
}
</script>

<style scoped>
.panel-grid { display: flex; flex-direction: column; gap: 16px; }

/* OS Grid */
.os-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; }
@media (min-width: 768px) { .os-grid { grid-template-columns: repeat(4, 1fr); } }
.os-item { display: flex; flex-direction: column; gap: 2px; padding: 8px 12px; background: var(--bg-hover); border-radius: 6px; }
.os-label { font-size: 0.7rem; color: var(--text-muted); }
.os-val { font-size: 0.85rem; font-weight: 600; color: var(--text-primary); }

/* Metric Rows */
.metric-row { display: flex; justify-content: space-between; padding: 6px 0; border-bottom: 1px solid var(--border-color); font-size: 0.85rem; }
.metric-row:last-child { border-bottom: none; }
.metric-label { color: var(--text-secondary); }
.metric-value { font-weight: 600; color: var(--text-primary); }

/* Data Table */
.data-table { width: 100%; border-collapse: collapse; }
.data-table th, .data-table td { padding: 10px 12px; text-align: left; border-bottom: 1px solid var(--border-color); font-size: 0.9rem; }
.data-table th { color: var(--text-secondary); font-weight: 600; }
.progress-bar { width: 80px; height: 6px; background: var(--bg-hover); border-radius: 3px; position: relative; display: inline-block; vertical-align: middle; margin-right: 6px; }
.progress-fill { height: 100%; border-radius: 3px; }
.progress-text { font-size: 0.75rem; color: var(--text-secondary); }

/* Services */
.empty { text-align: center; padding: 20px; color: var(--text-muted); }
.empty-cell { text-align: center; color: var(--text-muted); padding: 18px 10px; }
.service-list { display: flex; flex-direction: column; gap: 8px; }
.service-item { background: var(--bg-hover); border-radius: 8px; overflow: hidden; border: 1px solid var(--border-color); }
.service-item.expanded { border-color: var(--accent-color); }
.service-header { display: flex; align-items: center; gap: 10px; padding: 12px 16px; cursor: pointer; transition: background 0.15s; }
.service-header:hover { background: var(--bg-active); }
.service-status-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.service-status-dot.active { background: #10b981; box-shadow: 0 0 4px #10b981; }
.service-status-dot.healthy { background: #10b981; box-shadow: 0 0 4px #10b981; }
.service-status-dot.inactive, .service-status-dot.stopped { background: #6b7280; }
.service-status-dot.unknown { background: #f59e0b; }
.service-status-dot.down { background: #ef4444; }
.service-name { font-weight: 600; font-size: 0.9rem; color: var(--text-primary); }
.service-port { font-size: 0.75rem; color: var(--text-muted); }
.service-detail-preview { font-size: 0.75rem; color: var(--text-secondary); flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.service-expand-icon { font-size: 0.7rem; color: var(--text-muted); }
.service-detail-panel { padding: 12px 16px; background: var(--bg-active); border-top: 1px solid var(--border-color); }
.detail-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 8px; margin-bottom: 8px; }
@media (min-width: 768px) { .detail-grid { grid-template-columns: repeat(3, 1fr); } }
@media (min-width: 1024px) { .detail-grid { grid-template-columns: repeat(4, 1fr); } }
.detail-item { display: flex; flex-direction: column; gap: 2px; padding: 4px 8px; background: var(--bg-hover); border-radius: 4px; }
.detail-label { font-size: 0.65rem; color: var(--text-muted); }
.detail-value { font-size: 0.8rem; font-weight: 500; color: var(--text-primary); word-break: break-all; }
.engine-badge, .version-badge { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 0.7rem; margin-right: 6px; }
.engine-badge { background: rgba(99,102,241,0.2); color: #818cf8; }
.version-badge { background: rgba(16,185,129,0.2); color: #10b981; }
.pid-info { font-size: 0.7rem; color: var(--text-muted); margin-top: 4px; }

.detail-section-title { font-size: 0.75rem; font-weight: 600; color: var(--accent-color); margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px; }
.detail-model-name { font-family: monospace; font-size: 0.8rem; color: var(--text-primary); margin-bottom: 10px; padding: 6px 10px; background: var(--bg-hover); border-radius: 4px; word-break: break-all; }
.service-detail-preview { font-size: 0.75rem; color: var(--text-secondary); flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.service-expand-icon { font-size: 0.7rem; color: var(--text-muted); margin-left: auto; }
</style>
