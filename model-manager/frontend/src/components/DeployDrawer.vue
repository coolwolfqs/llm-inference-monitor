<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { CheckCircle2, ChevronDown, Rocket, XCircle } from 'lucide-vue-next'
import { api } from '../services/api'
import type { DeployPayload, DeploymentPlan, Engine, EngineParameter, ModelArtifact, Preflight, ProjectionArtifact, RuntimeConfig } from '../types/model'

const props = defineProps<{ model: ModelArtifact; currentConfig: RuntimeConfig; isCurrent?: boolean }>()
const emit = defineEmits<{ close: []; deployed: [taskId: string] }>()
const preflight = ref<Preflight>()
// Preflight is for compatibility and display. The deployment-plan response is
// the authoritative, name-keyed set of values used by the deploy API.
const canonicalPlan = ref<DeploymentPlan>()
let planRequestId = 0
const engines = ref<Engine[]>([])
const projectors = ref<ProjectionArtifact[]>([])
const draftModels = computed(() => preflight.value?.draft_models || [])
const loading = ref(true)
const submitting = ref(false)
const error = ref('')
const supportsMtp = props.model.tags.some((item) => item.toLowerCase() === 'mtp')
  || (props.model.classification?.capabilities || []).some((item) => item.toLowerCase() === 'mtp')
const contextOptions = [32768, 65536, 131072, 196608, 262144, 524288, 1048576]
const fallbackCacheTypes = ['f32', 'f16', 'bf16', 'q8_0', 'q4_0', 'q4_1', 'iq4_nl', 'q5_0', 'q5_1']
// MTP is selected by model recognition, while n-gram stays opt-in. Both
// controls remain visible so the user can see and change the effective mode.
const mtpEnabled = ref(supportsMtp)
const ngramEnabled = ref(false)
const profileId = ref('default')
const parameterValues = reactive<Record<string, unknown>>({})
const restoringConfig = ref(false)
const persistenceReady = ref(false)
const restoredRememberedConfig = ref(false)

const rememberedFormKeys = [
  'ctx_size', 'ngl', 'gpu', 'concurrency', 'k_cache_type', 'v_cache_type',
  'batch', 'ubatch', 'flash_attn', 'chunked_batch', 'threads', 'threads_http',
  'temp', 'reasoning', 'ui', 'mmproj_file', 'draft_model_id', 'spec_draft_n_max',
  'draft_k_cache_type', 'draft_v_cache_type', 'ngram_mod_n_min', 'ngram_mod_n_max',
  'ngram_mod_n_match', 'cache_ram', 'sleep_idle_seconds', 'device', 'fit',
  'kv_unified', 'cache_reuse', 'spec_draft_p_min',
] as const

type RememberedDeployConfig = {
  profile_id?: string
  mtp_enabled?: boolean
  ngram_enabled?: boolean
  form?: Record<string, unknown>
  parameters?: Record<string, unknown>
}

