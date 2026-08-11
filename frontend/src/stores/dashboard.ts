import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { fetchPanel, createSSEStream } from '../api'

export interface PanelData {
  timestamp: number
  [key: string]: any
}

export interface SSEMessage {
  type: string
  gpus?: any
  system?: any
  inference_stats?: any
  llm_metrics?: any
  kv_cache?: any
  health_score: number
  health_reasons?: any[]
  uptime: string
  ts: number
}

export const useDashboardStore = defineStore('dashboard', () => {
  const overview = ref<PanelData | null>(null)
  const inference = ref<PanelData | null>(null)
  const system = ref<PanelData | null>(null)
  const hardware = ref<PanelData | null>(null)
  const services = ref<PanelData | null>(null)
  const kvCache = ref<PanelData | null>(null)
  const healthScore = ref(100)
  const uptime = ref('')
  const sseConnected = ref(false)
  const lastUpdate = ref<Date | null>(null)

  const sse = ref<EventSource | null>(null)

  async function fetchPanelData(name: string) {
    try {
      const response = await fetchPanel(name)
      switch (name) {
        case 'overview':
          overview.value = response.data
          if (response.data.health_score !== undefined) {
            healthScore.value = response.data.health_score
          }
          if (response.data.uptime !== undefined) {
            uptime.value = response.data.uptime
          }
          break
        case 'inference':
          inference.value = response.data
          break
        case 'system':
          system.value = response.data
          break
        case 'hardware':
          hardware.value = response.data
          break
        case 'services':
          services.value = response.data
          break
      }
      lastUpdate.value = new Date()
    } catch (error) {
      console.error('Fetch error:', error)
    }
  }

  function connectSSE() {
    if (sse.value) {
      sse.value.close()
    }

    const eventSource = new EventSource('/api/sse')
    sse.value = eventSource

    eventSource.onopen = () => {
      sseConnected.value = true
    }

    eventSource.onerror = () => {
      sseConnected.value = false
    }

    eventSource.addEventListener('tick', (event) => {
      try {
        const data: SSEMessage = JSON.parse(event.data)
        healthScore.value = data.health_score || 100
        uptime.value = data.uptime || ''

        if (data.gpus) {
          hardware.value = { gpus: data.gpus, timestamp: data.ts }
        }
        if (data.system) {
          system.value = {
            ...(system.value || {}),
            system: { ...((system.value && system.value.system) || {}), ...data.system },
            timestamp: data.ts
          }
        }
        if (data.inference_stats) {
          inference.value = {
            ...(inference.value || {}),
            inference: data.inference_stats,
            timestamp: data.ts
          }
        }
        if (data.llm_metrics) {
          inference.value = {
            ...(inference.value || {}),
            llm: data.llm_metrics,
            timestamp: data.ts
          }
        }
        if (data.kv_cache) {
          kvCache.value = { ...data.kv_cache, timestamp: data.ts }
        }

        lastUpdate.value = new Date()
      } catch (error) {
        console.error('SSE parse error:', error)
      }
    })
  }

  function disconnectSSE() {
    if (sse.value) {
      sse.value.close()
      sse.value = null
    }
    sseConnected.value = false
  }

  // Toast notification
  function showToast(message: string, type: 'success' | 'error' | 'info' = 'info') {
    // Simple toast via console + custom event for App.vue to handle
    const evt = new CustomEvent('app-toast', { detail: { message, type } })
    window.dispatchEvent(evt)
    console.log('[Toast]', type.toUpperCase(), message)
  }

  // Confirm dialog via custom event
  function showConfirm(options: { title: string; message: string; detail?: string; icon?: string; confirmText?: string; danger?: boolean }): Promise<boolean> {
    return new Promise(resolve => {
      const evt = new CustomEvent('app-confirm', { detail: { ...options, resolve } })
      window.dispatchEvent(evt)
    })
  }

  return {
    overview,
    inference,
    system,
    hardware,
    services,
    kvCache,
    healthScore,
    uptime,
    sseConnected,
    lastUpdate,
    fetchPanelData,
    connectSSE,
    disconnectSSE,
    showToast,
    showConfirm,
  }
})
