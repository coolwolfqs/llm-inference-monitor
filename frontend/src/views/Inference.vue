<template>
  <div class="panel-grid">
    <div class="card card-half">
      <div class="card-header"><h3>推理统计</h3></div>
      <div class="card-body">
        <div class="metric-row"><span class="metric-label">活跃槽位</span><span class="metric-value">{{ inf?.active_slots || 0 }} / {{ inf?.total_slots || 0 }}</span></div>
        <div class="metric-row"><span class="metric-label">生成速度</span><span class="metric-value">{{ fmt(inf?.last_tps) }} t/s</span></div>
        <div class="metric-row"><span class="metric-label">延迟</span><span class="metric-value">{{ fmt(inf?.last_latency_ms) }} ms</span></div>
        <div class="metric-row"><span class="metric-label">KV Cache 使用</span><span class="metric-value" :style="{color: kvPctColor}">{{ fmt(kv?.summary?.pct) }}%</span></div>
        <div class="metric-row"><span class="metric-label">KV Cache Token数</span><span class="metric-value">{{ (kv?.summary?.kv_tokens || 0).toLocaleString() }}</span></div>
      </div>
    </div>

    <div class="card card-half">
      <div class="card-header"><h3>LLM 可观测性</h3></div>
      <div class="card-body">
        <div class="metric-row"><span class="metric-label">首字延迟 (TTFT)</span><span class="metric-value">{{ fmt(l?.ttft_ms) }} ms</span></div>
        <div class="metric-row"><span class="metric-label">逐字延迟 (TPOT)</span><span class="metric-value">{{ fmt(l?.tpot_ms) }} ms</span></div>
        <div class="metric-row"><span class="metric-label">TTFT P95</span><span class="metric-value">{{ fmt(l?.ttft_p95) }} ms</span></div>
        <div class="metric-row"><span class="metric-label">推测解码接受率</span><span class="metric-value">{{ fmt(l?.spec_accept_rate) }}</span></div>
      </div>
    </div>

    <!-- KV Cache Summary Panel -->
    <div class="card card-full" v-if="kv?.captured">
      <div class="card-header">
        <h3>🧠 KV Cache 显存占用</h3>
        <span class="kv-badge" :class="kvLevelClass">{{ kvLevelText }}</span>
      </div>
      <div class="card-body">
        <div class="kv-summary-grid">
          <div class="kv-summary-item">
            <span class="kv-label">计算来源</span>
            <span class="kv-value">{{ sourceLabel(kv?.summary?.source) }} / {{ confidenceLabel(kv?.summary?.confidence) }}</span>
          </div>
          <div class="kv-summary-item">
            <span class="kv-label">KV 缓存总量</span>
            <span class="kv-value">{{ kvSizeFmt(kv?.summary?.kv_total_mb) }}</span>
          </div>
          <div class="kv-summary-item">
            <span class="kv-label">KV 已用</span>
            <span class="kv-value">{{ kvSizeFmt(kv?.summary?.kv_used_mb) }}</span>
          </div>
          <div class="kv-summary-item">
            <span class="kv-label">物理显存剩余</span>
            <span class="kv-value">{{ kvSizeFmt(kv?.summary?.phys_free_mb) }}</span>
          </div>
          <div class="kv-summary-item">
            <span class="kv-label">Token 使用</span>
            <span class="kv-value">{{ (kv?.summary?.kv_tokens || 0).toLocaleString() }} / {{ (kv?.summary?.kv_total_tokens || 0).toLocaleString() }} ({{ fmt(kv?.summary?.tokens_pct) }}%)</span>
          </div>
          <div class="kv-summary-item">
            <span class="kv-label">模型权重</span>
            <span class="kv-value">{{ kvSizeFmt(kv?.summary?.model_weight_mb) }}</span>
          </div>
          <div class="kv-summary-item">
            <span class="kv-label">物理校验差异</span>
            <span class="kv-value">{{ kvSizeFmt(kv?.summary?.verify_delta_mb) }}</span>
          </div>
        </div>
        <div class="kv-formula-note" v-if="kv?.summary?.formula_ok">
          模型: {{ modelName(kv?.summary?.model) }} | 缓存类型: {{ kv?.summary?.cache_type }} | Context: {{ kv?.summary?.ctx_size_used }} | 每Token: {{ fmt(kv?.summary?.kv_per_token_bytes) }} bytes | Slots: {{ kv?.summary?.slots_observed || 0 }}
        </div>
        <div class="kv-formula-note" v-else>
          理论公式计算不可用，使用物理显存差值法
        </div>
      </div>
    </div>

    <!-- Per-GPU KV Cache Cards -->
    <div class="card card-full" v-if="kv?.cards && kv.cards.length > 0">
      <div class="card-header"><h3>📊 逐卡 KV Cache</h3></div>
      <div class="card-body">
        <div class="kv-cards-grid">
          <div class="kv-card" v-for="card in kv.cards" :key="card.gpu_index">
            <div class="kv-card-name">{{ card.name || 'GPU ' + card.gpu_index }}</div>
            <div class="kv-card-row">
              <span class="kv-card-label">KV 容量</span>
              <span class="kv-card-value">{{ kvSizeFmt(card.kv_total_mb) }}</span>
            </div>
            <div class="kv-card-row">
              <span class="kv-card-label">KV 已用</span>
              <span class="kv-card-value">{{ kvSizeFmt(card.kv_used_mb) }}</span>
            </div>
            <div class="kv-card-row">
              <span class="kv-card-label">物理剩余</span>
              <span class="kv-card-phys">{{ kvSizeFmt(card.mem_free_mb) }}</span>
            </div>
            <div class="kv-card-row">
              <span class="kv-card-label">来源/置信</span>
              <span class="kv-card-phys">{{ sourceLabel(card.source) }} / {{ confidenceLabel(card.confidence) }}</span>
            </div>
            <div class="kv-card-pct" :style="{color: pctColor(card.pct)}">{{ fmt(card.pct) }}%</div>
            <div class="kv-card-bar">
              <div class="kv-card-bar-fill" :style="{width: Math.min(card.pct, 100) + '%', background: pctBg(card.pct)}"></div>
            </div>
            <div class="kv-card-split" v-if="card.tensor_split_pct">Tensor Split: {{ card.tensor_split_pct }}%</div>
          </div>
        </div>
      </div>
    </div>

    <div class="card card-full">
      <div class="card-header"><h3>生成速度趋势 (1小时)</h3></div>
      <div class="card-body"><div ref="tpsChart" class="chart-container"></div></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useDashboardStore } from '../stores/dashboard'