function normalizeReasoning(value: unknown): DeployPayload['reasoning'] {
  const normalized = String(value ?? '').toLowerCase()
  return normalized === 'on' || normalized === 'auto' ? normalized : 'off'
}

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
  flash_attn: props.currentConfig.flash_attn == null
    ? true
    : props.currentConfig.flash_attn === true || String(props.currentConfig.flash_attn).toLowerCase() === 'on',
  chunked_batch: true,
  threads: props.currentConfig.threads || 8,
  threads_http: 4,
  temp: props.currentConfig.temp ?? 0.7,
  engine: 'llama',
  llama_version: '',
  reasoning: normalizeReasoning(props.currentConfig.reasoning),
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
const engineMatches = computed(() => preflight.value?.engine_matches || [])
const selectedEngineMatch = computed(() => engineMatches.value.find((item) => item.key === form.llama_version))
const engineSupportsMtp = computed(() => Boolean(
  (selectedEngineMatch.value?.capabilities || []).includes('mtp')
  || selectedEngine.value?.supports_mtp
  || selectedEngine.value?.version_params?.spec_draft_n_max,
))
const mtpAvailable = computed(() => supportsMtp && engineSupportsMtp.value)
const engineSupportsDraft = computed(() => Boolean(selectedEngine.value?.supports_draft_model))
const draftAvailable = computed(() => engineSupportsDraft.value && draftModels.value.length > 0)
const ngramAvailable = computed(() => Boolean(selectedEngine.value && selectedEngine.value.supports_ngram !== false))
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
const serverProfile = computed(() => selectedEngineMatch.value?.profiles?.[profileId.value])
const engineRecommendedParams = computed(() => serverProfile.value?.parameters || selectedEngine.value?.recommended_params || {})
const engineDifferences = computed(() => Object.entries(selectedEngine.value?.parameter_differences || {}))
const supportedEngineParameterCount = computed(() => engineParameterSchema.value.filter((parameter) => parameter.supported !== false).length)
const engineProfiles = computed(() => selectedEngineMatch.value?.profiles || selectedEngine.value?.profiles || {})
const profileOptions = computed(() => Object.entries(engineProfiles.value).filter(([, profile]) => profile.compatible !== false))
const selectedProfile = computed(() => engineProfiles.value[profileId.value])
const observedRuntime = computed(() => Boolean(props.isCurrent && (props.currentConfig.pid || props.currentConfig.model_path)))
const contextPerSlotLimit = computed(() => {
  const canonicalTotal = Number(canonicalPlan.value?.limits?.ctx_size_max || 0)
  const canonicalSlots = Math.max(1, Number(canonicalPlan.value?.parameters?.concurrency || 1))
  if (canonicalTotal > 0) return canonicalTotal / canonicalSlots
  const modelLimit = Number(preflight.value?.requirements?.context_length || 0)
  const profileLimits = Object.values(engineProfiles.value).map((profile) => {
    const slots = Math.max(1, Number(profile.parameters?.concurrency || 1))
    const total = Number(profile.limits?.ctx_size_max || 0)
    return total > 0 ? total / slots : 0
  }).filter((value) => value > 0)
  return modelLimit || (profileLimits.length ? Math.max(...profileLimits) : 1048576)
})
const contextLimit = computed(() => Math.min(1048576, contextPerSlotLimit.value * Math.max(1, Number(form.concurrency || 1))))
const visibleContextOptions = computed(() => {
  const options = contextOptions.filter((size) => size <= contextLimit.value)
  const current = Number(form.ctx_size || 0)
  return current && !options.includes(current) ? [...options, current].sort((a, b) => a - b) : options
})
const selectedProfileLabel = computed(() => {
  const planned = canonicalPlan.value?.parameters || serverProfile.value?.parameters || {}
  const comparisons: Array<[string, unknown]> = [
    ['ctx_size', form.ctx_size],
    ['concurrency', form.concurrency],
    ['batch', form.batch],
    ['ubatch', form.ubatch],
    ['device', form.device],
    ['k_cache_type', form.k_cache_type],
    ['v_cache_type', form.v_cache_type],
    ['flash_attn', form.flash_attn],
    ['chunked_batch', form.chunked_batch],
    ['spec_draft_n_max', form.spec_draft_n_max],
    ['fit', form.fit],
  ]
  const matches = comparisons.every(([key, actual]) => planned[key] === undefined || comparisonValue(key, planned[key]) === comparisonValue(key, actual))
  if (!matches) return '自定义调度'
  return selectedProfile.value?.label || (profileId.value === 'default' ? '最佳默认' : profileId.value)
})
const contextLabel = computed(() => {
  const size = Number(form.ctx_size || 0)
  return size >= 1048576 ? '1024K' : size >= 1024 ? `${Math.round(size / 1024)}K` : String(size)
})

function clampContextToConcurrency() {
  const current = Number(form.ctx_size || 0)
  if (current > contextLimit.value) form.ctx_size = contextLimit.value
}

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
  const options: Array<{ value: 'none' | 'mtp' | 'ngram' | 'mtp_ngram'; label: string; disabled?: boolean }> = [
    { value: 'none', label: '关闭投机解码' },
  ]
  options.push({ value: 'mtp', label: mtpAvailable.value ? 'MTP（模型内置草稿）' : 'MTP（当前不可用）', disabled: !mtpAvailable.value })
  options.push({ value: 'ngram', label: ngramAvailable.value ? 'n-gram' : 'n-gram（当前不可用）', disabled: !ngramAvailable.value })
  options.push({ value: 'mtp_ngram', label: 'MTP + n-gram', disabled: !mtpAvailable.value || !ngramAvailable.value })
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

