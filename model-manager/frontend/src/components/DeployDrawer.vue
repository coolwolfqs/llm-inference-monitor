<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { CheckCircle2, ChevronDown, Rocket, XCircle } from 'lucide-vue-next'
import { api } from '../services/api'
import type { DeployPayload, Engine, EngineParameter, ModelArtifact, Preflight, ProjectionArtifact, RuntimeConfig } from '../types/model'

const props = defineProps<{ model: ModelArtifact; currentConfig: RuntimeConfig }>()
const emit = defineEmits<{ close: []; deployed: [taskId: string] }>()
const preflight = ref<Preflight>()
const engines = ref<Engine[]>([])
const projectors = ref<ProjectionArtifact[]>([])
const draftModels = computed(() => preflight.value?.draft_models || [])
const loading = ref(true)
const submitting = ref(false)
const error = ref('')
const supportsMtp = props.model.tags.includes('MTP')
  || (props.model.classification?.capabilities || []).some((item) => item.toLowerCase() === 'mtp')
const contextOptions = [32768, 65536, 131072, 196608, 262144, 524288, 1048576]
const fallbackCacheTypes = ['f32', 'f16', 'bf16', 'q8_0', 'q4_0', 'q4_1', 'iq4_nl', 'q5_0', 'q5_1']
// MTP is selected by model recognition, while n-gram stays opt-in. Both
// controls remain visible so the user can see and change the effective mode.
const mtpEnabled = ref(supportsMtp)
const ngramEnabled = ref(false)
const profileId = ref('default')
const parameterValues = reactive<Record<string, unknown>>({})

const form = reactive<DeployPayload>({
  filename: props.model.id,
  ctx_size: props.model.ctx_default || 131072,
  ngl: props.currentConfig.ngl || 99,
  gpu: props.currentConfig.gpu || 'all',
  concurrency: props.currentConfig.concurrency || 1,
  k_cache_type: props.currentConfig.k_cache_type || 'q8_0',
  v_cache_type: props.currentConfig.v_cache_type || 'q8_0',
  batch: props.currentConfig.batch || 1024,
  ubatch: props.currentConfig.ubatch || 512,
  flash_attn: props.currentConfig.flash_attn ?? true,
  chunked_batch: true,
  threads: props.currentConfig.threads || 8,
  threads_http: 4,
  temp: props.currentConfig.temp ?? 0.7,
  engine: 'llama',
  llama_version: '',
  reasoning: 'off',
  ui: false,
  mmproj: props.currentConfig.mmproj || false,
  mmproj_file: props.currentConfig.mmproj_file || '',
  draft_model_id: '',
  spec_type: 'none',
  spec_draft_n_max: props.currentConfig.spec_draft_n_max || 1,
  draft_k_cache_type: props.currentConfig.draft_k_cache_type || 'q8_0',
  draft_v_cache_type: props.currentConfig.draft_v_cache_type || 'q8_0',
  ngram_mod_n_min: props.currentConfig.ngram_mod_n_min || 8,
  ngram_mod_n_max: props.currentConfig.ngram_mod_n_max || 32,
  ngram_mod_n_match: props.currentConfig.ngram_mod_n_match || 16,
  cache_ram: props.currentConfig.cache_ram || 2048,
  sleep_idle_seconds: props.currentConfig.sleep_idle_seconds || 300,
  device: props.currentConfig.device || '',
  fit: props.currentConfig.fit || '',
  kv_unified: props.currentConfig.kv_unified || false,
  cache_reuse: props.currentConfig.cache_reuse ?? undefined,
  spec_draft_p_min: props.currentConfig.spec_draft_p_min ?? undefined,
})