import { queryMetricsRange } from '../api'
import * as echarts from 'echarts'

const dashboard = useDashboardStore()
const tpsChart = ref<HTMLElement | null>(null)
const inf = computed(() => dashboard.inference?.inference || {})
const l = computed(() => dashboard.inference?.llm || {})
const kv = computed(() => dashboard.kvCache || {})

function fmt(v: any): string { return v != null ? Number(v).toFixed(1) : '0' }

function kvSizeFmt(mb: any): string {
  if (mb == null) return '--'
  const v = Number(mb)
  return v >= 1024 ? (v / 1024).toFixed(2) + ' GB' : v.toFixed(0) + ' MB'
}

function modelName(path: any): string {
  const s = String(path || '-')
  return s.split('/').pop() || s
}

function sourceLabel(source: any): string {
  const s = String(source || '')
  if (s === 'theory+slots') return '公式+Slots'
  if (s === 'physical-baseline') return '物理基线'
  if (s === 'physical-vram') return '物理显存'
  if (s === 'theory') return '公式估算'
  return s || '-'
}

function confidenceLabel(conf: any): string {
  const c = String(conf || '')
  if (c === 'high') return '高'
  if (c === 'medium') return '中'
  if (c === 'low') return '低'
  return c || '-'
}

function pctColor(pct: number): string {
  if (pct > 90) return '#ef4444'
  if (pct > 70) return '#f59e0b'
  return '#10b981'
}