const managedFormAliases: Record<string, string> = {
  ctx_size: 'ctx_size',
  ngl: 'ngl',
  concurrency: 'concurrency',
  batch: 'batch',
  ubatch: 'ubatch',
  threads: 'threads',
  threads_http: 'threads_http',
  flash_attn: 'flash_attn',
  chunked_batch: 'chunked_batch',
  k_cache_type: 'k_cache_type',
  v_cache_type: 'v_cache_type',
  draft_k_cache_type: 'draft_k_cache_type',
  draft_v_cache_type: 'draft_v_cache_type',
  spec_type: 'spec_type',
  spec_draft_n_max: 'spec_draft_n_max',
  spec_draft_p_min: 'spec_draft_p_min',
  ngram_mod_n_min: 'ngram_mod_n_min',
  ngram_mod_n_max: 'ngram_mod_n_max',
  ngram_mod_n_match: 'ngram_mod_n_match',
  cache_ram: 'cache_ram',
  sleep_idle_seconds: 'sleep_idle_seconds',
  temp: 'temp',
  reasoning: 'reasoning',
  ui: 'ui',
  device: 'device',
  fit: 'fit',
  kv_unified: 'kv_unified',
  cache_reuse: 'cache_reuse',
  mmproj: 'mmproj',
}

function normalizeFormDefault(key: string, value: unknown) {
  if (key === 'reasoning') return normalizeReasoning(value)
  if (key === 'flash_attn' || key === 'chunked_batch') {
    return value === true || ['on', 'true', '1', 'yes'].includes(String(value).toLowerCase())
  }
  return value
}

function comparisonValue(key: string, value: unknown) {
  if (key === 'flash_attn' || key === 'chunked_batch') {
    return value === true || ['on', 'true', '1', 'yes'].includes(String(value).toLowerCase())
  }
  return value === null || value === undefined ? '' : String(value)
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
  const rawProfileValues = profile?.parameters || profile?.values || {}
  const profileValues = serverProfile.value?.parameters || rawProfileValues
  const recommended = { ...engineRecommendedParams.value, ...profileValues }
  form.profile_id = profileId.value
  const formRecord = form as unknown as Record<string, unknown>
  for (const parameter of engineParameterSchema.value) {
    if (parameter.supported === false || !parameter.managed) continue
    const formKey = managedFormAliases[parameter.key]
    if (!formKey) continue
    const fallback = parameter.key === 'ctx_size' ? (props.model.ctx_default ?? parameter.default) : parameter.default
    const value = recommended[parameter.key] ?? parameter.recommended ?? fallback
    if (value !== undefined && value !== null && value !== '') {
      formRecord[formKey] = normalizeFormDefault(parameter.key, value)
    }
  }
  for (const parameter of engineParameterSchema.value) {
    if (parameter.supported === false || parameter.managed) continue
    const profileValue = profileValues[parameter.key]
    const currentValue = props.currentConfig.parameters?.[parameter.key]
    const recommendedValue = engineRecommendedParams.value[parameter.key]
    const value = profileValue ?? currentValue ?? recommendedValue ?? parameter.default
    if (value !== undefined && value !== null) parameterValues[parameter.key] = value
    else delete parameterValues[parameter.key]
  }
  const resolvedMmproj = (recommended.mmproj_file ?? form.mmproj_file) as unknown
  if (typeof resolvedMmproj === 'string' && resolvedMmproj) {
    const projector = projectorForValue(resolvedMmproj)
    if (projector) form.mmproj_file = projector.id
  }
  if (recommended.mmproj !== undefined) form.mmproj = Boolean(recommended.mmproj)
}

function rememberedConfigKey(engine = form.llama_version) {
  if (!engine) return ''
  // v4 could contain values written by the old positional/profile restore
  // path. A new key prevents a stale browser entry from reintroducing the
  // field mix-up this drawer is meant to prevent.
  return `model-manager:deploy-config:v5:${encodeURIComponent(props.model.id)}:${encodeURIComponent(engine)}`
}