const selectedEngine = computed(() => engines.value.find((item) => item.key === form.llama_version))
const engineSupportsMtp = computed(() => Boolean(
  selectedEngine.value?.supports_mtp
  ?? selectedEngine.value?.version_params?.spec_draft_n_max,
))
const mtpAvailable = computed(() => supportsMtp && engineSupportsMtp.value)
const engineSupportsDraft = computed(() => Boolean(selectedEngine.value?.supports_draft_model))
const draftAvailable = computed(() => engineSupportsDraft.value && draftModels.value.length > 0)
const showDraftCache = computed(() => Boolean(mtpEnabled.value || form.draft_model_id))
const cacheTypes = computed(() => {
  const values = selectedEngine.value?.cache_types
  return values?.length ? values : fallbackCacheTypes
})
const draftCacheTypes = computed(() => {
  const values = selectedEngine.value?.draft_cache_types
  return values?.length ? values : cacheTypes.value
})
const engineParameterSchema = computed(() => selectedEngine.value?.deployment_parameters || selectedEngine.value?.parameter_schema || [])
const engineRecommendedParams = computed(() => selectedEngine.value?.recommended_params || {})
const engineDifferences = computed(() => Object.entries(selectedEngine.value?.parameter_differences || {}))
const supportedEngineParameterCount = computed(() => engineParameterSchema.value.filter((parameter) => parameter.supported !== false).length)
const engineProfiles = computed(() => selectedEngine.value?.profiles || {})
const profileOptions = computed(() => Object.entries(engineProfiles.value))

const groupParameters = (predicate: (parameter: EngineParameter) => boolean) => {
  const groups = new Map<string, EngineParameter[]>()
  for (const parameter of engineParameterSchema.value) {
    if (!predicate(parameter) || !isParameterVisible(parameter)) continue
    const group = parameter.group || '引擎参数'
    const values = groups.get(group) || []
    values.push(parameter)
    groups.set(group, values)
  }
  return Array.from(groups.entries()).map(([name, parameters]) => ({ name, parameters }))
}

// Common non-managed parameters belong to the shared advanced section. Only
// descriptors explicitly marked common:false are rendered as extensions.
const advancedParameterGroups = computed(() => groupParameters((parameter) => (
  parameter.supported !== false && !parameter.managed && parameter.common !== false
)))
const extensionParameterGroups = computed(() => groupParameters((parameter) => (
  parameter.supported !== false && !parameter.managed && parameter.common === false
)))

const speculationMode = computed<'none' | 'mtp' | 'ngram' | 'mtp_ngram'>({
  get() {
    if (mtpEnabled.value && ngramEnabled.value) return 'mtp_ngram'
    if (mtpEnabled.value) return 'mtp'
    if (ngramEnabled.value) return 'ngram'
    return 'none'
  },
  set(value) {
    mtpEnabled.value = value === 'mtp' || value === 'mtp_ngram'
    ngramEnabled.value = value === 'ngram' || value === 'mtp_ngram'
  },
})
const speculationModeOptions = computed(() => {
  const options: Array<{ value: 'none' | 'mtp' | 'ngram' | 'mtp_ngram'; label: string }> = [
    { value: 'none', label: '关闭投机解码' },
  ]
  if (mtpAvailable.value) options.push({ value: 'mtp', label: 'MTP（模型内置草稿）' })
  if (selectedEngine.value?.supports_ngram !== false) options.push({ value: 'ngram', label: 'n-gram' })
  if (mtpAvailable.value && selectedEngine.value?.supports_ngram !== false) {
    options.push({ value: 'mtp_ngram', label: 'MTP + n-gram' })
  }
  return options
})

function supportsEngineParameter(key: string) {
  return engineParameterSchema.value.some((item) => item.key === key && item.supported !== false)
}

function engineParameter(key: string) {
  return engineParameterSchema.value.find((item) => item.key === key)
}

function isParameterVisible(parameter: EngineParameter) {
  const visible = parameter.visible_when
  if (!visible || typeof visible !== 'object') return true
  return Object.entries(visible).every(([key, expected]) => {
    const actual = parameterValues[key]
    return Array.isArray(expected) ? expected.includes(actual) : actual === expected
  })
}

function inputType(parameter: EngineParameter) {
  if (parameter.type === 'integer' || parameter.type === 'number') return 'number'
  return 'text'
}

function parameterHint(parameter: EngineParameter) {
  const pieces = [parameter.description]
  if (parameter.flag) pieces.push(parameter.flag)
  if (parameter.env) pieces.push(`环境变量 ${parameter.env}`)
  return pieces.filter(Boolean).join(' · ')
}

function recommendedValue(parameter: EngineParameter) {
  const value = engineRecommendedParams.value[parameter.key]
  if (value === undefined || value === null || value === '') return '通用默认'
  if (typeof value === 'boolean') return value ? '开启' : '关闭'
  return String(value)
}

