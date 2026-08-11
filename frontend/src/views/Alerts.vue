<template>
  <div class="panel-grid">
    <div class="card card-full">
      <div class="card-header"><h3>告警规则</h3></div>
      <div class="card-body">
        <button @click="testAlerts" class="test-btn">测试告警</button>
        <table class="data-table">
          <thead><tr><th>规则名称</th><th>监控指标</th><th>条件</th><th>阈值</th><th>持续时间</th><th>级别</th><th>通知渠道</th><th>状态</th></tr></thead>
          <tbody>
            <tr v-for="(rule, name) in rules" :key="name">
              <td>{{ name }}</td><td>{{ rule.Metric }}</td><td>{{ rule.Condition }}</td><td>{{ rule.Threshold }}</td><td>{{ rule.DurationSec }}秒</td>
              <td><span :class="['status-badge', 'badge-' + rule.Severity]">{{ rule.Severity === 'critical' ? '严重' : '警告' }}</span></td>
              <td>{{ (rule.Channels || []).join(', ') }}</td><td>{{ rule.Enabled ? '启用' : '禁用' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { fetchAlertStatus, testAlert } from '../api'
const rules = ref<Record<string, any>>({})
async function loadAlerts() {
  try { const res = await fetchAlertStatus(); rules.value = res.data.rules || {} } catch (e) { console.error(e) }
}
async function testAlerts() {
  try { await testAlert(); alert('测试告警已发送!') } catch (e) { console.error(e) }
}
onMounted(() => { loadAlerts() })
</script>

<style scoped>
.test-btn { margin-bottom: 12px; padding: 8px 16px; background: var(--accent-color); color: white; border: none; border-radius: 6px; cursor: pointer; font-weight: 600; font-size: 0.85rem; }
.data-table { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
.data-table th, .data-table td { padding: 8px 10px; text-align: left; border-bottom: 1px solid var(--border-color); }
.data-table th { color: var(--text-secondary); font-weight: 600; }
.status-badge { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 0.75rem; font-weight: 600; }
.badge-critical { background: rgba(239,68,68,0.2); color: #ef4444; }
.badge-warning { background: rgba(245,158,11,0.2); color: #f59e0b; }
</style>
