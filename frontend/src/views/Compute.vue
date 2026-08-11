<template>
  <div class="panel-grid">
    <!-- GPU 卡片 -->
    <div class="card card-full" v-for="gpu in gpus" :key="gpu.index">
      <div class="card-header">
        <h3>{{ gpu.name || 'GPU #' + gpu.index }}</h3>
        <span class="gpu-status" :class="{ online: gpu.util > 0 }">{{ gpu.util > 0 ? '工作中' : '空闲' }}</span>
      </div>
      <div class="card-body">
        <div class="gpu-grid">
          <div class="gpu-stat">
            <div class="stat-label">利用率</div>
            <div class="stat-value" :class="statColor(gpu.util)">{{ fmt(gpu.util) }}%</div>
            <div class="stat-bar"><div class="stat-bar-fill" :style="{ width: pct(gpu.util), background: barColor(gpu.util) }"></div></div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">显存</div>
            <div class="stat-value">{{ fmt(gpu.mem_util_pct) }}%</div>
            <div class="stat-bar"><div class="stat-bar-fill" :style="{ width: pct(gpu.mem_util_pct), background: barColor(gpu.mem_util_pct) }"></div></div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">温度</div>
            <div class="stat-value" :class="statColor(gpu.temp)">{{ fmt(gpu.temp) }}°C</div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">功耗</div>
            <div class="stat-value">{{ fmt(gpu.power_draw) }} / {{ fmt(gpu.power_limit) }} W</div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">频率</div>
            <div class="stat-value">{{ (gpu.clock || 0).toFixed(0) }} MHz</div>
          </div>
          <div class="gpu-stat">
            <div class="stat-label">风扇</div>
            <div class="stat-value">{{ fmt(gpu.fan_speed) }}%</div>
          </div>
        </div>
      </div>
    </div>

    <!-- 功率控制面板 -->
    <div class="card card-power" v-if="gpus.length > 0">
      <div class="card-header"><h3>⚡ 功率限制</h3></div>
      <div class="card-body">
        <div v-for="gpu in gpus" :key="'pl-' + gpu.index" class="power-row">
          <div class="power-info">
            <span class="power-gpu-name">{{ gpu.name || 'GPU #' + gpu.index }}</span>
            <span class="power-values">{{ fmt(gpu.power_draw) }}W / {{ fmt(gpu.power_limit) }}W ({{ pctNum(gpu.power_draw, gpu.power_limit) }})</span>
          </div>
          <div class="power-slider">
            <input type="range" :min="40" :max="100" :value="powerPcts[gpu.index] || 100"
              @input="onPowerChange(gpu.index, $event)" class="slider" />
            <button class="btn-apply" @click="applyPowerLimit(gpu)">应用</button>
          </div>
        </div>
      </div>
    </div>

    <!-- GPU 进程列表 -->
    <div class="card card-procs" v-if="gpuProcesses.length > 0">
      <div class="card-header"><h3>🔧 GPU 进程</h3><span class="proc-count">{{ gpuProcesses.length }} 个</span></div>
      <div class="card-body">
        <table class="proc-table">
          <thead><tr><th>GPU</th><th>PID</th><th>进程</th><th>显存占用</th></tr></thead>
          <tbody>
            <tr v-for="p in gpuProcesses" :key="p.pid">
              <td>GPU #{{ p.gpu_index }}</td>
              <td>{{ p.pid }}</td>
              <td>{{ p.name || 'unknown' }}</td>
              <td>{{ p.mem_used }} MB</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <div class="card card-procs" v-else>
      <div class="card-header"><h3>GPU 进程</h3></div>
      <div class="card-body empty">当前无 GPU 计算进程</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useDashboardStore } from '../stores/dashboard'
import { setGPUPowerLimit } from '../api'

const dashboard = useDashboardStore()
const gpus = computed(() => dashboard.hardware?.gpus?.gpus || [])
const gpuProcesses = ref<any[]>([])

const powerPcts = ref<Record<number, number>>({})

function onPowerChange(idx: number, event: Event) {
  const target = event.target as HTMLInputElement
  powerPcts.value[idx] = parseInt(target.value)
}

async function applyPowerLimit(gpu: any) {
  const gpuIndex = gpu.index
  const pct = powerPcts.value[gpuIndex] || 100
  const watts = Math.round((Number(gpu.power_limit) || pct) * pct / 100)
  try {
    await setGPUPowerLimit(gpuIndex, watts)
    // Show toast
    dashboard.showToast(`GPU #${gpuIndex} 功率已设置为 ${pct}% (${watts}W)`, 'success')
  } catch (e: any) {
    dashboard.showToast('设置失败: ' + e.message, 'error')
  }
}