function readRememberedConfig(engine = form.llama_version): RememberedDeployConfig | undefined {
  const key = rememberedConfigKey(engine)
  if (!key || typeof window === 'undefined') return undefined
  try {
    const raw = window.localStorage.getItem(key)
    if (!raw) return undefined
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? parsed as RememberedDeployConfig : undefined
  } catch {
    return undefined
  }
}

function restoreRememberedConfig(config?: RememberedDeployConfig) {
  restoredRememberedConfig.value = Boolean(config)
  if (!config) return
  const formRecord = form as unknown as Record<string, unknown>
  for (const key of rememberedFormKeys) {
    if (config.form && Object.prototype.hasOwnProperty.call(config.form, key)) {
      formRecord[key] = key === 'reasoning' ? normalizeReasoning(config.form[key]) : config.form[key]
    }
  }
  if (config.form?.mmproj_file) {
    const projector = projectorForValue(String(config.form.mmproj_file))
    form.mmproj_file = projector?.id || ''
  }
  if (config.form?.draft_model_id) {
    const draftId = String(config.form.draft_model_id)
    form.draft_model_id = draftModels.value.some((item) => item.id === draftId) ? draftId : ''
  }
  mtpEnabled.value = Boolean(config.mtp_enabled) && mtpAvailable.value
  ngramEnabled.value = Boolean(config.ngram_enabled) && selectedEngine.value?.supports_ngram !== false
  if (config.parameters && typeof config.parameters === 'object') {
    const allowed = new Set(engineParameterSchema.value.filter((item) => item.supported !== false && !item.managed).map((item) => item.key))
    for (const [key, value] of Object.entries(config.parameters)) {
      if (allowed.has(key)) parameterValues[key] = value
    }
  }
  form.mmproj = Boolean(form.mmproj_file)
  keepCacheSelection()
}

function restoreObservedRuntimeConfig() {
  if (!observedRuntime.value) return
  const formRecord = form as unknown as Record<string, unknown>
  for (const [parameterKey, formKey] of Object.entries(managedFormAliases)) {
    if (!Object.prototype.hasOwnProperty.call(props.currentConfig, parameterKey)) continue
    const value = props.currentConfig[parameterKey as keyof RuntimeConfig]
    if (value !== undefined && value !== null) formRecord[formKey] = normalizeFormDefault(parameterKey, value)
  }
  if (Object.prototype.hasOwnProperty.call(props.currentConfig, 'mmproj_file')) {
    const projector = projectorForValue(String(props.currentConfig.mmproj_file || ''))
    form.mmproj_file = projector?.id || ''
  }
  const specType = String(props.currentConfig.spec_type || '')
  mtpEnabled.value = specType.includes('draft-mtp') && mtpAvailable.value
  ngramEnabled.value = specType.includes('ngram') && selectedEngine.value?.supports_ngram !== false
  form.mmproj = Boolean(props.currentConfig.mmproj ?? form.mmproj_file)
  keepCacheSelection()
}

function selectEngineDefaults(engine: string) {
  if (!engine || !engines.value.length) return
  restoringConfig.value = true
  const remembered = readRememberedConfig(engine)
  const rememberedProfile = remembered?.profile_id
  profileId.value = rememberedProfile && engineProfiles.value[rememberedProfile] ? rememberedProfile : 'default'
  const preserveObserved = observedRuntime.value
    && !remembered
    && String(props.currentConfig.llama_version || '') === engine
  applyEngineProfile()
  if (preserveObserved) restoreObservedRuntimeConfig()
  restoreRememberedConfig(remembered)
  keepCacheSelection()
  restoringConfig.value = false
  persistenceReady.value = true
  void loadCanonicalPlan(engine, profileId.value, !remembered && !preserveObserved)
}

