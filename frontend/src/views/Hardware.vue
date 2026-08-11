<template>
  <div class="panel-grid">
    <div class="card card-full" v-for="gpu in gpus" :key="gpu.index">
      <div class="card-header">
        <h3>{{ gpu.name || 'GPU #' + gpu.index }}</h3>
        <span class="gpu-index">GPU {{ gpu.index }}</span>
      </div>
      <div class="card-body">
        <div class="gpu-grid">
          <div class="gpu-stat">
            <div class="stat-label">利用率</div>
            <div class="stat-value" :class="utilClass(gpu.util)">{{ fmt(gpu.util) }}%</div>
            <div class="stat-bar"><div class="stat-fill" :style="{ width: gpu.util + '%', background: utilColor(gpu.util) }"></div></div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">显存使用</div>
            <div class="stat-value">{{ fmt(gpu.mem_util_pct) }}%</div>
            <div class="stat-bar"><div class="stat-fill" :style="{ width: gpu.mem_util_pct + '%' }"></div></div>
            <div class="stat-detail">{{ fmtSize(gpu.mem_used) }} / {{ fmtSize(gpu.mem_total) }}</div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">温度</div>
            <div class="stat-value" :class="tempClass(gpu.temp)">{{ fmt(gpu.temp) }}°C</div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">功耗</div>
            <div class="stat-value">{{ fmt(gpu.power_draw) }}W</div>
            <div class="stat-detail">{{ fmt(gpu.power_pct) }}% / {{ fmt(gpu.power_limit) }}W</div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">频率</div>
            <div class="stat-value">{{ (gpu.clock || 0).toFixed(0) }} MHz</div>
            <div class="stat-detail">最大 {{ (gpu.clock_max || 0).toFixed(0) }} MHz</div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">风扇</div>
            <div class="stat-value">{{ fmt(gpu.fan_speed) }}%</div>
          </div>
        </div>

        <div class="gpu-details">
          <div class="detail-section">
            <h4>PCIe 信息</h4>
            <div class="detail-row"><span>世代:</span><span>{{ gpu.pcie?.gen || '-' }} (当前 {{ gpu.pcie?.current_gen || '-' }})</span></div>
            <div class="detail-row"><span>宽度:</span><span>{{ gpu.pcie?.width || '-' }} (当前 {{ gpu.pcie?.current_width || '-' }})</span></div>
          </div>
          <div class="detail-section">
            <h4>驱动信息</h4>
            <div class="detail-row"><span>驱动版本:</span><span>{{ gpu.driver || '-' }}</span></div>
            <div class="detail-row"><span>架构:</span><span>{{ gpu.arch || '-' }}</span></div>
            <div class="detail-row"><span>CUDA 核心:</span><span>{{ gpu.cuda_cores || '-' }}</span></div>
          </div>
          <div class="detail-section">
            <h4>进程</h4>
            <div v-if="gpu.processes && gpu.processes.length" class="process-list">
              <div v-for="p in gpu.processes" :key="p.pid" class="process-item">
                <span>PID: {{ p.pid }}</span>
                <span>显存: {{ fmtSize(p.mem_used) }}</span>
              </div>
            </div>
            <div v-else class="no-process">无 GPU 进程</div>
          </div>
        </div>

        <div class="gpu-chart-section">
          <h4>利用率趋势 (1小时)</h4>
          <div :ref="el => setChartRef(gpu.index, el)" class="chart-container"></div>
        </div>
      </div>
    </div>

    <div class="card card-full">
      <div class="card-header"><h3>GPU 聚合信息</h3></div>
      <div class="card-body">
        <div class="aggregate-grid">
          <div class="agg-item"><div class="agg-label">总显存</div><div class="agg-value">{{ fmtSize(aggregate.mem_used) }} / {{ fmtSize(aggregate.mem_total) }}</div></div>
          <div class="agg-item"><div class="agg-label">平均温度</div><div class="agg-value">{{ fmt(aggregate.temp) }}°C</div></div>
          <div class="agg-item"><div class="agg-label">总功耗</div><div class="agg-value">{{ fmt(aggregate.power_draw) }}W</div></div>
          <div class="agg-item"><div class="agg-label">平均频率</div><div class="agg-value">{{ (aggregate.clock || 0).toFixed(0) }} MHz</div></div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from 'vue'
import { useDashboardStore } from '../stores/dashboard'
import { queryMetricsRange } from '../api'
import * as echarts from 'echarts'

const dashboard = useDashboardStore()
const gpus = computed(() => dashboard.hardware?.gpus?.gpus || [])
const aggregate = computed(() => dashboard.hardware?.gpus?.aggregate || {})

