export interface ModelClassification {
  general_name?: string
  architecture?: string
  architecture_type?: string
  parameters?: string
  active_parameters?: string[]
  context_length?: number | null
  confidence?: 'high' | 'medium' | 'low'
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
  llama_version?: string
  ctx_size?: number
  ngl?: number
  concurrency?: number
  gpu?: string
  k_cache_type?: string
  v_cache_type?: string
  batch?: number
  ubatch?: number
  threads?: number
  temp?: number
  flash_attn?: boolean
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
}

export interface ProjectionArtifact {
  id: string
  name: string
  size: number
  relative_dir?: string
}

export interface ModelsResponse {
  schema_version: string
  models: ModelArtifact[]
  current_model: string
  current_model_id: string
  current_config: RuntimeConfig
  server_running: boolean
  mmproj_enabled: boolean
  total_size: string
  disk_free: string
  summary?: { deployable: number; artifacts?: number }
}

export interface Engine {
  key: string
  name: string
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
  reasoning: 'on' | 'off'
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
}