function persistRememberedConfig() {
  if (!persistenceReady.value || restoringConfig.value || typeof window === 'undefined') return
  const key = rememberedConfigKey()
  if (!key) return
  const formRecord = form as unknown as Record<string, unknown>
  const rememberedForm = Object.fromEntries(rememberedFormKeys.map((name) => [name, formRecord[name]]))
  try {
    window.localStorage.setItem(key, JSON.stringify({
      profile_id: profileId.value,
      mtp_enabled: mtpEnabled.value,
      ngram_enabled: ngramEnabled.value,
      form: rememberedForm,
      parameters: { ...parameterValues },
    }))
  } catch {
    // Local storage is an enhancement; a quota/private-mode failure must not
    // block deployment.
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

function applyResolvedParameters(values: Record<string, unknown>) {
  const formRecord = form as unknown as Record<string, unknown>
  // Every visible common control is assigned by its canonical parameter key;
  // never by the order of the schema array. This is the binding contract for
  // context/concurrency, draft token count, Fit, KV and the other fields.
  for (const [parameterKey, formKey] of Object.entries(managedFormAliases)) {
    if (!Object.prototype.hasOwnProperty.call(values, parameterKey)) continue
    const value = values[parameterKey]
    if (value !== undefined && value !== null && value !== '') {
      formRecord[formKey] = normalizeFormDefault(parameterKey, value)
    }
  }
  form.profile_id = profileId.value
}

function applyResolvedParameterValues(values: Record<string, unknown>) {
  for (const parameter of engineParameterSchema.value) {
    if (parameter.supported === false || parameter.managed) continue
    if (Object.prototype.hasOwnProperty.call(values, parameter.key) && values[parameter.key] !== undefined) {
      parameterValues[parameter.key] = values[parameter.key]
    } else if (!Object.prototype.hasOwnProperty.call(parameterValues, parameter.key)) {
      delete parameterValues[parameter.key]
    }
  }
}

async function loadCanonicalPlan(
  engine = form.llama_version,
  profile = profileId.value,
  applyToForm = true,
) {
  if (!engine) return
  const requestId = ++planRequestId
  canonicalPlan.value = undefined
  try {
    const plan = await api.deploymentPlan(props.model.id, engine, profile)
    if (requestId !== planRequestId || engine !== form.llama_version || profile !== profileId.value) return
    canonicalPlan.value = plan
    if (applyToForm) {
      applyResolvedParameters(plan.parameters || {})
      applyResolvedParameterValues(plan.parameters || {})
    }
    keepCacheSelection()
  } catch {
    // The preflight profile summary remains a safe fallback. The deploy API
    // still validates the final payload against the same resolver.
  }
}

watch(engineSupportsMtp, (available) => {
  if (!available) mtpEnabled.value = false
}, { flush: 'sync' })

watch(() => selectedEngine.value?.supports_ngram, (available) => {
  if (available === false) ngramEnabled.value = false
}, { flush: 'sync' })

watch(() => form.llama_version, (key) => {
  if (key) window.localStorage.setItem('model-manager:last-engine', key)
  if (key && engines.value.length) {
    selectEngineDefaults(key)
  }
  keepCacheSelection()
}, { flush: 'sync' })

watch(profileId, () => {
  if (!restoringConfig.value) {
    applyEngineProfile()
    void loadCanonicalPlan()
  }
}, { flush: 'sync' })

watch(mtpEnabled, (enabled) => {
  if (enabled) form.draft_model_id = ''
}, { flush: 'sync' })

watch(() => form.draft_model_id, (value) => {
  if (value) mtpEnabled.value = false
}, { flush: 'sync' })

watch(() => form.concurrency, clampContextToConcurrency, { flush: 'sync' })

watch(form, persistRememberedConfig, { deep: true })
watch(parameterValues, persistRememberedConfig, { deep: true })
watch([mtpEnabled, ngramEnabled, profileId], persistRememberedConfig, { flush: 'sync' })

onMounted(async () => {
  try {
    const [check, available, companions] = await Promise.all([api.preflight(props.model.id), api.engines(), api.projectors(props.model.id)])
    preflight.value = check
    const compatibleKeys = new Set(check.compatible_llama_versions || check.engine_matches?.filter((item) => item.compatible).map((item) => item.key) || [])
    engines.value = available.filter((item) => item.type !== 'vllm' && compatibleKeys.has(item.key))
    projectors.value = companions
    normalizeProjectorSelection(companions)
    form.engine = check.default_engine || check.compatible_engines[0] || 'llama'
    const rememberedEngine = window.localStorage.getItem('model-manager:last-engine')
    const engineCandidates = [
      ...(props.isCurrent ? [props.currentConfig.llama_version, rememberedEngine] : [rememberedEngine, props.currentConfig.llama_version]),
      check.default_llama_version,
      ...(check.compatible_llama_versions || []),
      engines.value.find((item) => item.key === 'vulkan')?.key,
      engines.value[0]?.key,
    ].filter((key): key is string => Boolean(key))
    form.llama_version = engineCandidates.find((key) => engines.value.some((item) => item.key === key)) || ''
    keepCacheSelection()
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
          <div><strong>{{ preflight.can_deploy ? '预检通过' : '当前不可部署' }}</strong><p>{{ preflight.can_deploy ? `兼容引擎：${(preflight.compatible_llama_versions || preflight.compatible_engines).join(', ')}` : preflight.blockers.join('；') }}</p></div>
        </div>

        <section class="form-section">
          <h3>引擎选择 <span class="section-note">模型 → 引擎 → 调度</span></h3>
          <div class="deploy-flow">
            <div><small>模型</small><strong>{{ model.family || model.alias || model.name }}</strong><span>{{ model.format || 'GGUF' }} · {{ model.size_human }}</span></div>
            <b>→</b>
            <div><small>引擎</small><strong>{{ selectedEngine?.name || '选择引擎' }}</strong><span>{{ selectedEngine?.version || '加载中' }}<span v-if="selectedEngine?.backend"> · {{ selectedEngine.backend }}</span></span></div>
            <b>→</b>
            <div><small>调度</small><strong>{{ selectedProfileLabel }}</strong><span>{{ form.concurrency }} 并发 · {{ contextLabel }}</span></div>
          </div>
          <label class="full-field"><span>引擎</span><select v-model="form.llama_version"><option v-for="engine in engines" :key="engine.key" :value="engine.key">{{ engine.name }} · {{ engine.version }}</option></select></label>
          <label v-if="profileOptions.length" class="full-field"><span>调度方案</span><select v-model="profileId"><option v-for="([key, profile]) in profileOptions" :key="key" :value="key">{{ profile.label || key }}</option></select></label>
          <p v-if="restoredRememberedConfig" class="config-memory">已恢复此模型 + 引擎的最近一次配置</p>
          <p v-else-if="observedRuntime" class="config-memory">已载入当前 llama-server 进程参数；profile 只作为比较基线，实际差异会保留。</p>
          <details v-if="selectedEngine" class="inline-details">
            <summary>更多引擎信息 <ChevronDown :size="15" /></summary>
            <div class="feature-row"><span v-for="feature in selectedEngine.features" :key="feature">{{ feature }}</span></div>
            <div class="engine-profile">
              <div class="engine-profile-title">{{ selectedEngine.branch ? `分支：${selectedEngine.branch}` : '通用 llama.cpp 引擎' }}<span v-if="selectedEngine.gpu_targets?.length"> · {{ selectedEngine.gpu_targets.join(', ') }}</span></div>
              <p v-if="selectedEngine.backend || selectedEngine.driver">后端：{{ selectedEngine.backend || 'llama' }}<span v-if="selectedEngine.driver"> · 驱动：{{ selectedEngine.driver }}</span></p>
              <p v-if="selectedEngine.binary_path">二进制：<code>{{ selectedEngine.binary_path }}</code></p>
              <p v-if="selectedEngine.parameter_file">参数文件：<code>{{ selectedEngine.parameter_file }}</code></p>
              <p v-if="selectedEngine.load_strategy?.default">加载：{{ selectedEngine.load_strategy.default.load_mode || '默认' }}<span v-if="selectedEngine.load_strategy.default.fit"> · Fit {{ selectedEngine.load_strategy.default.fit }}</span><span v-if="selectedEngine.load_strategy.default.kv_cache"> · KV {{ selectedEngine.load_strategy.default.kv_cache }}</span></p>
              <p v-if="selectedEngineMatch?.warnings?.length">匹配提示：{{ selectedEngineMatch.warnings.join('；') }}</p>
              <p>共性 {{ supportedEngineParameterCount - (selectedEngine.exclusive_parameters?.length || 0) }} 项 · 拓展 {{ selectedEngine.exclusive_parameters?.length || 0 }} 项</p>
              <div v-if="engineDifferences.length" class="engine-parameter-list">
                <span v-for="([key, difference]) in engineDifferences" :key="key" :title="difference.reason">{{ key }}：{{ difference.recommended }}</span>
              </div>
            </div>
          </details>
        </section>

        <section class="form-section">
          <h3>基础配置</h3>
          <div class="form-grid">
            <label data-field="ctx_size"><span>上下文大小</span><select v-model.number="form.ctx_size"><option v-for="size in visibleContextOptions" :key="size" :value="size">{{ size >= 1048576 ? '1024K' : `${size / 1024}K` }}</option></select><small>上限 {{ Math.round(contextLimit / 1024) }}K（模型窗口 × 并发数）</small></label>
            <label data-field="concurrency"><span>并发数</span><select v-model.number="form.concurrency"><option v-for="n in 4" :key="n" :value="n">{{ n }}</option></select></label>
          </div>
          <label class="full-field reasoning-field" data-field="reasoning"><span>思考模式</span><select v-model="form.reasoning"><option value="off">关闭思考模式</option><option value="on">开启思考模式</option><option value="auto">自动</option></select><small>关闭后不输出思考过程；自动由模型或引擎决定</small></label>
          <p class="section-default">默认：按模型能力 × 引擎推荐 profile 生成；已保存的本模型配置优先恢复。</p>
          <details class="inline-details">
            <summary>更多基础配置 <ChevronDown :size="15" /></summary>
            <div class="form-grid">
              <label><span>GPU 层数</span><input v-model.number="form.ngl" type="number" min="1" max="99" /></label>
              <label><span>GPU</span><input v-model="form.gpu" /></label>
            </div>
            <label class="full-field"><span>视觉模型</span><select v-model="form.mmproj_file"><option value="">不加载视觉模型</option><option v-for="projector in projectors" :key="projector.id" :value="projector.id">{{ projector.name }} · {{ Math.round(projector.size / 1024 / 1024) }}MB</option></select><small>{{ projectors.length ? '仅展示当前模型目录的匹配项，默认选择最佳匹配' : '当前模型目录没有可匹配的视觉模型' }}</small></label>
            <div class="form-grid enhancement-params">
<label><span>投机解码</span><select v-model="speculationMode"><option v-for="option in speculationModeOptions" :key="option.value" :value="option.value" :disabled="option.disabled">{{ option.label }}</option></select><small>{{ supportsMtp ? '模型已识别 MTP，默认 MTP' : '模型未识别 MTP，默认关闭' }}</small></label>
              <label v-if="mtpEnabled || form.draft_model_id" data-field="spec_draft_n_max"><span>预测 token 数</span><input v-model.number="form.spec_draft_n_max" type="number" min="1" max="32" /></label>
              <label v-if="(mtpEnabled || form.draft_model_id) && supportsEngineParameter('spec_draft_p_min')" data-field="spec_draft_p_min"><span>接受阈值</span><input v-model.number="form.spec_draft_p_min" type="number" min="0" max="1" step="0.01" /><small>推荐 {{ recommendedValue({ key: 'spec_draft_p_min' }) }}</small></label>
              <label v-if="ngramEnabled" data-field="ngram_mod_n_match"><span>n-gram 匹配长度</span><input v-model.number="form.ngram_mod_n_match" type="number" min="1" max="256" /></label>
            </div>
            <label v-if="draftModels.length || engineSupportsDraft" class="full-field draft-model-field"><span>草稿模型</span><select v-model="form.draft_model_id" :disabled="!draftAvailable || mtpEnabled"><option value="">不使用外置草稿模型</option><option v-for="draft in draftModels" :key="draft.id" :value="draft.id">{{ draft.name }} · {{ draft.size_human }}</option></select><small>{{ mtpEnabled ? '当前使用 MTP；切换到 n-gram 或关闭后可选外置草稿模型' : draftAvailable ? '仅展示当前模型目录或模型族的草稿模型' : '未发现可用草稿模型' }}</small></label>
          </details>
        </section>

        <details class="advanced-panel">
          <summary>高级参数 <ChevronDown :size="16" /></summary>
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
              <label v-if="supportsEngineParameter('fit')" data-field="fit"><span>Fit 模式</span><select v-model="form.fit"><option value="">默认</option><option value="on">on</option><option value="off">off</option></select></label>
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
          <summary>拓展配置 <ChevronDown :size="16" /></summary>
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
