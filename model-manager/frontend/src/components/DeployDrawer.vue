<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { CheckCircle2, ChevronDown, Rocket, XCircle } from 'lucide-vue-next'
import { api } from '../services/api'
import type { DeployPayload, Engine, ModelArtifact, Preflight, ProjectionArtifact, RuntimeConfig } from '../types/model'

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
const contextOptions = [32768, 65536, 131072, 196608, 262144, 524288, 1048576]
const fallbackCacheTypes = ['f32', 'f16', 'bf16', 'q8_0', 'q4_0', 'q4_1', 'iq4_nl', 'q5_0', 'q5_1']
// MTP is selected by model recognition, while n-gram stays opt-in. Both
// controls remain visible so the user can see and change the effective mode.
const mtpEnabled = ref(supportsMtp)
const ngramEnabled = ref(false)

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

watch(() => form.llama_version, (key) => {
  if (key) window.localStorage.setItem('model-manager:last-engine', key)
  keepCacheSelection()
})

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
    keepCacheSelection()
    // The computed value is initially false while the engine list is loading,
    // so explicitly clear a previously saved MTP selection once the actual
    // selected engine is known.
    if (!engineSupportsMtp.value) mtpEnabled.value = false
    if (!form.mmproj_file && companions.length === 1) {
      form.mmproj_file = companions[0].id
      form.mmproj = true
    }
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
    form.mmproj = Boolean(form.mmproj_file)
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
          <h3>基础配置</h3>
          <div class="form-grid">
            <label><span>上下文大小</span><select v-model.number="form.ctx_size"><option v-for="size in contextOptions" :key="size" :value="size">{{ size >= 1048576 ? '1024K' : `${size / 1024}K` }}</option></select></label>
            <label><span>并发数</span><select v-model.number="form.concurrency"><option v-for="n in 4" :key="n" :value="n">{{ n }}</option></select></label>
            <label><span>GPU 层数</span><input v-model.number="form.ngl" type="number" min="1" max="99" /></label>
            <label><span>GPU</span><input v-model="form.gpu" /></label>
          </div>
        </section>

        <section class="form-section">
          <h3>模型增强</h3>
          <label v-if="projectors.length" class="full-field"><span>视觉投影组件</span><select v-model="form.mmproj_file"><option value="">不加载视觉组件</option><option v-for="projector in projectors" :key="projector.id" :value="projector.id">{{ projector.name }} · {{ Math.round(projector.size / 1024 / 1024) }}MB</option></select></label>
          <div class="enhancement-grid">
            <label class="check-card"><input v-model="mtpEnabled" type="checkbox" :disabled="!mtpAvailable || Boolean(form.draft_model_id)" /><span><strong>MTP 推测解码</strong><small>{{ mtpAvailable ? '使用模型自带草稿预测' : supportsMtp ? '当前引擎不支持 MTP，已默认关闭' : '当前模型未识别为 MTP，默认关闭' }}</small></span></label>
            <label class="check-card"><input v-model="ngramEnabled" type="checkbox" :disabled="selectedEngine && selectedEngine.supports_ngram === false" /><span><strong>n-gram 加速</strong><small>匹配历史 token 序列，默认关闭</small></span></label>
          </div>
          <label v-if="draftModels.length || engineSupportsDraft" class="full-field draft-model-field"><span>外置草稿模型</span><select v-model="form.draft_model_id" :disabled="!draftAvailable || mtpEnabled"><option value="">不使用外置草稿模型</option><option v-for="draft in draftModels" :key="draft.id" :value="draft.id">{{ draft.name }} · {{ draft.size_human }}</option></select><small>{{ mtpEnabled ? '已启用 MTP 内置草稿层；如需外置草稿模型，请先取消 MTP' : draftAvailable ? '仅展示与当前主模型同包或同族的草稿组件' : '当前模型包未发现可用草稿模型，或所选引擎不支持 --model-draft' }}</small></label>
          <div v-if="mtpEnabled || ngramEnabled" class="form-grid enhancement-params">
            <label v-if="mtpEnabled"><span>Draft 预测数</span><input v-model.number="form.spec_draft_n_max" type="number" min="1" max="32" /></label>
            <label v-if="ngramEnabled"><span>ngram 查找长度</span><input v-model.number="form.ngram_mod_n_match" type="number" min="1" max="256" /></label>
          </div>
        </section>

        <section class="form-section">
          <h3>运行引擎</h3>
          <label class="full-field"><span>llama.cpp 版本</span><select v-model="form.llama_version"><option v-for="engine in engines" :key="engine.key" :value="engine.key">{{ engine.name }} · {{ engine.version }}</option></select></label>
          <div v-if="selectedEngine" class="feature-row"><span v-for="feature in selectedEngine.features" :key="feature">{{ feature }}</span></div>
        </section>

        <details class="advanced-panel">
          <summary>高级参数 <ChevronDown :size="16" /></summary>
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
          </div>
          <div class="switch-list">
            <label><input v-model="form.flash_attn" type="checkbox" /><span>Flash Attention</span></label>
            <label><input v-model="form.chunked_batch" type="checkbox" /><span>连续批处理</span></label>
            <label><input v-model="form.ui" type="checkbox" /><span>Web UI</span></label>
          </div>
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
