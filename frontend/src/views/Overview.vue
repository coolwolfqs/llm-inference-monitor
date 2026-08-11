<template>
  <div>
    <!-- 模型横幅 -->
    <div class="model-banner" v-if="overview.running_model || overview.service_running">
      <div class="banner-left">
        <div class="banner-model">
          <span class="banner-status" :class="overview.service_running ? 'running' : 'stopped'">
            {{ overview.service_running ? '\u25cf 运行中' : '\u25cb 未运行' }}
          </span>
          <span class="banner-name">{{ overview.running_model || '未部署模型' }}</span>
        </div>
        <div class="banner-meta">
          <span v-if="overview.deploy_config?.alias" class="meta-tag">{{ overview.deploy_config.alias }}</span>
          <span v-if="overview.running_engine" class="meta-tag">引擎: {{ overview.running_engine }}</span>
          <span v-if="overview.deploy_config?.ctx_size" class="meta-tag">ctx: {{ fmtCtx(overview.deploy_config.ctx_size) }}</span>
          <span v-if="overview.deploy_config?.ngl !== undefined" class="meta-tag">ngl: {{ overview.deploy_config.ngl }}</span>
          <span v-if="overview.model_quant_type" class="meta-tag">{{ overview.model_quant_type }}</span>
          <span v-for="t in (overview.model_tags || [])" :key="t" class="meta-tag tag-feature">{{ t }}</span>
        </div>
      </div>
      <div class="banner-actions">
        <button class="banner-btn" v-if="overview.service_running" @click="stopService">停止</button>
        <button class="banner-btn" v-if="overview.service_running" @click="restartService">重启</button>
        <button class="banner-btn primary" @click="goDeploy">部署</button>
      </div>
    </div>
    <div class="panel-grid">
    <div class="card card-full">
      <div class="card-header"><h3>健康评分</h3></div>
      <div class="card-body">
        <div class="health-circle" :class="healthClass">{{ dashboard.healthScore }}</div>
        <div class="health-reasons" v-if="healthReasons.length > 0">
          <div v-for="reason in healthReasons" :key="reason.item" class="reason-item">
            <span :class="['reason-badge', 'badge-' + reason.level]">{{ levelText(reason.level) }}</span>
            <span>{{ reason.item }}: {{ reason.value }} (扣 {{ reason.penalty }}分)</span>
          </div>
        </div>
      </div>
    </div>

    <div class="card card-half">
      <div class="card-header"><h3>当前模型</h3></div>
      <div class="card-body">
        <div class="metric-row"><span class="metric-label">模型</span><span class="metric-value model-name">{{ overview.running_model || '未加载' }}</span></div>
        <div class="metric-row"><span class="metric-label">引擎</span><span class="metric-value">{{ overview.active_engine || '-' }}</span></div>
        <div class="metric-row"><span class="metric-label">持久化</span><span class="metric-value">{{ overview.persist?.mode || '-' }}</span></div>
      </div>
    </div>

    <div class="card card-half">
      <div class="card-header"><h3>GPU 状态</h3></div>
      <div class="card-body">
        <div class="metric-row"><span class="metric-label">GPU 数量</span><span class="metric-value">{{ gpuCount }}</span></div>
        <div class="metric-row"><span class="metric-label">利用率</span><span class="metric-value">{{ fmt(gpuUtil) }}%</span></div>
        <div class="metric-row"><span class="metric-label">显存使用</span><span class="metric-value">{{ fmt(gpuMemPct) }}%</span></div>
        <div class="metric-row"><span class="metric-label">温度</span><span class="metric-value">{{ fmt(gpuTemp) }}°C</span></div>
        <div class="metric-row"><span class="metric-label">功耗</span><span class="metric-value">{{ fmt(gpuPower) }}W</span></div>
      </div>
    </div>

    <div class="card card-half">
      <div class="card-header"><h3>系统状态</h3></div>
      <div class="card-body">
        <div class="metric-row"><span class="metric-label">CPU 使用率</span><span class="metric-value">{{ fmt(cpuUtil) }}%</span></div>
        <div class="metric-row"><span class="metric-label">内存</span><span class="metric-value">{{ fmt(memPct) }}% ({{ fmt(memUsedGB) }}GB)</span></div>
        <div class="metric-row"><span class="metric-label">负载均值</span><span class="metric-value">{{ fmt2(load1) }}</span></div>
        <div class="metric-row"><span class="metric-label">进程数</span><span class="metric-value">{{ processCount }}</span></div>
      </div>
    </div>

    <div class="card card-half">
      <div class="card-header"><h3>推理状态</h3></div>
      <div class="card-body">
        <div class="metric-row"><span class="metric-label">生成速度</span><span class="metric-value">{{ fmt(inferenceTps) }} tokens/s</span></div>
        <div class="metric-row"><span class="metric-label">活跃槽位</span><span class="metric-value">{{ activeSlots }} / {{ totalSlots }}</span></div>
        <div class="metric-row"><span class="metric-label">KV Cache</span><span class="metric-value">{{ fmt(kvCachePct) }}%</span></div>
        <div class="metric-row"><span class="metric-label">服务状态</span><span class="metric-value" :class="overview.service_running ? 'text-green' : 'text-red'">{{ overview.service_running ? '运行中' : '已停止' }}</span></div>
      </div>
    </div>

    <div class="card card-half">
      <div class="card-header"><h3>LLM 指标</h3></div>
      <div class="card-body">
        <div class="metric-row"><span class="metric-label">TTFT</span><span class="metric-value">{{ fmt(overview.llm_metrics?.ttft_ms) }} ms</span></div>
        <div class="metric-row"><span class="metric-label">TPOT</span><span class="metric-value">{{ fmt(overview.llm_metrics?.tpot_ms) }} ms</span></div>
        <div class="metric-row"><span class="metric-label">生成速度</span><span class="metric-value">{{ fmt(overview.llm_metrics?.tokens_per_sec) }} t/s</span></div>
        <div class="metric-row"><span class="metric-label">Spec 接受率</span><span class="metric-value">{{ fmt(overview.llm_metrics?.spec_accept_rate) }}</span></div>
      </div>
    </div>

    <div class="card card-full">
      <div class="card-header"><h3>最近推理日志</h3></div>
      <div class="card-body">
        <table class="data-table">
          <thead><tr><th>时间</th><th>耗时</th><th>TPS</th><th>Tokens</th><th>Prompt</th><th>P Tokens</th><th>E Tokens</th></tr></thead>
          <tbody>
            <tr v-for="log in recentLogs" :key="log.timestamp">
              <td>{{ log.time }}</td>
              <td>{{ log.duration_ms?.toFixed(0) }}ms</td>
              <td>{{ log.tps?.toFixed(1) }}</td>
              <td>{{ log.tokens }}</td>
              <td>{{ log.prompt_ms?.toFixed(0) }}ms</td>
              <td>{{ log.prompt_tokens }}</td>
              <td>{{ log.eval_tokens }}</td>
            </tr>
            <tr v-if="recentLogs.length === 0"><td colspan="7" class="empty-cell">暂无推理日志</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useDashboardStore } from '../stores/dashboard'
