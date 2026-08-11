<template>
  <div class="panel-grid">
    <!-- 引擎列表 -->
    <div class="card card-full">
      <div class="card-header">
        <h3>🔧 推理引擎</h3>
        <span class="engine-count">{{ engines.length }} 个</span>
      </div>
      <div class="card-body">
        <div v-if="loading" class="loading">加载中...</div>
        <div v-else-if="engines.length === 0" class="empty">暂无可用引擎</div>
        <div v-else class="engine-grid">
          <div v-for="eng in engines" :key="eng.key" class="engine-card" :class="{ active: eng.is_running }">
            <!-- 电路背景装饰 -->
            <svg class="circuit-bg" viewBox="0 0 400 200" preserveAspectRatio="none">
              <path d="M0,30 H80 L100,50 H180 L200,30 H280 L300,80 H400" stroke="var(--accent-color)" stroke-width="0.5" fill="none" stroke-dasharray="4 6" opacity="0.3"/>
              <path d="M0,120 H120 L140,100 H220 L240,130 H340 L360,110 H400" stroke="var(--purple, #8b5cf6)" stroke-width="0.4" fill="none" stroke-dasharray="3 8" opacity="0.2"/>
              <circle cx="180" cy="30" r="2" fill="var(--accent-color)" opacity="0.3"/>
              <circle cx="300" cy="80" r="2" fill="var(--purple, #8b5cf6)" opacity="0.3"/>
            </svg>

            <div class="engine-header">
              <div class="engine-name-group">
                <span class="engine-dot" :class="{ active: eng.is_running }"></span>
                <span class="engine-name">{{ eng.name || eng.key }}</span>
                <span class="engine-badge" :class="eng.type === 'vllm' ? 'vllm' : 'llama'">{{ eng.type || 'llama' }}</span>
                <span class="engine-status" :class="{ active: eng.is_running }">
                  {{ eng.is_running ? '● 运行中' : '○ 已停止' }}
                </span>
              </div>
            </div>

            <div class="engine-version">
              <span v-if="eng.version">版本 <b>{{ eng.version }}</b></span>
              <span v-if="eng.commit">提交 <b>{{ eng.commit }}</b></span>
              <span v-if="eng.branch">分支 <b>{{ eng.branch }}</b></span>
            </div>

            <div class="engine-details">
              <div class="detail-row">
                <span class="detail-label">二进制路径</span>
                <span class="detail-value">{{ eng.binary_path || '-' }}</span>
              </div>
              <div v-if="eng.features" class="detail-tags">
                <span v-for="f in eng.features" :key="f" class="feat-tag">{{ f }}</span>
              </div>
            </div>

            <div class="engine-actions" v-if="!eng.is_running">
              <button class="btn-switch" @click="switchEngine(eng.key)">切换到此引擎</button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 当前引擎信息 -->
    <div class="card card-current" v-if="activeEngine">
      <div class="card-header"><h3>📌 当前活跃引擎</h3></div>
      <div class="card-body">
        <div class="current-info">
          <div class="info-item"><span class="label">引擎</span><span class="value">{{ activeEngine.name }}</span></div>
          <div class="info-item"><span class="label">版本</span><span class="value">{{ activeEngine.version || '-' }}</span></div>
          <div class="info-item"><span class="label">类型</span><span class="value">{{ activeEngine.type || 'llama' }}</span></div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useDashboardStore } from '../stores/dashboard'
import { switchEngine as apiSwitchEngine, getEngines, getActiveEngine } from '../api'

const dashboard = useDashboardStore()
const engines = ref<any[]>([])
const activeEngine = ref<any>(null)
const loading = ref(true)

async function loadEngines() {
  try {
    const res = await getEngines()
    engines.value = (res.data.engines || []).filter((e: any) => e.key !== 'vllm' || true)
  } catch (e) { console.error('加载引擎失败:', e) }
  try {
    const res = await getActiveEngine()
    const activeKey = res.data.active
    activeEngine.value = res.data.engine || engines.value.find((e: any) => e.key === activeKey) || res.data
  } catch (e) { /* ignore */ }
  loading.value = false
}