function pctBg(pct: number): string {
  if (pct > 90) return 'rgba(239,68,68,0.7)'
  if (pct > 70) return 'rgba(245,158,11,0.7)'
  return 'rgba(16,185,129,0.7)'
}

const kvPctColor = computed(() => pctColor(kv.value?.summary?.pct || 0))

const kvLevelClass = computed(() => {
  const level = kv.value?.summary?.worst_level || 'unknown'
  return level === 'critical' ? 'badge-critical' : level === 'warning' ? 'badge-warning' : 'badge-healthy'
})

const kvLevelText = computed(() => {
  const level = kv.value?.summary?.worst_level || 'unknown'
  return level === 'critical' ? '⚠️ 临界' : level === 'warning' ? '⚡ 警告' : '✓ 健康'
})

let chart: any = null
async function loadChart() {
  const end = Date.now(), start = end - 3600000
  try {
    const res = await queryMetricsRange('inference_tps', String(Math.floor(start/1000)), String(Math.floor(end/1000)), '60')
    if (!tpsChart.value) return
    if (!chart) chart = echarts.init(tpsChart.value, 'dark')
    const series = (res.data.result || []).map((item: any) => ({ name: '生成速度', type: 'line', data: (item.values || []).map((v: any) => ({ value: [v[0]*1000, v[1]] })), smooth: true, showSymbol: false }))
    chart.setOption({ grid: { top: 30, right: 20, bottom: 30, left: 50 }, xAxis: { type: 'time', axisLabel: { formatter: '{HH}:{mm}' } }, yAxis: { type: 'value', name: 'tokens/s' }, series, tooltip: { trigger: 'axis', formatter: '{b}<br/>{a}: {c}' } })
  } catch (e) { console.error(e) }
}
onMounted(() => { loadChart(); setInterval(loadChart, 60000) })
</script>

<style scoped>
.metric-value { font-variant-numeric: tabular-nums; }
.kv-summary-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; margin-bottom: 12px; }
.kv-summary-item { display: flex; flex-direction: column; padding: 8px 12px; background: var(--bg-hover); border-radius: 8px; }
.kv-label { font-size: 0.7rem; color: var(--text-secondary); margin-bottom: 4px; }
.kv-value { font-size: 0.95rem; font-weight: 600; color: var(--text-primary); font-variant-numeric: tabular-nums; }
.kv-formula-note { font-size: 0.7rem; color: var(--text-muted); text-align: center; padding: 4px 8px; background: var(--bg-tertiary); border-radius: 4px; }
.kv-badge { padding: 3px 10px; border-radius: 12px; font-size: 0.75rem; font-weight: 600; }
.badge-critical { background: rgba(239,68,68,0.15); color: #ef4444; }
.badge-warning { background: rgba(245,158,11,0.15); color: #f59e0b; }
.badge-healthy { background: rgba(16,185,129,0.15); color: #10b981; }
.kv-cards-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 16px; }
.kv-card { background: var(--bg-tertiary); border: 1px solid var(--border-color); border-radius: 10px; padding: 14px; }
.kv-card-name { font-size: 0.85rem; font-weight: 600; color: var(--text-primary); margin-bottom: 8px; }
.kv-card-row { display: flex; justify-content: space-between; font-size: 0.75rem; margin-bottom: 4px; }
.kv-card-label { color: var(--text-secondary); }
.kv-card-value { color: var(--text-primary); font-weight: 500; }
.kv-card-phys { color: var(--accent-color); font-weight: 500; }
.kv-card-pct { font-size: 0.85rem; font-weight: 700; text-align: right; margin-bottom: 4px; }
.kv-card-bar { height: 4px; background: var(--bg-hover); border-radius: 2px; overflow: hidden; margin-bottom: 6px; }
.kv-card-bar-fill { height: 100%; border-radius: 2px; transition: width 0.5s; }
.kv-card-split { font-size: 0.65rem; color: var(--text-muted); text-align: center; }
@media (max-width: 768px) {
  .kv-summary-grid { grid-template-columns: repeat(2, 1fr); }
  .kv-cards-grid { grid-template-columns: 1fr; }
}
</style>
