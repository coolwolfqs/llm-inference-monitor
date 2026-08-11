<template>
  <div class="panel-grid">
    <div class="card card-full">
      <div class="card-header">
        <h3>模型列表</h3>
        <div class="header-actions">
          <span class="model-count">显示 {{ filteredModels.length }} / 共 {{ models.length }}</span>
          <button @click="refreshModels" class="btn-refresh" title="刷新">&#x21bb;</button>
        </div>
      </div>
      <div class="card-body">
        <!-- 部署状态 -->
        <div class="deploy-status" v-if="deployStatus && deployStatus.model_path">
          <div class="ds-header">
            <span class="ds-badge" :class="serviceRunning ? 'running' : 'stopped'">{{ serviceRunning ? '\u25cf 运行中' : '\u25cb 未运行' }}</span>
            <span class="ds-model">{{ deployStatus.alias || currentModelName }}</span>
          </div>
          <div class="ds-grid">
            <div class="ds-item ds-clickable" @click="quickEditParam('ctx_size')" title="点击调整"><span class="ds-label">上下文</span><span class="ds-val">{{ fmtCtx(deployStatus.ctx_size) }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('ngl')" title="点击调整"><span class="ds-label">GPU层</span><span class="ds-val">{{ deployStatus.ngl ?? '-' }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('batch')" title="点击调整"><span class="ds-label">Batch</span><span class="ds-val">{{ deployStatus.batch ?? '-' }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('ubatch')" title="点击调整"><span class="ds-label">UBatch</span><span class="ds-val">{{ deployStatus.ubatch ?? '-' }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('np')" title="点击调整"><span class="ds-label">并发</span><span class="ds-val">{{ deployStatus.np ?? '-' }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('threads')" title="点击调整"><span class="ds-label">线程</span><span class="ds-val">{{ deployStatus.threads ?? '-' }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('k_cache_type')" title="点击调整"><span class="ds-label">K Cache</span><span class="ds-val">{{ cacheLabel(deployStatus.cache_type_k) }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('v_cache_type')" title="点击调整"><span class="ds-label">V Cache</span><span class="ds-val">{{ cacheLabel(deployStatus.cache_type_v) }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('flash_attn')" title="点击切换"><span class="ds-label">Flash Attn</span><span class="ds-val">{{ deployStatus.flash_attn === 'on' || deployStatus.flash_attn === true ? '\u2705' : '\u274c' }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('spec_draft_n_max')" title="点击调整"><span class="ds-label">Spec Draft</span><span class="ds-val">{{ deployStatus.spec_draft_n_max ?? '-' }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('temp')" title="点击调整"><span class="ds-label">Temperature</span><span class="ds-val">{{ deployStatus.temp ?? '-' }}</span></div>
            <div class="ds-item ds-clickable" @click="quickEditParam('reasoning')" title="点击切换"><span class="ds-label">Reasoning</span><span class="ds-val">{{ deployStatus.reasoning || 'off' }}</span></div>
            <div class="ds-item ds-clickable" v-if="deployStatus.split_mode || deployStatus.split_mode === ''" @click="quickEditParam('split_mode')" title="点击调整"><span class="ds-label">Split Mode</span><span class="ds-val">{{ deployStatus.split_mode || '默认' }}</span></div>
            <div class="ds-item ds-clickable" v-if="deployStatus.fit || deployStatus.fit === ''" @click="quickEditParam('fit')" title="点击切换"><span class="ds-label">Fit</span><span class="ds-val">{{ deployStatus.fit || 'off' }}</span></div>
          </div>
        </div>

        <!-- 搜索栏 -->
        <div class="filter-bar">
          <div class="search-box">
            <span class="search-icon">&#x1f50d;</span>
            <input v-model="search" placeholder="搜索模型名称或别名..." class="search-input" />
          </div>
          <div class="filter-chips">
            <div class="chip-group">
              <span class="chip-label">架构</span>
              <button v-for="arch in archOptions" :key="arch"
                class="chip" :class="{ active: filterArch === arch }"
                @click="filterArch = filterArch === arch ? '' : arch">
                {{ arch === '' ? '全部' : arch }}
              </button>
            </div>
            <div class="chip-group">
              <span class="chip-label">量化</span>
              <button v-for="q in quantOptions" :key="q"
                class="chip" :class="{ active: filterQuant === q }"
                @click="filterQuant = filterQuant === q ? '' : q">
                {{ q === '' ? '全部' : q }}
              </button>
            </div>
            <div class="chip-group">
              <span class="chip-label">标签</span>
              <button v-for="tag in tagOptions" :key="tag"
                class="chip" :class="{ active: activeTags.includes(tag) }"
                @click="toggleTag(tag)">
                {{ tag }}
              </button>
            </div>
            <div class="chip-group">
              <span class="chip-label">大小</span>
              <button v-for="s in sizeOptions" :key="s.label"
                class="chip" :class="{ active: filterSize === s.label }"
                @click="filterSize = filterSize === s.label ? '' : s.label">
                {{ s.label }}
              </button>
            </div>
          </div>
        </div>

        <!-- Quick Switch 收藏/最近 -->
        <div class="quick-switch-bar" v-if="favorites.length > 0 || recent.length > 0">
          <div class="qs-section" v-if="favorites.length > 0">
            <span class="qs-label">&#x2b50; 收藏</span>
            <button v-for="f in favorites" :key="f" @click="quickDeploy(f)" class="qs-btn">
              {{ getAlias(f) }}
            </button>
          </div>
          <div class="qs-section" v-if="recent.length > 0">
            <span class="qs-label">&#x1f552; 最近</span>
            <button v-for="r in recent.slice(0, 5)" :key="r" @click="quickDeploy(r)" class="qs-btn qs-recent">
              {{ getAlias(r) }}
            </button>
          </div>
        </div>

        <!-- 模型列表 -->
        <div class="model-list">
          <div v-for="m in filteredModels" :key="m.path" class="model-card" :class="{ 'model-running': m.is_running }">
            <div class="model-main">
              <div class="model-title-row">
                <span class="running-badge" v-if="m.is_running">运行中</span>
                <span class="model-alias" @click="toggleFavModel(m.name)" :title="'点击' + (isFav(m.name) ? '取消收藏' : '收藏')">
                  {{ isFav(m.name) ? '&#x2b50; ' : '' }}{{ m.alias || m.name }}
                </span>
                <span class="model-size-badge">{{ m.size_human }}</span>
              </div>
              <div class="model-filename" :title="m.name">{{ m.name }}</div>
              <div class="model-tags">
                <span v-if="m.quant_type" class="tag tag-quant">{{ m.quant_type }}</span>
                <span v-for="t in (m.tags || [])" :key="t" class="tag" :class="tagClass(t)">{{ tagLabel(t) }}</span>
                <span v-if="isMMProj(m)" class="tag tag-mmproj">视觉插件</span>
              </div>
              <!-- Deploy Prefs 摘要 -->
              <div class="deploy-prefs-summary" v-if="hasPrefs(m.name)">
                <span class="pref-tag">ctx: {{ getPref(m.name, 'ctx_size') }}</span>
                <span class="pref-tag">ngl: {{ getPref(m.name, 'ngl') }}</span>
                <span class="pref-tag">np: {{ getPref(m.name, 'np') }}</span>
              </div>
            </div>
            <div class="model-actions">
              <button v-if="!m.is_running && !isMMProj(m)" @click="openDeploy(m)" class="btn-deploy">部署</button>
              <button v-if="m.is_running" disabled class="btn-running">运行中</button>
              <button @click="toggleFavModel(m.name)" class="btn-fav" :class="{ active: isFav(m.name) }">
                {{ isFav(m.name) ? '&#x2b50;' : '&#x2606;' }}
              </button>
            </div>
          </div>
        </div>

        <div v-if="filteredModels.length === 0" class="empty-state">
          <p>没有找到匹配的模型</p>
          <button @click="clearFilters" class="btn-clear">清除筛选</button>
        </div>
      </div>
    </div>

    <!-- 部署参数弹窗 -->
    <div v-if="showDeployModal" class="modal-overlay" @click.self="closeDeploy">
      <div class="modal-content">
        <div class="modal-header">
          <h3>部署模型</h3>
          <button @click="closeDeploy" class="btn-close">&times;</button>
        </div>
        <div class="modal-body">
          <div class="deploy-model-info">
            <span class="deploy-name">{{ deployModel.alias || deployModel.name }}</span>
            <span class="deploy-file">{{ deployModel.name }}</span>
            <span class="deploy-size">{{ deployModel.size_human }}</span>
          </div>

          <!-- 参数预设选择 -->
          <div class="preset-bar">
            <span class="preset-label">参数预设:</span>
            <button v-for="preset in presets" :key="preset.name"
              @click="applyPreset(preset)" class="preset-btn" :class="{ active: currentPreset === preset.name }">
              {{ preset.label }}
            </button>
            <button @click="loadSavedPrefs" class="preset-btn preset-saved" v-if="hasPrefs(deployModel.name)">
              &#x1f4be; 上次参数
            </button>
          </div>

          <div class="params-grid">
            <div class="param-item">
              <label>上下文大小</label>
              <select v-model.number="deployParams.ctx_size">
                <option :value="8192">8K</option>
                <option :value="16384">16K</option>
                <option :value="32768">32K</option>
                <option :value="65536">64K</option>
                <option :value="131072">128K</option>
                <option :value="262144">256K</option>
                <option :value="524288">512K</option>
              </select>
            </div>
            <div class="param-item">
              <label>GPU 层数 (ngl)</label>
              <input type="number" v-model.number="deployParams.ngl" min="0" max="99" />
            </div>
            <div class="param-item">
              <label>并发数 (np)</label>
              <select v-model.number="deployParams.np">
                <option :value="1">1</option>
                <option :value="2">2</option>
                <option :value="4">4</option>
                <option :value="8">8</option>
              </select>
            </div>
            <div class="param-item">
              <label>Batch Size</label>
              <input type="number" v-model.number="deployParams.batch" min="64" max="4096" step="64" />
            </div>
            <div class="param-item">
              <label>UBatch Size</label>
              <input type="number" v-model.number="deployParams.ubatch" min="64" max="1024" step="64" />
            </div>
            <div class="param-item">
              <label>K Cache 类型</label>
              <select v-model="deployParams.k_cache_type">
                <option value="f32">F32</option>
                <option value="f16">F16</option>
                <option value="q8_0">Q8_0</option>
                <option value="q4_0">Q4_0</option>
                <option value="iq4_nl">IQ4_NL</option>
                <option value="turbo4">Turbo4</option>
              </select>
            </div>
            <div class="param-item">
              <label>V Cache 类型</label>
              <select v-model="deployParams.v_cache_type">
                <option value="f32">F32</option>
                <option value="f16">F16</option>
                <option value="q8_0">Q8_0</option>
                <option value="q4_0">Q4_0</option>
                <option value="iq4_nl">IQ4_NL</option>
                <option value="turbo4">Turbo4</option>
              </select>
            </div>
            <div class="param-item">
              <label>Flash Attention</label>
              <select v-model="deployParams.flash_attn">
                <option value="on">开启</option>
                <option value="off">关闭</option>
              </select>
            </div>
            <div class="param-item">
              <label>Chunked Batch</label>
              <select v-model="deployParams.chunked_batch">
                <option value="on">开启</option>
                <option value="off">关闭</option>
              </select>
            </div>
            <div class="param-item">
              <label>温度 (temp)</label>
              <input type="number" v-model.number="deployParams.temp" min="0" max="2" step="0.1" />
            </div>
            <div class="param-item">
              <label>推理模式</label>
              <select v-model="deployParams.reasoning">
                <option value="off">关闭</option>
                <option value="on">开启</option>
              </select>
            </div>
            <div class="param-item">
              <label>计算线程</label>
              <input type="number" v-model.number="deployParams.threads" min="1" max="32" />
            </div>
            <div class="param-item">
              <label>HTTP 线程</label>
              <input type="number" v-model.number="deployParams.threads_http" min="1" max="16" />
            </div>
            <div class="param-item">
              <label>Draft K Cache</label>
              <select v-model="deployParams.draft_k_cache">
                <option value="f32">F32</option>
                <option value="f16">F16</option>
                <option value="q8_0">Q8_0</option>
                <option value="turbo2">Turbo2</option>
              </select>
            </div>
            <div class="param-item">
              <label>Draft V Cache</label>
              <select v-model="deployParams.draft_v_cache">
                <option value="f32">F32</option>
                <option value="f16">F16</option>
                <option value="q8_0">Q8_0</option>
                <option value="turbo2">Turbo2</option>
              </select>
            </div>
            <div class="param-item">
              <label>Spec Draft Max</label>
              <input type="number" v-model.number="deployParams.spec_draft_n_max" min="1" max="8" />
            </div>
            <div class="param-item">
              <label>Split Mode</label>
              <select v-model="deployParams.split_mode">
                <option value="">不设置(默认)</option>
                <option value="none">none</option>
                <option value="layer">layer</option>
                <option value="row">row</option>
              </select>
            </div>
            <div class="param-item">
              <label>Fit (--fit)</label>
              <select v-model="deployParams.fit">
                <option value="">关闭</option>
                <option value="on">开启</option>
              </select>
            </div>
          </div>

          <div class="deploy-actions">
            <label class="save-prefs-check">
              <input type="checkbox" v-model="savePrefs" /> 保存为此模型的默认参数
            </label>
            <button @click="closeDeploy" class="btn-cancel">取消</button>
            <button @click="confirmDeploy" class="btn-confirm" :disabled="deploying">
              {{ deploying ? '部署中...' : '确认部署' }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { getModels, deployModel as apiDeployModel, addRecent } from '../api'

interface Model {
  name: string
  alias: string
  path: string
  size: number
  size_human: string
  format: string
  quant_type: string
  tags: string[]
  is_running: boolean
  deploy_config?: any
}

const models = ref<Model[]>([])
const serverRunning = ref(false)
const currentConfig = ref<any>(null)
const currentModel = ref('')
const search = ref('')
const filterArch = ref('')
const filterQuant = ref('')
const activeTags = ref<string[]>([])
const filterSize = ref('')

const showDeployModal = ref(false)
const deployModel = ref<Model>({} as Model)
const deploying = ref(false)
const savePrefs = ref(true)
const currentPreset = ref('')

// Deploy Prefs (per-model parameter persistence via localStorage)
const deployPrefs = ref<Record<string, any>>({})
const favorites = ref<string[]>([])
const recent = ref<string[]>([])

function loadPrefs() {
  try {
    const raw = localStorage.getItem('deploy_prefs')
    if (raw) deployPrefs.value = JSON.parse(raw)
  } catch (e) { deployPrefs.value = {} }
  try {
    const raw = localStorage.getItem('model_favorites')
    if (raw) favorites.value = JSON.parse(raw)
  } catch (e) { favorites.value = [] }
  try {
    const raw = localStorage.getItem('model_recent')
    if (raw) recent.value = JSON.parse(raw)
  } catch (e) { recent.value = [] }
}

function savePrefsForModel(modelName: string, params: any) {
  deployPrefs.value[modelName] = { ...params }
  localStorage.setItem('deploy_prefs', JSON.stringify(deployPrefs.value))
}

function getPrefsForModel(modelName: string): any {
  return deployPrefs.value[modelName] || null
}

function hasPrefs(modelName: string): boolean {
  return !!deployPrefs.value[modelName]
}

function getPref(modelName: string, key: string): any {
  return deployPrefs.value[modelName]?.[key] || '-'
}

function isFav(modelName: string): boolean {
  return favorites.value.includes(modelName)
}

function toggleFavModel(modelName: string) {
  const idx = favorites.value.indexOf(modelName)
  if (idx >= 0) favorites.value.splice(idx, 1)
  else favorites.value.push(modelName)
  localStorage.setItem('model_favorites', JSON.stringify(favorites.value))
}

function addRecentModel(modelName: string) {
  const idx = recent.value.indexOf(modelName)
  if (idx >= 0) recent.value.splice(idx, 1)
  recent.value.unshift(modelName)
  if (recent.value.length > 10) recent.value = recent.value.slice(0, 10)
  localStorage.setItem('model_recent', JSON.stringify(recent.value))
  // Also save to backend
  addRecent({ model: modelName }).catch(() => {})
}

const deployParams = ref({
  ctx_size: 262144, ngl: 99, batch: 512, ubatch: 256,
  k_cache_type: 'turbo4', v_cache_type: 'turbo4',
  flash_attn: 'on', temp: 0.6, np: 2, reasoning: 'off',
  threads: 8, threads_http: 4,
  draft_k_cache: 'turbo2', draft_v_cache: 'turbo2',
  spec_draft_n_max: 2, chunked_batch: 'on',
  split_mode: '', fit: '',
})

const presets = [
  { name: 'default', label: '默认', params: { ctx_size: 262144, ngl: 99, batch: 512, ubatch: 256, np: 2, flash_attn: 'on', chunked_batch: 'on', temp: 0.6, reasoning: 'off', threads: 8, threads_http: 4, split_mode: '', fit: '' } },
  { name: 'fast', label: '快速', params: { ctx_size: 32768, ngl: 99, batch: 256, ubatch: 128, np: 4, flash_attn: 'on', chunked_batch: 'on', temp: 0.6, reasoning: 'off', threads: 16, threads_http: 8, split_mode: '', fit: '' } },
  { name: 'long', label: '长上下文', params: { ctx_size: 524288, ngl: 99, batch: 1024, ubatch: 512, np: 1, flash_attn: 'on', chunked_batch: 'on', temp: 0.6, reasoning: 'off', threads: 8, threads_http: 4, split_mode: '', fit: '' } },
  { name: 'balanced', label: '均衡', params: { ctx_size: 131072, ngl: 99, batch: 512, ubatch: 256, np: 2, flash_attn: 'on', chunked_batch: 'on', temp: 0.6, reasoning: 'off', threads: 8, threads_http: 4, split_mode: '', fit: '' } },
]

function applyPreset(preset: any) {
  deployParams.value = { ...deployParams.value, ...preset.params }
  currentPreset.value = preset.name
}

function loadSavedPrefs() {
  const saved = getPrefsForModel(deployModel.value.name)
  if (saved) {
    deployParams.value = { ...deployParams.value, ...saved }
    currentPreset.value = ''
  }
}

const archOptions = computed(() => {
  const archs = new Set<string>()
  models.value.forEach(m => {
    const a = m.alias || m.name
    if (a.includes('Qwopus3.6-27B') || a.startsWith('Qwen3.6-27B')) archs.add('Qwen3.6-27B')
    else if (a.startsWith('Qwen3.6-35B')) archs.add('Qwen3.6-35B')
    else if (a.startsWith('Gemma4-26B')) archs.add('Gemma4-26B')
    else if (a.startsWith('Gemma4-31B')) archs.add('Gemma4-31B')
    else if (a.startsWith('Qwen3.5-9B')) archs.add('Qwen3.5-9B')
    else if (m.quant_type === 'mmproj' || a.includes('mmproj')) archs.add('视觉插件')
  })
  return ['', ...Array.from(archs)]
})

const quantOptions = computed(() => {
  const qs = new Set<string>()
  models.value.forEach(m => { if (m.quant_type) qs.add(m.quant_type) })
  return ['', ...Array.from(qs).sort()]
})

const tagOptions = computed(() => {
  const ts = new Set<string>()
  models.value.forEach(m => { (m.tags || []).forEach(t => ts.add(t)) })
  return Array.from(ts).sort()
})

const sizeOptions = [
  { label: '全部', min: 0, max: 999 },
  { label: '<15G', min: 0, max: 15 },
  { label: '15-20G', min: 15, max: 20 },
  { label: '>20G', min: 20, max: 999 },
]

function parseGB(sh: string): number {
  const m = sh.match(/([\d.]+)\s*(GB|MB)/)
  if (!m) return 0
  const v = parseFloat(m[1])
  return m[2] === 'GB' ? v : v / 1024
}

const deployStatus = computed(() => {
  const running = models.value.find(m => m.is_running)
  return running?.deploy_config || currentConfig.value || null
})
const serviceRunning = computed(() => serverRunning.value || models.value.some(m => m.is_running))
const currentModelName = computed(() => {
  const r = models.value.find(m => m.is_running)
  return r ? (r.alias || r.name) : (currentModel.value || '未部署')
})
function fmtCtx(v: any): string {
  const n = parseInt(v)
  if (!n) return '-'
  if (n >= 1024) return (n / 1024).toFixed(0) + 'K'
  return String(n)
}
const cacheLabels: Record<string, string> = {
  f32: 'F32', f16: 'F16', bf16: 'BF16', q8_0: 'Q8_0', q4_0: 'Q4_0',
  iq4_nl: 'IQ4_NL', turbo2: 'TQ2', turbo3: 'TQ3', turbo4: 'TQ4'
}
function cacheLabel(k: string): string {
  return cacheLabels[k] || k || '-'
}

const filteredModels = computed(() => {
  return models.value.filter(m => {
    const s = search.value.toLowerCase()
    const matchSearch = !s || m.name.toLowerCase().includes(s) || (m.alias && m.alias.toLowerCase().includes(s))
    let matchArch = true
    if (filterArch.value) {
      const a = m.alias || m.name
      const f = filterArch.value
      if (f === 'Qwen3.6-27B') matchArch = a.includes('Qwopus3.6-27B') || a.startsWith('Qwen3.6-27B')
      else if (f === 'Qwen3.6-35B') matchArch = a.startsWith('Qwen3.6-35B')
      else if (f === 'Gemma4-26B') matchArch = a.startsWith('Gemma4-26B')
      else if (f === 'Gemma4-31B') matchArch = a.startsWith('Gemma4-31B')
      else if (f === 'Qwen3.5-9B') matchArch = a.startsWith('Qwen3.5-9B')
      else if (f === '视觉插件') matchArch = isMMProj(m)
    }
    const matchQuant = !filterQuant.value || m.quant_type === filterQuant.value
    const matchTags = activeTags.value.length === 0 || activeTags.value.every(t => (m.tags || []).includes(t))
    let matchSize = true
    if (filterSize.value) {
      const sr = sizeOptions.find(x => x.label === filterSize.value)
      if (sr) { const gb = parseGB(m.size_human); matchSize = gb >= sr.min && gb < sr.max }
    }
    return matchSearch && matchArch && matchQuant && matchTags && matchSize
  })
})

function isMMProj(m: Model): boolean {
  return m.quant_type === 'mmproj' || m.name.includes('mmproj')
}

function getAlias(name: string): string {
  const m = models.value.find(x => x.name === name)
  return m ? (m.alias || name) : name
}

function tagClass(t: string): string {
  const map: Record<string, string> = {
    MoE: 'tag-moe', Dense: 'tag-dense', MTP: 'tag-mtp',
    TurboQuant: 'tag-tq', MXFP4: 'tag-mxfp4',
    Uncensored: 'tag-uncensored', Reasoning: 'tag-reasoning',
  }
  return map[t] || 'tag-default'
}

function tagLabel(t: string): string {
  const map: Record<string, string> = {
    MoE: 'MoE', Dense: 'Dense', MTP: 'MTP',
    TurboQuant: 'TQ', MXFP4: 'MXFP4',
    Uncensored: 'Uncensored', Reasoning: 'Reasoning',
  }
  return map[t] || t
}

function toggleTag(tag: string) {
  const idx = activeTags.value.indexOf(tag)
  if (idx >= 0) activeTags.value.splice(idx, 1)
  else activeTags.value.push(tag)
}

function clearFilters() {
  search.value = ''; filterArch.value = ''; filterQuant.value = ''
  activeTags.value = []; filterSize.value = ''
}

async function refreshModels() {
  try {
    const res = await getModels()
    serverRunning.value = !!res.data.server_running
    currentConfig.value = normalizeDeployConfig(res.data.current_config || null)
    currentModel.value = res.data.current_model || basename(currentConfig.value?.model_path || currentConfig.value?.model || '')
    const currentPath = currentConfig.value?.model_path || ''
    models.value = (res.data.models || []).map((m: Model) => {
      const running = serverRunning.value && (
        m.name === currentModel.value ||
        m.path === currentPath ||
        (!!currentPath && currentPath.endsWith('/' + m.name))
      )
      return {
        ...m,
        is_running: running,
        deploy_config: running ? {
          ...(currentConfig.value || {}),
          model: m.name,
          model_path: currentPath || m.path,
          alias: m.alias || currentConfig.value?.alias,
        } : m.deploy_config,
      }
    })
  } catch (e) { console.error('加载失败:', e) }
}

function basename(path: string): string {
  return path ? path.split('/').filter(Boolean).pop() || '' : ''
}

function normalizeDeployConfig(cfg: any): any {
  if (!cfg) return null
  return {
    ...cfg,
    cache_type_k: cfg.cache_type_k ?? cfg.k_cache_type,
    cache_type_v: cfg.cache_type_v ?? cfg.v_cache_type,
    k_cache_type: cfg.k_cache_type ?? cfg.cache_type_k,
    v_cache_type: cfg.v_cache_type ?? cfg.cache_type_v,
    np: cfg.np ?? cfg.concurrency,
  }
}

function openDeploy(model: Model) {
  deployModel.value = model
  // Try to load saved prefs for this model
  const saved = getPrefsForModel(model.name)
  if (saved) {
    deployParams.value = { ...deployParams.value, ...saved }
    currentPreset.value = ''
  } else {
    // Reset to default
    deployParams.value = {
      ctx_size: 262144, ngl: 99, batch: 512, ubatch: 256,
      k_cache_type: 'turbo4', v_cache_type: 'turbo4',
      flash_attn: 'on', temp: 0.6, np: 2, reasoning: 'off',
      threads: 8, threads_http: 4,
      draft_k_cache: 'turbo2', draft_v_cache: 'turbo2',
      spec_draft_n_max: 2, chunked_batch: 'on',
    }
  }
  showDeployModal.value = true
}

function quickDeploy(modelName: string) {
  const m = models.value.find(x => x.name === modelName)
  if (m && !m.is_running) openDeploy(m)
}

function closeDeploy() {
  showDeployModal.value = false
  deploying.value = false
}

async function confirmDeploy() {
  deploying.value = true
  try {
    const params = {
      filename: deployModel.value.name,
      ctx_size: deployParams.value.ctx_size,
      ngl: deployParams.value.ngl,
      batch: deployParams.value.batch,
      ubatch: deployParams.value.ubatch,
      k_cache_type: deployParams.value.k_cache_type,
      v_cache_type: deployParams.value.v_cache_type,
      flash_attn: deployParams.value.flash_attn,
      temp: deployParams.value.temp,
      np: deployParams.value.np,
      reasoning: deployParams.value.reasoning,
      threads: deployParams.value.threads,
      threads_http: deployParams.value.threads_http,
      draft_k_cache: deployParams.value.draft_k_cache,
      draft_v_cache: deployParams.value.draft_v_cache,
      spec_draft_n_max: deployParams.value.spec_draft_n_max,
      chunked_batch: deployParams.value.chunked_batch,
      split_mode: deployParams.value.split_mode || undefined,
      fit: deployParams.value.fit || undefined,
    }
    const res = await apiDeployModel(params)
    alert(res.data.message || '部署请求已发送，请稍候...')

    // Save prefs if checked
    if (savePrefs.value) {
      savePrefsForModel(deployModel.value.name, params)
    }
    // Add to recent
    addRecentModel(deployModel.value.name)

    closeDeploy()
    refreshModels()
  } catch (e: any) {
    alert('部署失败: ' + (e.response?.data?.message || e.message))
  } finally {
    deploying.value = false
  }
}

// Quick edit parameter from status bar
const paramDefs: Record<string, any> = {
  ctx_size: { label: '上下文大小', type: 'number', min: 4096, max: 524288 },
  ngl: { label: 'GPU 层数', type: 'number', min: 1, max: 99 },
  np: { label: '并发数', type: 'select', options: [1, 2, 4, 8] },
  k_cache_type: { label: 'K Cache', type: 'select', options: ['f32','f16','q8_0','q4_0','iq4_nl','turbo4'] },
  v_cache_type: { label: 'V Cache', type: 'select', options: ['f32','f16','q8_0','q4_0','iq4_nl','turbo4'] },
  batch: { label: 'Batch', type: 'number', min: 64, max: 4096 },
  ubatch: { label: 'UBatch', type: 'number', min: 64, max: 1024 },
  flash_attn: { label: 'Flash Attn', type: 'toggle' },
  threads: { label: 'CPU 线程', type: 'number', min: 1, max: 32 },
  threads_http: { label: 'HTTP 线程', type: 'number', min: 1, max: 16 },
  temp: { label: '温度', type: 'number', min: 0, max: 2 },
  reasoning: { label: 'Reasoning', type: 'toggle' },
  split_mode: { label: 'Split Mode', type: 'select', options: ['', 'none', 'layer', 'row'] },
  fit: { label: 'Fit', type: 'toggle' },
  spec_draft_n_max: { label: 'Draft 预测数', type: 'number', min: 1, max: 5 },
}

async function quickEditParam(paramName: string) {
  if (!serviceRunning.value || !currentModel.value) return
  const def = paramDefs[paramName]
  if (!def) return
  const cfg = deployStatus.value || currentConfig.value
  if (!cfg) return
  const cur = cfg[paramName]
  let newVal: any = cur

  if (def.type === 'toggle') {
    newVal = (cur === 'on' || cur === true) ? 'off' : 'on'
  } else if (def.type === 'select') {
    const input = prompt(`${def.label} (当前: ${cur})
可选值: ${def.options.join(', ')}`, cur)
    if (input === null) return
    newVal = input.trim()
  } else if (def.type === 'number') {
    const input = prompt(`${def.label} (当前: ${cur}, 范围 ${def.min}~${def.max}):`, cur)
    if (input === null) return
    newVal = parseFloat(input)
    if (isNaN(newVal)) return
    if (newVal < def.min) newVal = def.min
    if (newVal > def.max) newVal = def.max
  }
  if (newVal === cur) return

  // Build payload with all current params + the change
  const payload: any = {
    filename: cfg.model_path?.split('/').pop() || cfg.model?.split('/').pop() || '',
    ctx_size: cfg.ctx_size, ngl: cfg.ngl,
    batch: cfg.batch, ubatch: cfg.ubatch,
    k_cache_type: cfg.k_cache_type || cfg.cache_type_k,
    v_cache_type: cfg.v_cache_type || cfg.cache_type_v,
    flash_attn: cfg.flash_attn === 'on' || cfg.flash_attn === true,
    temp: cfg.temp, np: cfg.np || cfg.concurrency,
    reasoning: cfg.reasoning || 'off',
    threads: cfg.threads, threads_http: cfg.threads_http,
    draft_k_cache: cfg.draft_k_cache_type || 'turbo2',
    draft_v_cache: cfg.draft_v_cache_type || 'turbo2',
    spec_draft_n_max: cfg.spec_draft_n_max || 0,
    chunked_batch: cfg.chunked_batch === 'on' || cfg.chunked_batch === true,
    split_mode: cfg.split_mode || undefined,
    fit: cfg.fit || undefined,
  }
  payload[paramName] = newVal

  try {
    await apiDeployModel(payload)
    alert(`${def.label} 已调整为 ${newVal}，模型重启中...`)
    setTimeout(() => refreshModels(), 12000)
  } catch (e: any) {
    alert('调整失败: ' + (e.response?.data?.message || e.message))
  }
}

onMounted(() => { loadPrefs(); refreshModels() })
</script>

<style scoped>
.deploy-status { background: var(--bg-hover); border-radius: 8px; padding: 12px 16px; margin-bottom: 12px; border: 1px solid var(--border-color); }
.ds-header { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.ds-badge { font-size: 0.75rem; font-weight: 600; padding: 2px 8px; border-radius: 4px; }
.ds-badge.running { background: rgba(16,185,129,0.2); color: #10b981; }
.ds-badge.stopped { background: rgba(107,114,128,0.2); color: #6b7280; }
.ds-model { font-weight: 700; font-size: 0.9rem; color: var(--text-primary); }
.ds-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
@media (min-width: 768px) { .ds-grid { grid-template-columns: repeat(4, 1fr); } }
@media (min-width: 1024px) { .ds-grid { grid-template-columns: repeat(6, 1fr); } }
.ds-item { display: flex; flex-direction: column; gap: 2px; padding: 6px 8px; background: var(--bg-active); border-radius: 4px; }
.ds-clickable { cursor: pointer; transition: all 0.15s; }
.ds-clickable:hover { background: var(--accent-color); color: white; transform: scale(1.05); }
.ds-clickable:hover .ds-label { color: rgba(255,255,255,0.8); }
.ds-clickable:hover .ds-val { color: white; }
.ds-label { font-size: 0.65rem; color: var(--text-muted); }
.ds-val { font-size: 0.8rem; font-weight: 600; color: var(--text-primary); }
.header-actions { display: flex; align-items: center; gap: 12px; }
.model-count { color: var(--text-secondary); font-size: 0.8rem; }
.btn-refresh { background: none; border: none; font-size: 1.2rem; cursor: pointer; color: var(--text-secondary); padding: 4px 8px; border-radius: 4px; }
.btn-refresh:hover { background: var(--bg-hover); }
.filter-bar { margin-bottom: 16px; }
.search-box { position: relative; margin-bottom: 12px; }
.search-icon { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); font-size: 0.9rem; }
.search-input { width: 100%; padding: 8px 12px 8px 32px; background: var(--bg-hover); border: 1px solid var(--border-color); border-radius: 8px; color: var(--text-primary); font-size: 0.85rem; }
.search-input:focus { outline: none; border-color: var(--accent-color); }
.filter-chips { display: flex; flex-direction: column; gap: 8px; }
.chip-group { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.chip-label { font-size: 0.7rem; color: var(--text-muted); min-width: 40px; }
.chip { padding: 3px 10px; border-radius: 12px; border: 1px solid var(--border-color); background: var(--bg-hover); color: var(--text-secondary); font-size: 0.72rem; cursor: pointer; transition: all 0.15s; }
.chip:hover { border-color: var(--accent-color); color: var(--text-primary); }
.chip.active { background: var(--accent-color); color: white; border-color: var(--accent-color); }

/* Quick Switch */
.quick-switch-bar { display: flex; gap: 16px; padding: 10px 14px; background: var(--bg-hover); border-radius: 8px; margin-bottom: 12px; flex-wrap: wrap; align-items: center; }
.qs-section { display: flex; align-items: center; gap: 6px; }
.qs-label { font-size: 0.75rem; color: var(--text-muted); }
.qs-btn { padding: 3px 10px; border-radius: 12px; border: 1px solid var(--border-color); background: var(--bg-active); color: var(--text-primary); font-size: 0.72rem; cursor: pointer; transition: all 0.15s; }
.qs-btn:hover { border-color: var(--accent-color); background: var(--accent-color); color: white; }
.qs-recent { opacity: 0.8; }

/* Model Cards */
.model-list { display: flex; flex-direction: column; gap: 6px; max-height: calc(100vh - 420px); overflow-y: auto; padding-right: 4px; }
.model-card { display: flex; justify-content: space-between; align-items: flex-start; padding: 10px 12px; background: var(--bg-hover); border-radius: 8px; border-left: 3px solid transparent; transition: all 0.15s; }
.model-card:hover { background: var(--bg-active); }
.model-card.model-running { border-left-color: var(--success-color); background: rgba(16,185,129,0.05); }
.model-main { flex: 1; min-width: 0; }
.model-title-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 2px; }
.running-badge { background: var(--success-color); color: white; padding: 1px 6px; border-radius: 4px; font-size: 0.6rem; font-weight: 700; }
.model-alias { font-weight: 600; font-size: 0.9rem; cursor: pointer; }
.model-alias:hover { color: var(--accent-color); }
.model-size-badge { background: var(--bg-active); padding: 1px 8px; border-radius: 4px; font-size: 0.7rem; color: var(--text-secondary); }
.model-filename { font-family: monospace; font-size: 0.72rem; color: var(--text-muted); margin-bottom: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.model-tags { display: flex; gap: 4px; flex-wrap: wrap; }
.tag { padding: 1px 7px; border-radius: 4px; font-size: 0.62rem; font-weight: 600; }
.tag-quant { background: rgba(99,102,241,0.2); color: #818cf8; }
.tag-moe { background: rgba(168,85,247,0.2); color: #c084fc; }
.tag-dense { background: rgba(34,211,238,0.2); color: #22d3ee; }
.tag-mtp { background: rgba(251,191,36,0.2); color: #fbbf24; }
.tag-tq { background: rgba(236,72,153,0.2); color: #ec4899; }
.tag-mxfp4 { background: rgba(245,158,11,0.2); color: #f59e0b; }
.tag-uncensored { background: rgba(239,68,68,0.2); color: #ef4444; }
.tag-reasoning { background: rgba(16,185,129,0.2); color: #10b981; }
.tag-mmproj { background: rgba(107,114,128,0.2); color: #9ca3af; }
.tag-default { background: var(--bg-active); color: var(--text-secondary); }
.deploy-prefs-summary { display: flex; gap: 6px; margin-top: 4px; }
.pref-tag { font-size: 0.65rem; background: var(--bg-active); padding: 1px 6px; border-radius: 3px; color: var(--text-muted); }
.model-actions { display: flex; gap: 6px; flex-shrink: 0; margin-left: 8px; align-items: center; }
.btn-deploy { padding: 5px 14px; background: var(--accent-color); color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 0.78rem; font-weight: 500; }
.btn-deploy:hover { opacity: 0.85; }
.btn-running { padding: 5px 14px; background: rgba(16,185,129,0.2); color: var(--success-color); border: none; border-radius: 6px; font-size: 0.78rem; font-weight: 500; cursor: default; }
.btn-fav { background: none; border: none; font-size: 1.1rem; cursor: pointer; padding: 2px 4px; opacity: 0.5; }
.btn-fav:hover { opacity: 1; }
.btn-fav.active { opacity: 1; }
.empty-state { text-align: center; padding: 40px 0; color: var(--text-muted); }
.btn-clear { margin-top: 8px; padding: 6px 16px; background: var(--bg-active); border: 1px solid var(--border-color); border-radius: 6px; color: var(--text-primary); cursor: pointer; font-size: 0.8rem; }

/* Modal */
.modal-overlay { position: fixed; top: 0; left: 0; right: 0; bottom: 0; background: rgba(0,0,0,0.7); display: flex; align-items: center; justify-content: center; z-index: 1000; padding: 16px; }
.modal-content { background: var(--bg-secondary); border-radius: 12px; max-width: 700px; width: 100%; max-height: 90vh; overflow-y: auto; box-shadow: 0 20px 60px rgba(0,0,0,0.5); }
.modal-header { display: flex; justify-content: space-between; align-items: center; padding: 16px 20px; border-bottom: 1px solid var(--border-color); }
.modal-header h3 { margin: 0; font-size: 1rem; }
.btn-close { background: none; border: none; color: var(--text-secondary); font-size: 1.2rem; cursor: pointer; padding: 4px; }
.modal-body { padding: 16px 20px; }
.deploy-model-info { margin-bottom: 12px; padding: 12px; background: var(--bg-hover); border-radius: 8px; }
.deploy-name { font-weight: 700; font-size: 0.95rem; }
.deploy-file { display: block; font-family: monospace; font-size: 0.72rem; color: var(--text-muted); margin-top: 2px; }
.deploy-size { display: inline-block; margin-top: 4px; background: var(--bg-active); padding: 2px 8px; border-radius: 4px; font-size: 0.75rem; color: var(--text-secondary); }

/* Presets */
.preset-bar { display: flex; gap: 6px; align-items: center; margin-bottom: 14px; flex-wrap: wrap; }
.preset-label { font-size: 0.75rem; color: var(--text-muted); }
.preset-btn { padding: 3px 10px; border-radius: 12px; border: 1px solid var(--border-color); background: var(--bg-hover); color: var(--text-secondary); font-size: 0.72rem; cursor: pointer; }
.preset-btn:hover { border-color: var(--accent-color); }
.preset-btn.active { background: var(--accent-color); color: white; border-color: var(--accent-color); }
.preset-saved { color: var(--warning-color); border-color: var(--warning-color); }

/* Params Grid */
.params-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; margin-bottom: 20px; }
@media (min-width: 768px) { .params-grid { grid-template-columns: repeat(3, 1fr); } }
.param-item { display: flex; flex-direction: column; gap: 4px; }
.param-item label { font-size: 0.72rem; color: var(--text-secondary); }
.param-item select, .param-item input { padding: 6px 8px; background: var(--bg-hover); border: 1px solid var(--border-color); border-radius: 6px; color: var(--text-primary); font-size: 0.82rem; }
.param-item select:focus, .param-item input:focus { outline: none; border-color: var(--accent-color); }
.deploy-actions { display: flex; align-items: center; justify-content: flex-end; gap: 12px; }
.save-prefs-check { display: flex; align-items: center; gap: 4px; font-size: 0.8rem; color: var(--text-secondary); cursor: pointer; margin-right: auto; }
.btn-cancel { padding: 8px 20px; background: var(--bg-hover); border: 1px solid var(--border-color); border-radius: 6px; color: var(--text-primary); cursor: pointer; font-size: 0.85rem; }
.btn-confirm { padding: 8px 24px; background: var(--accent-color); color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 0.85rem; font-weight: 600; }
.btn-confirm:disabled { opacity: 0.5; cursor: not-allowed; }
@media (max-width: 768px) { .model-card { flex-direction: column; gap: 8px; } .model-actions { align-self: flex-end; } }
</style>
