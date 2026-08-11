import axios from 'axios'

const api = axios.create({ baseURL: '/api', timeout: 10000 })

// ── 基础设施 ──
export function fetchHealth()          { return api.get('/health') }
export function createSSEStream()      { return new EventSource('/api/sse') }
export function createDeployStream()   { return new EventSource('/api/stream/deploy') }

// ── 模型管理 ──
export function getModels()            { return api.get('/models') }
export function deployModel(data: any) { return api.post('/models/deploy', data) }
export function switchModel(data: any) { return api.post('/models/switch', data) }
export function stopModel()            { return api.post('/models/stop') }

// ── 引擎管理 ──
export function getEngines()            { return api.get('/engines') }
export function getActiveEngine()       { return api.get('/engines/active') }
export function switchEngine(engine: string) { return api.post('/engines/switch', { engine }) }

// ── 配置管理 ──
export function getPersistMode()           { return api.get('/settings/persist') }
export function setPersistMode(data: any)  { return api.post('/settings/persist', data) }
export function getLlamaParams()           { return api.get('/settings/params') }
export function setLlamaParams(data: any)  { return api.post('/settings/params', data) }

// ── 快速切换 ──
export function getQuickSwitch()           { return api.get('/quick-switch') }
export function updateQuickSwitch(d: any)  { return api.post('/quick-switch', d) }
export function addRecent(d: any)          { return api.post('/quick-switch/add-recent', d) }
export function toggleFav(d: any)          { return api.post('/quick-switch/toggle-fav', d) }

// ── GPU 管理 ──
export function getGPUInfo()                          { return api.get('/gpu/info') }
export function setGPUPowerLimit(gpuId: number, watts: number) {
  return api.post('/gpu/power_limit', { gpu_id: gpuId, limit_watts: watts })
}

// ── KV 基线 ──
export function fetchKVBaseline()    { return api.get('/kv-baseline/status') }
export function refreshKVBaseline()  { return api.post('/kv-baseline/refresh') }

// ── 监控面板 ──
export function fetchPanel(name: string)           { return api.get('/monitor/panel/' + name) }
export function fetchActiveRequests()              { return api.get('/monitor/active-requests') }
export function getRequestSources()                { return api.get('/monitor/request-sources') }

// ── 系统操作 ──
export function systemAction(action: string)       { return api.post('/system/' + action) }
export function sendControl(action: string, params?: Record<string, any>) {
  return api.post('/control', { action, params })
}

// ── 指标查询 ──
export function queryMetrics(query: string) {
  return api.get('/metrics/query', { params: { query } })
}
export function queryMetricsRange(query: string, start: string, end: string, step: string) {
  return api.get('/metrics/query_range', { params: { query, start, end, step } })
}

// ── 基准测试 ──
export function getBenchmarkHistory()     { return api.get('/benchmark/history') }
export function getBenchmarkProviders()   { return api.get('/benchmark/providers') }

// ── 集群 ──
export function getClusterNodes()         { return api.get('/cluster/nodes') }

// ── 告警 ──
export function fetchAlertStatus()        { return api.get('/alerts/status') }
export function testAlert()               { return api.post('/alerts/test') }

export default api
