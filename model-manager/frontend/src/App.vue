<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Activity, Boxes, ChevronRight, CircleStop, CloudUpload, ExternalLink, Filter, Gauge, HardDrive, LayoutGrid, Menu, Moon, RefreshCw, Search, SlidersHorizontal, Sun, TerminalSquare, X } from 'lucide-vue-next'
import DeployDrawer from './components/DeployDrawer.vue'
import ModelCard from './components/ModelCard.vue'
import { api } from './services/api'
import type { ModelArtifact, ModelsResponse, Operation } from './types/model'

type StatusFilter = 'all' | 'running' | 'stopped'
type SortKey = 'mtime' | 'size-desc' | 'size-asc' | 'name'

const state = ref<ModelsResponse>()
const operations = ref<Operation[]>([])
const favorites = ref<string[]>([])
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
const query = ref('')
const activeFamily = ref('all')
const status = ref<StatusFilter>('all')
const selectedTags = ref<string[]>([])
const sort = ref<SortKey>('mtime')
const embedded = new URLSearchParams(location.search).get('embedded') === '1'
const sidebarOpen = ref(false)
const mobileFilters = ref(false)
const detailModel = ref<ModelArtifact>()
const deployModel = ref<ModelArtifact>()
const renameModel = ref<ModelArtifact>()
const renameValue = ref('')
const theme = ref<'light' | 'dark'>((localStorage.getItem('model-manager-theme') as 'light' | 'dark') || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'))
const notice = ref<{ text: string; type: 'success' | 'error' | 'info' }>()
const deployment = ref<{ id: string; progress: number; phase: string }>()
let noticeTimer = 0

const models = computed(() => state.value?.models ?? [])
const currentId = computed(() => state.value?.current_model_id || '')
const deployableModels = computed(() => models.value.filter((model) => model.deployable))
const families = computed(() => {
  const counts = new Map<string, number>()
  for (const model of models.value) {
    const family = model.role === 'model' ? (model.family || model.alias || '未分类') : model.category
    counts.set(family, (counts.get(family) || 0) + 1)
  }
  return [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]))
})
const tags = computed(() => {
  const counts = new Map<string, number>()
  for (const model of models.value) for (const tag of model.tags) counts.set(tag, (counts.get(tag) || 0) + 1)
  return [...counts.entries()].sort((a, b) => b[1] - a[1])
})
const filteredModels = computed(() => {
  const needle = query.value.trim().toLowerCase()
  const result = models.value.filter((model) => {
    const family = model.role === 'model' ? (model.family || model.alias || '未分类') : model.category
    const running = model.id === currentId.value
    if (activeFamily.value !== 'all' && family !== activeFamily.value) return false
    if (status.value === 'running' && !running) return false
    if (status.value === 'stopped' && running) return false
    if (!selectedTags.value.every((tag) => model.tags.includes(tag))) return false
    if (needle && ![model.name, model.alias, model.family, model.format, model.quant_type, ...model.tags].join(' ').toLowerCase().includes(needle)) return false
    return true
  })
  return result.sort((a, b) => sort.value === 'mtime' ? b.modified - a.modified : sort.value === 'size-desc' ? b.size - a.size : sort.value === 'size-asc' ? a.size - b.size : a.name.localeCompare(b.name))
})
const currentModel = computed(() => models.value.find((model) => model.id === currentId.value))
const activeFilterCount = computed(() => (activeFamily.value !== 'all' ? 1 : 0) + selectedTags.value.length + (status.value !== 'all' ? 1 : 0))

function toast(text: string, type: 'success' | 'error' | 'info' = 'info') {
  notice.value = { text, type }
  clearTimeout(noticeTimer)
  noticeTimer = window.setTimeout(() => { notice.value = undefined }, 4200)
}