function applyEngineProfile() {
  // Clear branch-only values when returning to an older engine. This prevents
  // an option from being accidentally sent to a binary that did not advertise
  // it in VERSION.json.
  form.device = ''
  form.fit = ''
  form.kv_unified = false
  form.cache_reuse = undefined
  form.spec_draft_p_min = undefined

  const profile = engineProfiles.value[profileId.value]
  const profileValues = profile?.parameters || profile?.values || {}
  const recommended = { ...engineRecommendedParams.value, ...profileValues }
  form.profile_id = profileId.value
  if (typeof recommended.ctx_size === 'number') form.ctx_size = recommended.ctx_size
  if (typeof recommended.ngl === 'number') form.ngl = recommended.ngl
  if (typeof recommended.device === 'string') form.device = recommended.device
  if (recommended.fit === 'on' || recommended.fit === 'off') form.fit = recommended.fit
  if (typeof recommended.kv_unified === 'boolean') form.kv_unified = recommended.kv_unified
  if (typeof recommended.cache_reuse === 'number') form.cache_reuse = recommended.cache_reuse
  if (typeof recommended.spec_draft_p_min === 'number') form.spec_draft_p_min = recommended.spec_draft_p_min
  if (typeof recommended.batch === 'number') form.batch = recommended.batch
  if (typeof recommended.ubatch === 'number') form.ubatch = recommended.ubatch
  if (typeof recommended.concurrency === 'number') form.concurrency = recommended.concurrency
  if (typeof recommended.spec_draft_n_max === 'number') form.spec_draft_n_max = recommended.spec_draft_n_max
  if (typeof recommended.threads === 'number') form.threads = recommended.threads
  if (typeof recommended.temp === 'number') form.temp = recommended.temp
  if (typeof recommended.k_cache_type === 'string') form.k_cache_type = recommended.k_cache_type
  if (typeof recommended.v_cache_type === 'string') form.v_cache_type = recommended.v_cache_type
  for (const parameter of engineParameterSchema.value) {
    if (parameter.supported === false || parameter.managed) continue
    const profileValue = profileValues[parameter.key]
    const currentValue = props.currentConfig.parameters?.[parameter.key]
    const recommendedValue = engineRecommendedParams.value[parameter.key]
    const value = profileValue ?? currentValue ?? recommendedValue ?? parameter.default
    if (value !== undefined && value !== null) parameterValues[parameter.key] = value
    else delete parameterValues[parameter.key]
  }
}

function projectorForValue(value: string) {
  const wanted = String(value || '').trim()
  if (!wanted) return undefined
  return projectors.value.find((item) =>
    item.id === wanted
    || item.name === wanted
    || item.path === wanted
    || item.relative_path === wanted,
  )
}

function normalizeProjectorSelection(companions: ProjectionArtifact[]) {
  const inherited = String(form.mmproj_file || '').trim()
  const validInherited = companions.find((item) =>
    item.id === inherited
    || item.name === inherited
    || item.path === inherited
    || item.relative_path === inherited,
  )
  // Keep the previous selection only when its projector belongs to the new
  // model bundle. Otherwise default to the first projector in this bundle;
  // never carry a previous model's path across model drawers.
  const defaultProjector = validInherited || companions[0]
  form.mmproj_file = defaultProjector?.id || ''
  form.mmproj = Boolean(form.mmproj_file)
}

function keepCacheSelection() {
  const targetDefault = cacheTypes.value.includes('q8_0') ? 'q8_0' : (cacheTypes.value[0] || 'q8_0')
  const draftDefault = draftCacheTypes.value.includes('q8_0') ? 'q8_0' : (draftCacheTypes.value[0] || 'q8_0')
  if (!cacheTypes.value.includes(form.k_cache_type)) form.k_cache_type = targetDefault
  if (!cacheTypes.value.includes(form.v_cache_type)) form.v_cache_type = targetDefault
  if (!draftCacheTypes.value.includes(form.draft_k_cache_type)) form.draft_k_cache_type = draftDefault
  if (!draftCacheTypes.value.includes(form.draft_v_cache_type)) form.draft_v_cache_type = draftDefault
}

watch(engineSupportsMtp, (available) => {
  if (!available) mtpEnabled.value = false
})

