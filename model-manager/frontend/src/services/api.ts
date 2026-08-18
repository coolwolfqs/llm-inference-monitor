import type { DeploymentPlan, DeployPayload, Engine, ModelsResponse, Operation, Preflight, ProjectionArtifact } from '../types/model'

const base = '/model-manager/api'

function adminKey(): string {
  let key = sessionStorage.getItem('dashboard_admin_key') ?? ''
  if (!key) {
    key = window.prompt('请输入管理密钥：') ?? ''
    if (!key) throw new Error('已取消管理操作')
    sessionStorage.setItem('dashboard_admin_key', key)
  }
  return key
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  const method = (init.method ?? 'GET').toUpperCase()
  if (!['GET', 'HEAD'].includes(method)) headers.set('X-Admin-Key', adminKey())
  if (init.body && !(init.body instanceof FormData)) headers.set('Content-Type', 'application/json')
  const response = await fetch(`${base}${path}`, { ...init, headers })
  if (response.status === 403) sessionStorage.removeItem('dashboard_admin_key')
  const data = await response.json().catch(() => ({}))
  if (!response.ok) {
    const detail = data.detail ?? data.error
    const message = typeof detail === 'string' ? detail : detail ? JSON.stringify(detail) : `请求失败 (${response.status})`
    throw new Error(message)
  }
  return data as T
}

export const api = {
  models: () => request<ModelsResponse>('/models'),
  engines: async () => (await request<{ engines: Engine[] }>('/engines')).engines,
  operations: async () => (await request<{ operations: Operation[] }>('/operations?limit=20')).operations,
  preflight: (id: string) => request<Preflight>(`/models/preflight/${encodeURIComponent(id)}`),
  deploymentPlan: (modelId: string, engineKey: string, profileId = 'default') => request<DeploymentPlan>(
    `/models/deployment-plan/${encodeURIComponent(modelId)}/${encodeURIComponent(engineKey)}/${encodeURIComponent(profileId)}`,
  ),
  projectors: async (id: string) =>
    (await request<{ files: ProjectionArtifact[] }>(`/models/mmproj-files/${encodeURIComponent(id)}`)).files,
  quickSwitch: () => request<{ favorites: string[]; recent: string[] }>('/quick-switch'),
  toggleFavorite: (name: string) => request<{ favorites: string[] }>('/quick-switch/toggle-fav', { method: 'POST', body: JSON.stringify({ name }) }),
  stop: () => request<{ stopped: boolean; message?: string }>('/models/stop', { method: 'POST' }),
  remove: (filename: string) => request<{ size?: string }>('/models/delete', { method: 'POST', body: JSON.stringify({ filename }) }),
  rename: (filename: string, new_name: string) => request<{ to: string }>('/models/rename', { method: 'POST', body: JSON.stringify({ filename, new_name }) }),
  rescan: () => request('/catalog/rescan', { method: 'POST' }),
  deploy: (payload: DeployPayload) => request<{ task_id: string }>('/deployments', { method: 'POST', body: JSON.stringify(payload) }),
  deployment: (taskId: string) => request<{ state: string; progress: number; phase: string; error?: string }>(`/deployments/${encodeURIComponent(taskId)}`),
  cancelDownload: (taskId: string) => request<{ state: string; phase: string }>(
    `/hub/downloads/${encodeURIComponent(taskId)}/cancel`, { method: 'POST' },
  ),
  upload: (file: File) => {
    const data = new FormData()
    data.append('file', file)
    return request('/upload', { method: 'POST', body: data })
  },
}