async function load(silent = false) {
  if (silent) refreshing.value = true
  else loading.value = true
  error.value = ''
  try {
    const [modelsData, quick, history] = await Promise.all([api.models(), api.quickSwitch(), api.operations()])
    state.value = modelsData
    favorites.value = quick.favorites || []
    operations.value = history
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '数据加载失败'
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function toggleTheme() {
  theme.value = theme.value === 'light' ? 'dark' : 'light'
  localStorage.setItem('model-manager-theme', theme.value)
  document.documentElement.dataset.theme = theme.value
}

function clearFilters() {
  activeFamily.value = 'all'
  status.value = 'all'
  selectedTags.value = []
  query.value = ''
}

function toggleTag(tag: string) {
  selectedTags.value = selectedTags.value.includes(tag) ? selectedTags.value.filter((item) => item !== tag) : [...selectedTags.value, tag]
}

async function toggleFavorite(model: ModelArtifact) {
  try {
    favorites.value = (await api.toggleFavorite(model.id)).favorites
  } catch (cause) { toast(cause instanceof Error ? cause.message : '收藏失败', 'error') }
}

async function stopServer() {
  if (!confirm('停止后推理 API 将不可用，确认停止服务？')) return
  try {
    await api.stop()
    toast('推理服务已停止', 'success')
    await load(true)
  } catch (cause) { toast(cause instanceof Error ? cause.message : '停止失败', 'error') }
}

async function removeModel(model: ModelArtifact) {
  if (!confirm(`“${model.name}”将移入受保护的回收站，是否继续？`)) return
  try {
    await api.remove(model.id)
    toast('已移至回收站', 'success')
    await load(true)
  } catch (cause) { toast(cause instanceof Error ? cause.message : '回收失败', 'error') }
}

function openRename(model: ModelArtifact) {
  renameModel.value = model
  renameValue.value = model.name
}

async function submitRename() {
  if (!renameModel.value || !/^[A-Za-z0-9_.-]+$/.test(renameValue.value) || renameValue.value.includes('..')) return toast('文件名格式不合法', 'error')
  try {
    await api.rename(renameModel.value.id, renameValue.value)
    renameModel.value = undefined
    toast('模型已重命名', 'success')
    await load(true)
  } catch (cause) { toast(cause instanceof Error ? cause.message : '重命名失败', 'error') }
}

async function rescan() {
  try {
    await api.rescan()
    toast('模型目录重扫完成', 'success')
    await load(true)
  } catch (cause) { toast(cause instanceof Error ? cause.message : '重扫失败', 'error') }
}

async function upload(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0]
  if (!file) return
  try {
    toast(`正在上传 ${file.name}…`)
    await api.upload(file)
    toast('上传完成', 'success')
    await load(true)
  } catch (cause) { toast(cause instanceof Error ? cause.message : '上传失败', 'error') }
  ;(event.target as HTMLInputElement).value = ''
}

async function watchDeployment(taskId: string) {
  deployModel.value = undefined
  deployment.value = { id: taskId, progress: 0, phase: '已创建' }
  for (let attempt = 0; attempt < 180; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 1000))
    try {
      const task = await api.deployment(taskId)
      deployment.value = { id: taskId, progress: task.progress || 0, phase: task.phase || task.state }
      if (task.state === 'succeeded') { toast('模型部署成功', 'success'); deployment.value = undefined; await load(true); return }
      if (['failed', 'cancelled'].includes(task.state)) throw new Error(task.error || '部署任务失败')
    } catch (cause) { deployment.value = undefined; toast(cause instanceof Error ? cause.message : '部署任务失败', 'error'); return }
  }
}

onMounted(() => {
  document.documentElement.dataset.theme = theme.value
  load()
})
onUnmounted(() => clearTimeout(noticeTimer))
</script>