import { stopModel, systemAction } from '../api'

const dashboard = useDashboardStore()
const router = useRouter()
const overview = computed(() => dashboard.overview || {})
const gpuList = computed(() => Array.isArray(overview.value.gpus) ? overview.value.gpus : [])
const gpuAggregate = computed(() => overview.value.gpu_aggregate || overview.value.gpus?.aggregate || null)
const gpuCount = computed(() => overview.value.gpu_count ?? gpuList.value.length)
const gpuUtil = computed(() => overview.value.gpu_util ?? gpuAggregate.value?.util ?? overview.value.gpu?.util)
const gpuMemPct = computed(() => overview.value.gpu_mem_pct ?? gpuAggregate.value?.mem_util_pct ?? overview.value.gpu?.mem_util_pct)
const gpuTemp = computed(() => overview.value.gpu_temp ?? gpuAggregate.value?.temp ?? overview.value.gpu?.temp)
const gpuPower = computed(() => overview.value.gpu_power ?? gpuAggregate.value?.power_draw ?? overview.value.gpu?.power_draw)
const cpuUtil = computed(() => overview.value.cpu_util ?? overview.value.cpu?.usage)
const memPct = computed(() => overview.value.mem_used_pct ?? overview.value.memory?.percent)
const memUsedGB = computed(() => overview.value.mem_used_gb ?? (overview.value.memory?.used ? overview.value.memory.used / 1073741824 : null))
const load1 = computed(() => overview.value.load_1 ?? overview.value.cpu?.load1)
const processCount = computed(() => overview.value.process_count ?? overview.value.cpu?.processes ?? 0)
const inferenceStats = computed(() => overview.value.inference_stats || {})
const inferenceTps = computed(() => overview.value.inference_tps ?? inferenceStats.value.last_tps)
const activeSlots = computed(() => overview.value.active_slots ?? inferenceStats.value.active_slots ?? 0)
const totalSlots = computed(() => overview.value.total_slots ?? inferenceStats.value.total_slots ?? 0)
const kvCachePct = computed(() => overview.value.kv_cache_pct ?? overview.value.kv_cache?.summary?.pct ?? inferenceStats.value.kv_cache_used_pct)
const recentLogs = computed(() => Array.isArray(overview.value.logs) ? overview.value.logs.slice(0, 10) : [])