function pct(v: any): string {
  const n = Number(v) || 0
  return Math.min(100, Math.max(0, n)) + '%'
}

function pctNum(val: any, total: any): string {
  const v = Number(val) || 0
  const t = Number(total) || 1
  return Math.round(v / t * 100) + '%'
}

function fmt(v: any): string {
  return v != null ? Number(v).toFixed(1) : '0'
}

function statColor(v: any): string {
  const n = Number(v) || 0
  if (n > 85) return 'danger'
  if (n > 60) return 'warning'
  return ''
}

function barColor(v: any): string {
  const n = Number(v) || 0
  if (n > 85) return '#ef4444'
  if (n > 60) return '#f59e0b'
  return '#3fb950'
}

onMounted(() => {
  gpus.value.forEach((g: any) => {
    powerPcts.value[g.index] = 100
  })
  // Load GPU processes
  loadGPUProcs()
})

async function loadGPUProcs() {
  try {
    const res = await fetch('/api/compute/procs')
    if (res.ok) {
      const data = await res.json()
      gpuProcesses.value = data.procs || []
    }
  } catch (e) { /* no procs endpoint yet */ }
}
</script>

<style scoped>
.panel-grid { display: flex; flex-direction: column; gap: 16px; }
.card-header { display: flex; justify-content: space-between; align-items: center; }
.gpu-status { font-size: 0.75rem; padding: 2px 8px; border-radius: 4px; }
.gpu-status.online { background: rgba(16,185,129,0.2); color: #10b981; }
.gpu-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; }
@media (min-width: 768px) { .gpu-grid { grid-template-columns: repeat(3, 1fr); gap: 16px; } }
@media (min-width: 1024px) { .gpu-grid { grid-template-columns: repeat(6, 1fr); } }
.gpu-stat { background: var(--bg-hover); padding: 12px; border-radius: 6px; text-align: center; }
.stat-label { color: var(--text-secondary); font-size: 0.75rem; margin-bottom: 6px; }
.stat-value { font-size: 1rem; font-weight: 700; }
.stat-value.warning { color: #f59e0b; }
.stat-value.danger { color: #ef4444; }
.stat-bar { height: 4px; background: var(--bg-active); border-radius: 2px; margin-top: 6px; overflow: hidden; }
.stat-bar-fill { height: 100%; border-radius: 2px; transition: width 0.3s ease; }

/* Power Control */
.card-power { background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 10px; }
.card-power .card-body { padding: 12px 16px; }
.power-row { display: flex; flex-direction: column; gap: 8px; padding: 10px 0; border-bottom: 1px solid var(--border-color); }
.power-row:last-child { border-bottom: none; }
@media (min-width: 768px) { .power-row { flex-direction: row; align-items: center; justify-content: space-between; } }
.power-info { display: flex; flex-direction: column; gap: 2px; }
.power-gpu-name { font-weight: 600; font-size: 0.85rem; color: var(--text-primary); }
.power-values { font-size: 0.75rem; color: var(--text-secondary); }
.power-slider { display: flex; align-items: center; gap: 10px; width: 100%; }
@media (min-width: 768px) { .power-slider { width: 50%; } }
.slider { flex: 1; height: 6px; -webkit-appearance: none; appearance: none; background: var(--bg-active); border-radius: 3px; outline: none; }
.slider::-webkit-slider-thumb { -webkit-appearance: none; appearance: none; width: 18px; height: 18px; border-radius: 50%; background: var(--accent-color); cursor: pointer; }
.btn-apply { padding: 4px 12px; background: var(--accent-color); color: white; border: none; border-radius: 4px; font-size: 0.75rem; cursor: pointer; white-space: nowrap; }
.btn-apply:hover { opacity: 0.85; }

/* GPU Processes */
.card-procs { background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 10px; }
.proc-count { font-size: 0.75rem; color: var(--text-secondary); }
.proc-table { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
.proc-table th { text-align: left; padding: 8px 10px; color: var(--text-secondary); border-bottom: 1px solid var(--border-color); font-weight: 500; }
.proc-table td { padding: 6px 10px; color: var(--text-primary); border-bottom: 1px solid var(--border-color); }
.proc-table tr:last-child td { border-bottom: none; }
</style>