watch(() => selectedEngine.value?.supports_ngram, (available) => {
  if (available === false) ngramEnabled.value = false
})

watch(() => form.llama_version, (key) => {
  if (key) window.localStorage.setItem('model-manager:last-engine', key)
  if (key && engines.value.length) {
    profileId.value = 'default'
    applyEngineProfile()
  }
  keepCacheSelection()
})

watch(profileId, () => applyEngineProfile())

watch(mtpEnabled, (enabled) => {
  if (enabled) form.draft_model_id = ''
})

watch(() => form.draft_model_id, (value) => {
  if (value) mtpEnabled.value = false
})

onMounted(async () => {
  try {
    const [check, available, companions] = await Promise.all([api.preflight(props.model.id), api.engines(), api.projectors(props.model.id)])
    preflight.value = check
    engines.value = available.filter((item) => item.type !== 'vllm')
    projectors.value = companions
    normalizeProjectorSelection(companions)
    form.engine = check.default_engine || check.compatible_engines[0] || 'llama'
    const rememberedEngine = window.localStorage.getItem('model-manager:last-engine')
    const engineCandidates = [
      rememberedEngine,
      props.currentConfig.llama_version,
      check.default_llama_version,
      ...(check.compatible_llama_versions || []),
      engines.value.find((item) => item.key === 'vulkan')?.key,
      engines.value[0]?.key,
    ].filter((key): key is string => Boolean(key))
    form.llama_version = engineCandidates.find((key) => engines.value.some((item) => item.key === key)) || ''
    profileId.value = 'default'
    applyEngineProfile()
    keepCacheSelection()
    // The computed value is initially false while the engine list is loading,
    // so explicitly clear a previously saved MTP selection once the actual
    // selected engine is known.
    if (!engineSupportsMtp.value) mtpEnabled.value = false
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '部署预检失败'
  } finally {
    loading.value = false
  }
})

async function submit() {
  submitting.value = true
  error.value = ''
  try {
    // Keep the client defensive if an engine is changed after opening the
    // drawer or if its capability metadata is stale.
    const useMtp = mtpEnabled.value && mtpAvailable.value
    form.spec_type = useMtp && ngramEnabled.value ? 'draft-mtp,ngram-mod' : useMtp ? 'draft-mtp' : ngramEnabled.value ? 'ngram-mod' : 'none'
    form.spec_draft_n_max = useMtp ? Math.max(1, form.spec_draft_n_max) : 0
    if (form.draft_model_id) form.spec_draft_n_max = Math.max(1, form.spec_draft_n_max)
    // Submit only the canonical id of a projector offered for this model.
    // This is a second defensive boundary against stale runtime config.
    const selectedProjector = projectorForValue(form.mmproj_file)
    form.mmproj_file = selectedProjector?.id || ''
    form.mmproj = Boolean(selectedProjector)
    form.parameters = Object.fromEntries(
      Object.entries(parameterValues).filter(([key, value]) => {
        const parameter = engineParameter(key)
        return parameter && parameter.supported !== false && !parameter.managed && value !== undefined
      }),
    )
    const check = await api.preflight(props.model.id)
    if (!check.can_deploy) throw new Error(check.blockers.join('；') || '部署预检未通过')
    const task = await api.deploy(form)
    emit('deployed', task.task_id)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '部署失败'
    submitting.value = false
  }
}
</script>

