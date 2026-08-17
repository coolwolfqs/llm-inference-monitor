export interface ModelClassification {
  general_name?: string
  architecture?: string
  architecture_type?: string
  parameters?: string
  active_parameters?: string[]
  context_length?: number | null
  confidence?: 'high' | 'medium' | 'low'
  capabilities?: string[]
  warnings?: Array<{ code: string; field?: string }>
}

export interface ModelArtifact {
  id: string
  relative_path: string
  name: string
  display_name: string
  alias?: string
  relative_dir?: string
  size: number
  size_human: string
  modified: number
  ctx_default?: number | null
  format?: string
  quant_type?: string
  tags: string[]
  role: 'model' | 'projection' | 'draft' | string
  category: string
  family?: string
  classification?: ModelClassification
  deployable: boolean
  supported_engines: string[]
}

export interface RuntimeConfig {
  pid?: number | null
  model?: string
  model_path?: string
  binary_path?: string
  alias?: string
  llama_version?: string
  profile_id?: string | null
  ctx_size?: number
  ngl?: number
  concurrency?: number
  gpu?: string
  k_cache_type?: string
  v_cache_type?: string
  batch?: number
  ubatch?: number
  threads?: number
  threads_http?: number
  temp?: number
  flash_attn?: boolean | string
  chunked_batch?: boolean | string
  reasoning?: string
  ui?: boolean
  host?: string
  port?: number
  spec_type?: string
  cache_ram?: number
  sleep_idle_seconds?: number
  mmproj?: boolean
  mmproj_file?: string
  draft_model?: string
  draft_model_path?: string
  draft_k_cache_type?: string
  draft_v_cache_type?: string
  spec_draft_n_max?: number
  ngram_mod_n_min?: number
  ngram_mod_n_max?: number
  ngram_mod_n_match?: number
  device?: string
  fit?: string
  kv_unified?: boolean
  cache_reuse?: number | null
  spec_draft_p_min?: number | null
  parameters?: Record<string, unknown>
}

export interface ProjectionArtifact {
  id: string
  name: string
  size: number
  path?: string
  relative_path?: string
  relative_dir?: string
}

export interface ModelsResponse {
  schema_version: string
  models: ModelArtifact[]
  current_model: string
  current_model_id: string
  current_config: RuntimeConfig
  desired_config?: {
    profile_id?: string
    parameters?: Record<string, unknown>
    [key: string]: unknown
  }
  server_running: boolean
  mmproj_enabled: boolean
  total_size: string
  disk_free: string
  summary?: { deployable: number; artifacts?: number }
}

export interface Engine {
  key: string
  name: string
  binary_path?: string
  version: string
  type: string
  features: string[]
  supports_mtp?: boolean
  supports_draft_model?: boolean
  supports_ngram?: boolean
  cache_types?: string[]
  draft_cache_types?: string[]
  spec_types?: string[]
  version_params?: Record<string, unknown>
  backend?: string
  branch?: string
  commit?: string
  source?: string
  github_url?: string
  gpu_targets?: string[]
  driver?: string
  parameter_schema?: EngineParameter[]
  parameter_notes?: string[]
  recommended_params?: Record<string, unknown>
  deployment_parameters?: EngineParameter[]
  exclusive_parameters?: string[]
  parameter_differences?: Record<string, {
    recommended?: unknown
    default?: unknown
    reason?: string
  }>
  parameter_file?: string
  parameter_config_version?: number | null
  profiles?: Record<string, {
    label?: string
    description?: string
    compatible?: boolean
    limits?: { ctx_size_max?: number; [key: string]: unknown }
    parameters?: Record<string, unknown>
    values?: Record<string, unknown>
  }>
  load_strategy?: {
    model_root?: string
    binary_root?: string
    default?: Record<string, unknown>
    artifacts?: string[]
    [key: string]: unknown
  }
  engine_environment?: Record<string, unknown>
  capabilities?: string[]
}

export interface EngineParameter {
  key: string
  label?: string
  type?: string
  description?: string
  flag?: string
  group?: string
  supported?: boolean
  common?: boolean
  default?: unknown
  recommended?: unknown
  min?: number
  max?: number
  step?: number
  values?: string[]
  false_flag?: string
  env?: string
  managed?: boolean
  placeholder?: string
  visible_when?: Record<string, unknown>
  requires?: string[]
  conflicts?: string[]
  source?: string
  secret?: boolean
  deprecated?: boolean
  load_phase?: 'env' | 'model' | 'runtime' | string
}

export interface Operation {
  sequence: number
  operation_id: string
  method: string
  path: string
  state: 'running' | 'succeeded' | 'failed'
  status_code?: number
  started_at: number
  duration_ms?: number
}

export interface Preflight {
  can_deploy: boolean
  blockers: string[]
  compatible_engines: string[]
  compatible_llama_versions?: string[]
  default_engine?: string
  default_llama_version?: string
  draft_models?: ModelArtifact[]
  projectors?: ProjectionArtifact[]
  requirements?: Record<string, unknown>
  engine_matches?: EngineMatch[]
}

export interface EngineMatchProfile {
  label?: string
  compatible?: boolean
  parameters?: Record<string, unknown>
  values?: Record<string, unknown>
  limits?: { ctx_size_max?: number; [key: string]: unknown }
  reasons?: string[]
}

export interface EngineMatch {
  key: string
  name?: string
  version?: string
  compatible: boolean
  reasons: string[]
  warnings: string[]
  capabilities: string[]
  recommended_profile?: string | null
  profiles?: Record<string, EngineMatchProfile>
}

export interface DeploymentPlan {
  schema_version: number
  model: Record<string, unknown>
  engine: { key: string; type: string; version?: string; parameter_file?: string }
  match: { compatible: boolean; reasons: string[]; warnings: string[]; capabilities: string[] }
  profile_id: string
  profile: Record<string, unknown>
  parameters: Record<string, unknown>
  parameter_schema: EngineParameter[]
  limits: { ctx_size_max?: number; [key: string]: unknown }
  artifacts: { projectors: ProjectionArtifact[]; draft_models: ModelArtifact[] }
}

export interface DeployPayload {
  filename: string
  ctx_size: number
  ngl: number
  gpu: string
  concurrency: number
  k_cache_type: string
  v_cache_type: string
  batch: number
  ubatch: number
  flash_attn: boolean
  chunked_batch: boolean
  threads: number
  threads_http: number
  temp: number
  engine: string
  llama_version: string
  profile_id?: string
  reasoning: 'on' | 'off' | 'auto'
  ui: boolean
  mmproj: boolean
  mmproj_file: string
  draft_model_id?: string
  draft_model?: string
  spec_type: string
  spec_draft_n_max: number
  draft_k_cache_type: string
  draft_v_cache_type: string
  ngram_mod_n_min: number
  ngram_mod_n_max: number
  ngram_mod_n_match: number
  cache_ram: number
  sleep_idle_seconds: number
  device?: string
  fit?: string
  kv_unified?: boolean
  cache_reuse?: number | null
  spec_draft_p_min?: number | null
  parameters?: Record<string, unknown>
}