function fmt(v: any): string { return v != null ? Number(v).toFixed(1) : '0' }
function fmtSize(mb: number): string {
  if (!mb) return '0 MB'
  if (mb >= 1024) return (mb / 1024).toFixed(1) + ' GB'
  return mb.toFixed(0) + ' MB'
}
function utilClass(v: number): string { return v > 80 ? 'val-high' : v > 50 ? 'val-mid' : '' }
function utilColor(v: number): string { return v > 80 ? '#ef4444' : v > 50 ? '#f59e0b' : '#10b981' }
function tempClass(v: number): string { return v > 80 ? 'val-high' : v > 65 ? 'val-mid' : '' }

const chartRefs: Record<number, HTMLElement | null> = {}
const charts: Record<number, echarts.ECharts | null> = {}
function setChartRef(idx: number, el: any) { if (el) chartRefs[idx] = el }

async function loadCharts() {
  const end = Date.now(), start = end - 3600000
  for (const gpu of gpus.value) {
    const el = chartRefs[gpu.index]
    if (!el) continue
    if (!charts[gpu.index]) charts[gpu.index] = echarts.init(el, 'dark')
    try {
      const res = await queryMetricsRange('gpu_util{gpu_id="' + gpu.index + '"}', String(Math.floor(start/1000)), String(Math.floor(end/1000)), '60')
      const data = (res.data.result || [])[0]?.values?.map((v: any) => ({ value: [v[0]*1000, v[1]] })) || []
      charts[gpu.index].setOption({
        grid: { top: 10, right: 10, bottom: 20, left: 40 },
        xAxis: { type: 'time', axisLabel: { formatter: '{HH}:{mm}' } },
        yAxis: { type: 'value', max: 100, name: '%' },
        series: [{ type: 'line', data, smooth: true, showSymbol: false, areaStyle: { opacity: 0.2 }, lineStyle: { width: 2 } }],
        tooltip: { trigger: 'axis' }
      })
    } catch (e) { console.error(e) }
  }
}

onMounted(() => { nextTick(() => loadCharts()); setInterval(loadCharts, 60000) })
</script>

<style scoped>
.gpu-index { color: var(--text-secondary); font-size: 0.75rem; }
.gpu-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; margin-bottom: 16px; }
@media (min-width: 768px) { .gpu-grid { grid-template-columns: repeat(3, 1fr); gap: 16px; } }
@media (min-width: 1024px) { .gpu-grid { grid-template-columns: repeat(6, 1fr); } }
.gpu-stat { background: var(--bg-hover); padding: 12px; border-radius: 8px; text-align: center; }
.stat-label { color: var(--text-secondary); font-size: 0.75rem; margin-bottom: 6px; }
.stat-value { font-size: 1.1rem; font-weight: 700; }
.stat-detail { font-size: 0.7rem; color: var(--text-secondary); margin-top: 4px; }
.stat-bar { width: 100%; height: 4px; background: var(--bg-active); border-radius: 2px; margin-top: 8px; overflow: hidden; }
.stat-fill { height: 100%; background: var(--accent-color); border-radius: 2px; transition: width 0.5s; }
.val-high { color: #ef4444; }
.val-mid { color: #f59e0b; }
.gpu-details { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; margin-bottom: 16px; }
.detail-section h4 { font-size: 0.85rem; color: var(--text-secondary); margin-bottom: 8px; }
.detail-row { display: flex; justify-content: space-between; padding: 4px 0; font-size: 0.8rem; border-bottom: 1px solid var(--border-color); }
.detail-row span:first-child { color: var(--text-secondary); }
.process-list { display: flex; flex-direction: column; gap: 4px; }
.process-item { display: flex; justify-content: space-between; padding: 4px 8px; background: var(--bg-active); border-radius: 4px; font-size: 0.75rem; }
.no-process { color: var(--text-muted); font-size: 0.8rem; padding: 8px 0; }
.gpu-chart-section { margin-top: 16px; }
.gpu-chart-section h4 { font-size: 0.85rem; color: var(--text-secondary); margin-bottom: 8px; }
.chart-container { width: 100%; height: 150px; }
.aggregate-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 12px; }
@media (min-width: 768px) { .aggregate-grid { grid-template-columns: repeat(4, 1fr); } }
.agg-item { text-align: center; padding: 12px; background: var(--bg-hover); border-radius: 8px; }
.agg-label { color: var(--text-secondary); font-size: 0.75rem; margin-bottom: 4px; }
.agg-value { font-size: 1rem; font-weight: 700; }
</style>