async function switchEngine(key: string) {
  const ok = await dashboard.showConfirm({
    title: '切换引擎',
    message: `确定切换到引擎 ${key} 吗？推理服务将重启。`,
    icon: '🔧',
    confirmText: '切换'
  })
  if (!ok) return
  try {
    await apiSwitchEngine(key)
    dashboard.showToast('引擎已切换', 'success')
    setTimeout(() => loadEngines(), 5000)
  } catch (e: any) {
    dashboard.showToast('切换失败: ' + e.message, 'error')
  }
}

onMounted(() => loadEngines())
</script>

<style scoped>
.panel-grid { display: flex; flex-direction: column; gap: 16px; }
.card-header { display: flex; justify-content: space-between; align-items: center; }
.engine-count { font-size: 0.8rem; color: var(--text-secondary); }
.loading, .empty { text-align: center; padding: 40px; color: var(--text-muted); }
.engine-grid { display: grid; grid-template-columns: 1fr; gap: 12px; }
@media (min-width: 768px) { .engine-grid { grid-template-columns: repeat(2, 1fr); } }

.engine-card { position: relative; background: var(--bg-hover); border-radius: 10px; padding: 16px; overflow: hidden; border: 1px solid var(--border-color); transition: all 0.2s; }
.engine-card.active { border-color: var(--success-color); background: rgba(16,185,129,0.05); }
.engine-card:hover { border-color: var(--accent-color); }
.circuit-bg { position: absolute; top: 0; left: 0; width: 100%; height: 100%; pointer-events: none; z-index: 0; }

.engine-header { position: relative; z-index: 1; margin-bottom: 8px; }
.engine-name-group { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.engine-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-muted); }
.engine-dot.active { background: var(--success-color); box-shadow: 0 0 6px var(--success-color); }
.engine-name { font-weight: 700; font-size: 0.95rem; color: var(--text-primary); }
.engine-badge { padding: 1px 8px; border-radius: 4px; font-size: 0.65rem; font-weight: 600; }
.engine-badge.llama { background: rgba(99,102,241,0.2); color: #818cf8; }
.engine-badge.vllm { background: rgba(236,72,153,0.2); color: #ec4899; }
.engine-status { font-size: 0.75rem; color: var(--text-muted); }
.engine-status.active { color: var(--success-color); }

.engine-version { position: relative; z-index: 1; display: flex; gap: 12px; font-size: 0.72rem; color: var(--text-secondary); margin-bottom: 10px; }
.engine-version b { color: var(--text-primary); font-weight: 500; }

.engine-details { position: relative; z-index: 1; margin-top: 10px; }
.detail-row { display: flex; justify-content: space-between; font-size: 0.75rem; padding: 4px 0; }
.detail-label { color: var(--text-muted); }
.detail-value { color: var(--text-secondary); font-family: monospace; max-width: 70%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.detail-tags { display: flex; gap: 4px; flex-wrap: wrap; margin-top: 6px; }
.feat-tag { font-size: 0.65rem; padding: 1px 6px; background: var(--bg-active); border-radius: 3px; color: var(--text-muted); }

.engine-actions { position: relative; z-index: 1; margin-top: 12px; }
.btn-switch { width: 100%; padding: 8px; background: var(--accent-color); color: white; border: none; border-radius: 6px; font-size: 0.85rem; cursor: pointer; font-weight: 500; }
.btn-switch:hover { opacity: 0.85; }

.card-current { background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 10px; }
.current-info { display: flex; gap: 24px; flex-wrap: wrap; }
.info-item { display: flex; flex-direction: column; gap: 2px; }
.info-item .label { font-size: 0.7rem; color: var(--text-muted); }
.info-item .value { font-size: 0.9rem; font-weight: 600; color: var(--text-primary); }
</style>
