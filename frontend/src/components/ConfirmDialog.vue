<template>
  <div v-if=visible class=confirm-overlay @click.self=cancel>
    <div class="confirm-dialog">
      <div class="confirm-header">
        <span class=confirm-icon>{{ icon }}</span>
        <h3>{{ title }}</h3>
      </div>
      <div class="confirm-body">
        <p>{{ message }}</p>
        <div v-if=detail class=confirm-detail>{{ detail }}</div>
      </div>
      <div class="confirm-footer">
        <button class="btn-cancel" @click="cancel" :disabled="loading">取消</button>
        <button class="btn-confirm" @click="confirm" :disabled="loading" :class="{ danger: danger }">
          {{ loading ? '执行中...' : confirmText }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang=ts>
import { ref } from 'vue'
const visible = ref(false)
const title = ref('')
const message = ref('')
const detail = ref('')
const icon = ref('⚠️')
const confirmText = ref('确认')
const danger = ref(false)
const loading = ref(false)
let resolveFn: ((value: boolean) => void) | null = null

function show(options: { title: string; message: string; detail?: string; icon?: string; confirmText?: string; danger?: boolean }): Promise<boolean> {
  title.value = options.title
  message.value = options.message
  detail.value = options.detail || ''
  icon.value = options.icon || '⚠️'
  confirmText.value = options.confirmText || '确认'
  danger.value = options.danger || false
  loading.value = false
  visible.value = true
  return new Promise(resolve => { resolveFn = resolve })
}
async function confirm() { loading.value = true; if (resolveFn) resolveFn(true); visible.value = false }
function cancel() { if (resolveFn) resolveFn(false); visible.value = false }
defineExpose({ show })
</script>

<style scoped>
.confirm-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.7); display: flex; align-items: center; justify-content: center; z-index: 2000; padding: 16px; }
.confirm-dialog { background: var(--bg-secondary); border-radius: 12px; max-width: 420px; width: 100%; box-shadow: 0 20px 60px rgba(0,0,0,0.5); overflow: hidden; }
.confirm-header { display: flex; align-items: center; gap: 12px; padding: 20px 24px 12px; border-bottom: 1px solid var(--border-color); }
.confirm-icon { font-size: 1.5rem; }
.confirm-header h3 { margin: 0; font-size: 1.1rem; color: var(--text-primary); }
.confirm-body { padding: 16px 24px; }
.confirm-body p { margin: 0; font-size: 0.95rem; color: var(--text-primary); line-height: 1.5; }
.confirm-detail { margin-top: 8px; padding: 8px 12px; background: var(--bg-hover); border-radius: 6px; font-size: 0.8rem; color: var(--text-secondary); font-family: monospace; }
.confirm-footer { display: flex; justify-content: flex-end; gap: 10px; padding: 16px 24px; border-top: 1px solid var(--border-color); }
.btn-cancel, .btn-confirm { padding: 8px 20px; border-radius: 6px; font-size: 0.85rem; cursor: pointer; border: 1px solid var(--border-color); }
.btn-cancel { background: var(--bg-hover); color: var(--text-primary); }
.btn-confirm { background: var(--accent-color); color: white; border: none; font-weight: 600; }
.btn-confirm.danger { background: #dc2626; }
.btn-confirm:disabled, .btn-cancel:disabled { opacity: 0.5; cursor: not-allowed; }
</style>