<template>
  <div class="drawer-backdrop" @click.self="emit('close')">
    <aside class="drawer" aria-modal="true" role="dialog" aria-labelledby="deploy-title">
      <header class="drawer-header">
        <div><span class="eyebrow">部署配置</span><h2 id="deploy-title">{{ model.family || model.alias }}</h2><p>{{ model.name }}</p></div>
        <button class="icon-button" aria-label="关闭" @click="emit('close')">×</button>
      </header>
      <div class="drawer-content">
        <div v-if="loading" class="skeleton-block">正在执行兼容性预检…</div>
        <div v-else-if="preflight" class="preflight" :class="preflight.can_deploy ? 'ready' : 'blocked'">
          <CheckCircle2 v-if="preflight.can_deploy" :size="19" />
          <XCircle v-else :size="19" />
          <div><strong>{{ preflight.can_deploy ? '预检通过' : '当前不可部署' }}</strong><p>{{ preflight.can_deploy ? `兼容引擎：${preflight.compatible_engines.join(', ')}` : preflight.blockers.join('；') }}</p></div>
        </div>

        <section class="form-section">
          <h3>1. 引擎选择</h3>
          <label class="full-field"><span>llama.cpp 版本</span><select v-model="form.llama_version"><option v-for="engine in engines" :key="engine.key" :value="engine.key">{{ engine.name }} · {{ engine.version }}</option></select></label>
          <label v-if="profileOptions.length" class="full-field"><span>部署 profile</span><select v-model="profileId"><option v-for="([key, profile]) in profileOptions" :key="key" :value="key">{{ profile.label || key }}</option></select><small>profile 只覆盖推荐值，仍可继续调整公共参数。</small></label>
          <div v-if="selectedEngine" class="feature-row"><span v-for="feature in selectedEngine.features" :key="feature">{{ feature }}</span></div>
          <div v-if="selectedEngine" class="engine-profile">
            <div class="engine-profile-title">{{ selectedEngine.branch ? `分支：${selectedEngine.branch}` : '通用 llama.cpp 引擎' }}<span v-if="selectedEngine.gpu_targets?.length"> · {{ selectedEngine.gpu_targets.join(', ') }}</span></div>
            <p v-if="selectedEngine.backend || selectedEngine.driver">后端：{{ selectedEngine.backend || 'llama' }}<span v-if="selectedEngine.driver"> · 驱动：{{ selectedEngine.driver }}</span></p>
            <p v-if="selectedEngine.binary_path">二进制：<code>{{ selectedEngine.binary_path }}</code></p>
            <p v-if="selectedEngine.parameter_file">参数来源：<code>{{ selectedEngine.parameter_file }}</code></p>
            <p v-if="selectedEngine.load_strategy?.default">加载策略：{{ selectedEngine.load_strategy.default.load_mode || '默认' }}<span v-if="selectedEngine.load_strategy.default.fit"> · Fit {{ selectedEngine.load_strategy.default.fit }}</span><span v-if="selectedEngine.load_strategy.default.kv_cache"> · KV {{ selectedEngine.load_strategy.default.kv_cache }}</span></p>
            <p v-for="note in selectedEngine.parameter_notes" :key="note">{{ note }}</p>
            <p>统一参数 {{ supportedEngineParameterCount }} 项；独占参数 {{ selectedEngine.exclusive_parameters?.length || 0 }} 项。</p>
            <p v-if="!engineDifferences.length && !selectedEngine.exclusive_parameters?.length">该引擎没有额外差异，使用统一部署参数。</p>
            <div v-if="engineDifferences.length" class="engine-parameter-list">
              <span v-for="([key, difference]) in engineDifferences" :key="key" :title="difference.reason">{{ key }}：{{ difference.recommended }}</span>
            </div>
            <div v-if="selectedEngine.exclusive_parameters?.length" class="engine-parameter-list">
              <span v-for="key in selectedEngine.exclusive_parameters" :key="key">独占：{{ key }}</span>
            </div>
          </div>
        </section>

        <section class="form-section">
          <h3>2. 基础配置</h3>
          <div class="form-grid">
            <label><span>上下文大小</span><select v-model.number="form.ctx_size"><option v-for="size in contextOptions" :key="size" :value="size">{{ size >= 1048576 ? '1024K' : `${size / 1024}K` }}</option></select></label>
            <label><span>并发数</span><select v-model.number="form.concurrency"><option v-for="n in 4" :key="n" :value="n">{{ n }}</option></select></label>
            <label><span>GPU 层数</span><input v-model.number="form.ngl" type="number" min="1" max="99" /></label>
            <label><span>GPU</span><input v-model="form.gpu" /></label>
          </div>
          <label class="full-field"><span>视觉模型</span><select v-model="form.mmproj_file"><option value="">不加载视觉模型</option><option v-for="projector in projectors" :key="projector.id" :value="projector.id">{{ projector.name }} · {{ Math.round(projector.size / 1024 / 1024) }}MB</option></select><small>{{ projectors.length ? '仅展示与当前主模型同目录匹配的视觉模型；默认使用该目录首个匹配项，也可明确选择不加载' : '当前模型目录没有可匹配的视觉模型，默认不加载' }}</small></label>
          <div class="form-grid enhancement-params">
            <label><span>投机解码方式</span><select v-model="speculationMode"><option v-for="option in speculationModeOptions" :key="option.value" :value="option.value">{{ option.label }}</option></select><small>{{ supportsMtp ? '已识别 MTP 模型，默认优先 MTP' : '未识别 MTP，默认关闭；可按需选择 n-gram' }}</small></label>
            <label v-if="mtpEnabled || form.draft_model_id"><span>预测 token 数</span><input v-model.number="form.spec_draft_n_max" type="number" min="1" max="32" /></label>
            <label v-if="(mtpEnabled || form.draft_model_id) && supportsEngineParameter('spec_draft_p_min')"><span>接受阈值</span><input v-model.number="form.spec_draft_p_min" type="number" min="0" max="1" step="0.01" /><small>当前引擎推荐 {{ recommendedValue({ key: 'spec_draft_p_min' }) }}</small></label>
            <label v-if="ngramEnabled"><span>n-gram 匹配长度</span><input v-model.number="form.ngram_mod_n_match" type="number" min="1" max="256" /></label>
          </div>
          <label v-if="draftModels.length || engineSupportsDraft" class="full-field draft-model-field"><span>草稿模型</span><select v-model="form.draft_model_id" :disabled="!draftAvailable || mtpEnabled"><option value="">不使用外置草稿模型</option><option v-for="draft in draftModels" :key="draft.id" :value="draft.id">{{ draft.name }} · {{ draft.size_human }}</option></select><small>{{ mtpEnabled ? '当前使用 MTP 内置草稿层；如需外置草稿模型，请将投机解码方式切换为 n-gram 或关闭' : draftAvailable ? '仅展示与当前主模型同目录或同族的草稿模型' : '当前模型包未发现可用草稿模型，或所选引擎不支持外置草稿模型' }}</small></label>
        </section>

        <details class="advanced-panel">
          <summary>3. 高级参数（共性） <ChevronDown :size="16" /></summary>
          <p class="parameter-panel-help">这里仅放所有引擎通用的性能、KV Cache、加载和服务参数；不支持的字段会自动隐藏。</p>
          <div class="form-grid advanced-grid">
            <label><span>K Cache</span><select v-model="form.k_cache_type"><option v-for="v in cacheTypes" :key="v" :value="v">{{ v }}</option></select></label>
            <label><span>V Cache</span><select v-model="form.v_cache_type"><option v-for="v in cacheTypes" :key="v" :value="v">{{ v }}</option></select></label>
            <template v-if="showDraftCache">
              <label><span>MTP/Draft K Cache</span><select v-model="form.draft_k_cache_type"><option v-for="v in draftCacheTypes" :key="v" :value="v">{{ v }}</option></select></label>
              <label><span>MTP/Draft V Cache</span><select v-model="form.draft_v_cache_type"><option v-for="v in draftCacheTypes" :key="v" :value="v">{{ v }}</option></select></label>
            </template>
            <label><span>Batch</span><input v-model.number="form.batch" type="number" /></label>
            <label><span>Ubatch</span><input v-model.number="form.ubatch" type="number" /></label>
            <label><span>CPU 线程</span><input v-model.number="form.threads" type="number" /></label>
            <label><span>温度</span><input v-model.number="form.temp" type="number" step="0.1" /></label>
            <label><span>Cache RAM (MiB)</span><input v-model.number="form.cache_ram" type="number" /></label>
            <label><span>空闲休眠 (秒)</span><input v-model.number="form.sleep_idle_seconds" type="number" /></label>
            <template v-if="selectedEngine && engineParameterSchema.length">
              <label v-if="supportsEngineParameter('device')"><span>设备</span><select v-if="engineParameter('device')?.values?.length" v-model="form.device"><option v-for="value in engineParameter('device')?.values" :key="value" :value="value">{{ value }}</option></select><input v-else v-model="form.device" placeholder="Vulkan0" /><small>统一设备参数，按引擎自动填充</small></label>
              <label v-if="supportsEngineParameter('fit')"><span>Fit 模式</span><select v-model="form.fit"><option value="">默认</option><option value="on">on</option><option value="off">off</option></select></label>
              <label v-if="supportsEngineParameter('cache_reuse')"><span>Cache reuse</span><input v-model.number="form.cache_reuse" type="number" min="0" max="1048576" /></label>
            </template>
          </div>
          <div class="switch-list">
            <label><input v-model="form.flash_attn" type="checkbox" /><span>Flash Attention</span></label>
            <label><input v-model="form.chunked_batch" type="checkbox" /><span>连续批处理</span></label>
            <label><input v-model="form.ui" type="checkbox" /><span>Web UI</span></label>
            <label v-if="supportsEngineParameter('kv_unified')"><input v-model="form.kv_unified" type="checkbox" /><span>统一 KV Cache（引擎推荐）</span></label>
          </div>
          <section v-for="group in advancedParameterGroups" :key="group.name" class="parameter-group">
            <h4>{{ group.name }}</h4>
            <div class="form-grid advanced-grid">
              <label v-for="parameter in group.parameters" :key="parameter.key" :title="parameterHint(parameter)">
                <template v-if="parameter.type === 'boolean'">
                  <span class="parameter-toggle"><input v-model="parameterValues[parameter.key]" type="checkbox" /><span>{{ parameter.label || parameter.key }}</span></span>
                </template>
                <template v-else>
                  <span>{{ parameter.label || parameter.key }}</span>
                  <select v-if="(parameter.type === 'select' || parameter.type === 'multi-select') && parameter.values?.length" v-model="parameterValues[parameter.key]" :multiple="parameter.type === 'multi-select'">
                    <option v-for="value in parameter.values" :key="value" :value="value">{{ value }}</option>
                  </select>
                  <input v-else v-model="parameterValues[parameter.key]" :type="inputType(parameter)" :min="parameter.min" :max="parameter.max" :step="parameter.step" :placeholder="parameter.placeholder" />
                </template>
                <small v-if="parameterHint(parameter)">{{ parameterHint(parameter) }}</small>
              </label>
            </div>
          </section>
        </details>
        <details class="advanced-panel engine-parameters-panel">
          <summary>4. 拓展配置（引擎/模型） <ChevronDown :size="16" /></summary>
          <p class="parameter-panel-help">这里仅显示当前引擎或模型专属参数；新增引擎优先通过参数文件扩展，不改变共性页面结构。</p>
          <template v-if="extensionParameterGroups.length">
            <section v-for="group in extensionParameterGroups" :key="group.name" class="parameter-group">
              <h4>{{ group.name }}</h4>
              <div class="form-grid advanced-grid">
                <label v-for="parameter in group.parameters" :key="parameter.key" :title="parameterHint(parameter)">
                  <template v-if="parameter.type === 'boolean'">
                    <span class="parameter-toggle"><input v-model="parameterValues[parameter.key]" type="checkbox" /><span>{{ parameter.label || parameter.key }}</span></span>
                  </template>
                  <template v-else>
                    <span>{{ parameter.label || parameter.key }}</span>
                    <select v-if="(parameter.type === 'select' || parameter.type === 'multi-select') && parameter.values?.length" v-model="parameterValues[parameter.key]" :multiple="parameter.type === 'multi-select'">
                      <option v-for="value in parameter.values" :key="value" :value="value">{{ value }}</option>
                    </select>
                    <input v-else v-model="parameterValues[parameter.key]" :type="inputType(parameter)" :min="parameter.min" :max="parameter.max" :step="parameter.step" :placeholder="parameter.placeholder" />
                  </template>
                  <small v-if="parameterHint(parameter)">{{ parameterHint(parameter) }}</small>
                </label>
              </div>
            </section>
          </template>
          <p v-else class="parameter-panel-help">当前引擎没有额外拓展参数，使用上面的共性配置即可。</p>
        </details>
        <p v-if="error" class="form-error">{{ error }}</p>
      </div>
      <footer class="drawer-footer">
        <button class="button ghost" @click="emit('close')">取消</button>
        <button class="button primary" :disabled="loading || submitting || !preflight?.can_deploy" @click="submit"><Rocket :size="16" />{{ submitting ? '创建部署任务…' : '确认部署' }}</button>
      </footer>
    </aside>
  </div>
</template>