<template>
  <div class="app-shell" :class="{ embedded }">
    <header class="topbar">
      <div class="brand-block">
        <button class="icon-button mobile-only" aria-label="打开导航" @click="sidebarOpen = !sidebarOpen"><Menu :size="20" /></button>
        <div class="brand-mark"><Boxes :size="22" /></div>
        <div><strong>模型中心</strong><span>Model Registry</span></div>
      </div>
      <nav class="product-nav">
        <a href="/"><Gauge :size="16" />监控面板</a>
        <a class="active" href="/model-manager/"><Boxes :size="16" />模型管理</a>
        <a href="/apps/cluster/"><Activity :size="16" />集群配置</a>
        <a href="/benchmark/"><SlidersHorizontal :size="16" />LLM 测速</a>
        <a href="/apps/inference/"><TerminalSquare :size="16" />推理 API</a>
      </nav>
      <div class="top-actions">
        <span class="service-state" :class="state?.server_running ? 'online' : 'offline'"><i />{{ state?.server_running ? '服务运行中' : '服务已停止' }}</span>
        <button class="icon-button" :aria-label="theme === 'light' ? '切换暗色' : '切换亮色'" @click="toggleTheme"><Moon v-if="theme === 'light'" :size="18" /><Sun v-else :size="18" /></button>
        <button class="icon-button" aria-label="刷新" @click="load(true)"><RefreshCw :size="18" :class="{ spin: refreshing }" /></button>
      </div>
    </header>

    <aside class="sidebar" :class="{ open: sidebarOpen }">
      <div class="sidebar-heading"><span>资产视图</span><button class="icon-button mobile-only" aria-label="关闭导航" @click="sidebarOpen = false"><X :size="18" /></button></div>
      <button class="nav-filter" :class="{ active: activeFamily === 'all' }" @click="activeFamily = 'all'"><LayoutGrid :size="16" /><span>全部工件</span><b>{{ models.length }}</b></button>
      <div class="sidebar-group">
        <p>模型系列</p>
        <button v-for="[family, count] in families" :key="family" class="nav-filter" :class="{ active: activeFamily === family }" @click="activeFamily = family"><span>{{ family }}</span><b>{{ count }}</b></button>
      </div>
      <div class="sidebar-group">
        <p>运行状态</p>
        <button class="nav-filter" :class="{ active: status === 'running' }" @click="status = status === 'running' ? 'all' : 'running'"><span>运行中</span><b>{{ state?.server_running ? 1 : 0 }}</b></button>
        <button class="nav-filter" :class="{ active: status === 'stopped' }" @click="status = status === 'stopped' ? 'all' : 'stopped'"><span>未运行</span><b>{{ Math.max(0, deployableModels.length - (state?.server_running ? 1 : 0)) }}</b></button>
      </div>
      <label class="upload-card">
        <CloudUpload :size="22" />
        <strong>上传 GGUF 模型</strong>
        <span>点击选择文件</span>
        <input type="file" accept=".gguf" @change="upload" />
      </label>
    </aside>

    <main class="main">
      <div v-if="error" class="error-banner"><div><strong>模型目录暂不可用</strong><p>{{ error }}</p></div><button class="button ghost" @click="load()">重新加载</button></div>
      <template v-else>
        <section class="page-heading">
          <div><span class="eyebrow">资产与部署</span><h1>模型库</h1><p>统一管理本地模型、运行配置与部署记录</p></div>
          <button class="button ghost" @click="rescan"><RefreshCw :size="16" />重扫目录</button>
        </section>

        <section class="stats-grid">
          <article><div class="stat-icon blue"><Boxes :size="20" /></div><div><span>可部署模型</span><strong>{{ state?.summary?.deployable ?? deployableModels.length }}</strong><small>共 {{ models.length }} 个工件</small></div></article>
          <article><div class="stat-icon violet"><HardDrive :size="20" /></div><div><span>模型总容量</span><strong>{{ state?.total_size || '—' }}</strong><small>含视觉与草稿组件</small></div></article>
          <article><div class="stat-icon green"><Activity :size="20" /></div><div><span>磁盘可用</span><strong>{{ state?.disk_free || '—' }}</strong><small>建议保留 15% 以上</small></div></article>
        </section>

        <section v-if="state?.server_running && currentModel" class="runtime-card">
          <div class="runtime-left"><div class="pulse-icon"><Activity :size="19" /></div><div><span>当前运行</span><strong>{{ currentModel.family || currentModel.alias }}</strong><p>{{ currentModel.name }}</p></div></div>
          <div class="runtime-config">
            <span>CTX <b>{{ (state.current_config.ctx_size || 0) / 1024 }}K</b></span><span>GPU <b>{{ state.current_config.gpu || 'all' }}</b></span><span>NGL <b>{{ state.current_config.ngl }}</b></span><span>并发 <b>{{ state.current_config.concurrency }}</b></span><span>K/V <b>{{ state.current_config.k_cache_type }}/{{ state.current_config.v_cache_type }}</b></span>
          </div>
          <div class="runtime-actions"><button class="button ghost compact" @click="deployModel = currentModel">调整配置</button><button class="button danger compact" @click="stopServer"><CircleStop :size="15" />停止</button></div>
        </section>

        <section class="library-card">
          <div class="library-toolbar">
            <div class="search-box"><Search :size="18" /><input v-model="query" placeholder="搜索名称、系列、量化或标签…" /></div>
            <button class="button ghost mobile-filter-button" @click="mobileFilters = !mobileFilters"><Filter :size="16" />筛选<span v-if="activeFilterCount">{{ activeFilterCount }}</span></button>
            <select v-model="sort" class="sort-select"><option value="mtime">最新修改</option><option value="size-desc">容量：大到小</option><option value="size-asc">容量：小到大</option><option value="name">名称：A–Z</option></select>
          </div>
          <div class="tag-filter-bar" :class="{ expanded: mobileFilters }">
            <span>标签</span>
            <button v-for="[tag, count] in tags" :key="tag" :class="{ active: selectedTags.includes(tag) }" @click="toggleTag(tag)">{{ tag }} <small>{{ count }}</small></button>
            <button v-if="activeFilterCount || query" class="clear-filter" @click="clearFilters">清除筛选</button>
          </div>
          <div class="result-summary"><span>显示 <strong>{{ filteredModels.length }}</strong> / {{ models.length }} 个工件</span><span v-if="activeFamily !== 'all'">{{ activeFamily }} <button @click="activeFamily = 'all'">×</button></span></div>
          <div v-if="loading" class="loading-list"><div v-for="n in 5" :key="n" /></div>
          <div v-else-if="filteredModels.length" class="model-list">
            <ModelCard v-for="model in filteredModels" :key="model.id" :model="model" :running="model.id === currentId" :favorite="favorites.includes(model.id) || favorites.includes(model.name)" @deploy="deployModel = $event" @detail="detailModel = $event" @favorite="toggleFavorite" @rename="openRename" @remove="removeModel" />
          </div>
          <div v-else class="empty-state"><Search :size="28" /><strong>没有匹配项</strong><p>请更换关键词或清除筛选条件。</p><button class="button ghost" @click="clearFilters">清除筛选</button></div>
        </section>

        <details class="operations-panel">
          <summary><span><Activity :size="17" />最近操作</span><small>{{ operations.length }} 条</small><ChevronRight :size="17" /></summary>
          <div class="operation-list"><div v-for="operation in operations" :key="operation.operation_id"><i :class="operation.state" /><span>{{ operation.path.replace('/api/', '') }}</span><b>{{ operation.state === 'succeeded' ? '成功' : operation.state === 'running' ? '执行中' : '失败' }}</b><time>{{ new Date(operation.started_at * 1000).toLocaleString('zh-CN') }}</time><small>{{ operation.duration_ms ? `${Math.round(operation.duration_ms)}ms` : '—' }}</small></div></div>
        </details>
      </template>
    </main>

    <DeployDrawer v-if="deployModel" :model="deployModel" :current-config="state?.current_config || {}" @close="deployModel = undefined" @deployed="watchDeployment" />

    <div v-if="detailModel" class="modal-backdrop" @click.self="detailModel = undefined"><section class="dialog"><header><div><span class="eyebrow">工件详情</span><h2>{{ detailModel.family || detailModel.alias }}</h2></div><button class="icon-button" @click="detailModel = undefined">×</button></header><dl><div><dt>文件名</dt><dd>{{ detailModel.name }}</dd></div><div><dt>相对路径</dt><dd>{{ detailModel.relative_path }}</dd></div><div><dt>类型 / 量化</dt><dd>{{ detailModel.format || '—' }} / {{ detailModel.quant_type || '—' }}</dd></div><div><dt>容量</dt><dd>{{ detailModel.size_human }}</dd></div><div><dt>架构</dt><dd>{{ detailModel.classification?.architecture || '—' }}</dd></div><div><dt>上下文</dt><dd>{{ detailModel.ctx_default ? `${detailModel.ctx_default / 1024}K` : '—' }}</dd></div></dl><footer><button class="button ghost" @click="detailModel = undefined">关闭</button><button v-if="detailModel.deployable" class="button primary" @click="deployModel = detailModel; detailModel = undefined">部署此模型</button></footer></section></div>

    <div v-if="renameModel" class="modal-backdrop" @click.self="renameModel = undefined"><form class="dialog small-dialog" @submit.prevent="submitRename"><header><div><span class="eyebrow">资产管理</span><h2>重命名工件</h2></div><button type="button" class="icon-button" @click="renameModel = undefined">×</button></header><label class="full-field"><span>新文件名</span><input v-model="renameValue" autofocus /></label><p class="field-help">仅允许字母、数字、下划线、点和横线；目录位置保持不变。</p><footer><button type="button" class="button ghost" @click="renameModel = undefined">取消</button><button class="button primary">保存名称</button></footer></form></div>

    <div v-if="deployment" class="deployment-bar"><div><Activity :size="17" /><span>部署任务·{{ deployment.phase }}</span><b>{{ deployment.progress }}%</b></div><div><i :style="{ width: `${deployment.progress}%` }" /></div></div>
    <Transition name="toast"><div v-if="notice" class="toast" :class="notice.type">{{ notice.text }}</div></Transition>
    <div v-if="sidebarOpen" class="sidebar-backdrop" @click="sidebarOpen = false" />
  </div>
</template>