const healthClass = computed(() => {
  const score = dashboard.healthScore
  if (score >= 90) return 'health-good'
  if (score >= 70) return 'health-warning'
  return 'health-critical'
})

const healthReasons = computed(() => overview.value.health_reasons || [])

function levelText(level: string): string {
  return level === 'critical' ? '严重' : level === 'warning' ? '警告' : level === 'notice' ? '提示' : level.toUpperCase()
}

function fmt(v: any): string { return v != null ? Number(v).toFixed(1) : '0' }
function fmt2(v: any): string { return v != null ? Number(v).toFixed(2) : '0' }
function fmtCtx(v: any): string {
  const n = Number(v) || 0
  if (n >= 1024) return (n / 1024).toFixed(0) + 'K'
  return n ? String(n) : '-'
}

function goDeploy() {
  router.push('/models')
}

async function restartService() {
  const ok = await dashboard.showConfirm({
    title: '重启推理服务',
    message: '确定要重启推理服务吗？当前推理任务将中断。',
    icon: '\u{1f504}',
    confirmText: '重启'
  })
  if (!ok) return
  try {
    await systemAction('restart_llama')
    dashboard.showToast('已发送重启请求', 'success')
    setTimeout(() => dashboard.fetchPanelData('overview'), 5000)
  } catch (e: any) {
    dashboard.showToast('重启失败: ' + (e.response?.data?.error || e.message), 'error')
  }
}

async function stopService() {
  const ok = await dashboard.showConfirm({
    title: '停止模型',
    message: '确定要停止当前推理模型吗？当前推理任务将中断。',
    icon: '\u23f9',
    confirmText: '停止'
  })
  if (!ok) return
  try {
    await stopModel()
    dashboard.showToast('已发送停止请求', 'success')
    setTimeout(() => dashboard.fetchPanelData('overview'), 2000)
  } catch (e: any) {
    dashboard.showToast('停止失败: ' + (e.response?.data?.error || e.message), 'error')
  }
}

onMounted(() => { dashboard.fetchPanelData('overview') })
</script>

<style scoped>
.health-circle { width: 100px; height: 100px; margin: 16px auto; border-radius: 50%; display: flex; align-items: center; justify-content: center; font-size: 1.75rem; font-weight: 700; }
@media (min-width: 768px) { .health-circle { width: 120px; height: 120px; font-size: 2rem; } }
.health-good { background: rgba(16,185,129,0.15); border: 3px solid #10b981; color: #10b981; }
.health-warning { background: rgba(245,158,11,0.15); border: 3px solid #f59e0b; color: #f59e0b; }
.health-critical { background: rgba(239,68,68,0.15); border: 3px solid #ef4444; color: #ef4444; }
.health-reasons { max-width: 100%; margin: 16px auto 0; }
.reason-item { display: flex; align-items: center; gap: 8px; padding: 6px 10px; margin-bottom: 6px; background: var(--bg-hover); border-radius: 6px; font-size: 0.8rem; flex-wrap: wrap; }
.reason-badge { padding: 2px 6px; border-radius: 4px; font-size: 0.65rem; font-weight: 700; }
.badge-critical { background: rgba(239,68,68,0.2); color: #ef4444; }
.badge-warning { background: rgba(245,158,11,0.2); color: #f59e0b; }
.badge-notice { background: rgba(99,102,241,0.2); color: #818cf8; }
.model-name { font-family: monospace; font-size: 0.8rem; }
.text-green { color: #10b981; }
.text-red { color: #ef4444; }
.data-table { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
.data-table th, .data-table td { padding: 8px 10px; text-align: left; border-bottom: 1px solid var(--border-color); }
.data-table th { color: var(--text-secondary); font-weight: 600; }
.empty-cell { text-align: center; color: var(--text-muted); padding: 18px 10px; }
@media (max-width: 768px) { .data-table { font-size: 0.7rem; } .data-table th, .data-table td { padding: 6px 8px; } }
</style>
