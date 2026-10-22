// 与后端 internal/model/manager.go 及 handlers 响应结构对齐

export type AdminRole = 'super_admin' | 'admin'
export type AdminStatus = 'active' | 'disabled'

export interface SessionResult {
  admin_id: number
  username: string
  role: AdminRole
  display_name: string
  access_token: string
  refresh_token: string
  access_expires_in: number // 秒
  has_passkey: boolean
  has_totp: boolean
}

export interface PendingTOTP {
  token: string
  ttl: number
}

export interface Passkey {
  credential_id: string
  name: string
  created_at: string
  last_used_at: string | null
}

export interface MeInfo {
  id: number
  username: string
  role: AdminRole
  display_name: string
  avatar: string
  last_login_at: string | null
  has_totp: boolean
  has_passkey: boolean
  passkeys: Passkey[]
}

export interface AuthSession {
  id: number
  admin_id: number
  device: string
  ip: string
  user_agent: string
  issued_at: string
  expires_at: string
  last_seen_at: string | null
  revoked_at: string | null
}

export interface Admin {
  id: number
  username: string
  role: AdminRole
  status: AdminStatus
  display_name: string
  avatar: string
  last_login_at: string | null
  created_at: string
}

export interface LLMProvider {
  id: number
  name: string
  base_url: string
  model: string
  max_tokens: number
  temperature: number
  in_price_per_m: number
  out_price_per_m: number
  enabled: boolean
  is_active: boolean
  priority: number
  has_api_key: boolean
}

export interface UsageSummaryRow {
  dimension: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_cents: number
}

export interface UsagePoint {
  ts: string
  total_tokens: number
  cost_cents: number
  calls: number
}

export type BTNodeType = 'selector' | 'sequence' | 'condition' | 'action' | 'subtree' | 'custom'

export interface BTNode {
  type: BTNodeType
  name: string
  condition?: string
  pipeline_id?: string
  subtree_id?: string
  children?: BTNode[]
}

export interface PipelineView {
  id: string
  pass_ids: string[]
  readonly: boolean
}

export interface PassView {
  id: string
  type_name: string
}

export interface SubtreeView {
  id: string
  node: BTNode
}

export interface ConduitSnapshot {
  behavior_tree: BTNode | null
  pipelines: PipelineView[]
  passes: PassView[]
  subtrees: SubtreeView[]
  conditions: string[]
  generated_at: string
}

export interface ConduitTrace {
  id: number
  trace_id: string
  message_id: string
  user_id: string
  group_id: string
  platform: string
  pipeline: string
  status: string // ok / error
  err_msg: string
  duration_ms: number
  trace: unknown // conduit.TraceSpan JSON 树
  created_at: string
}

export interface NodeTraffic {
  id: number
  bucket: string
  pipeline_id: string
  node_name: string
  count: number
  error_count: number
  total_duration_ms: number
}

export interface ConfigRevision {
  id: number
  scope: string
  name: string
  comment: string
  author_name: string
  created_at: string
}

export interface AuditLog {
  id: number
  admin_id: number | null
  username: string
  action: string
  target_type: string
  target_id: string
  detail: string
  ip: string
  result: string // ok / deny / error
  created_at: string
}

export interface DashboardStats {
  today: {
    messages_processed: number
    messages_error: number
    llm_calls: number
    cost_cents: number
  }
  runtime: {
    active_provider: string
    active_model: string
    provider_count: number
    plugin_count: number
    admin_count: number
    engine_running: boolean
    queue_len: number
  }
  server_time: string
}

export interface Page<T> {
  items: T[]
  total: number
}

export interface ApiErrorBody {
  error: string
  code?: string
  retry_after?: number
}

export interface GroupView {
  group_id: string
  platform: string
  has_config: boolean
  bot_enabled: boolean | null
  topic_enabled: boolean | null
  credit_enabled: boolean | null
  whitelist: string[]
  blacklist: string[]
  welcome_msg: string
  remark?: string
}

export interface UserView {
  id: number
  platform: string
  platform_user_id: string
  nickname: string
  banned_at?: string | null
  ban_reason?: string
  created_at: string
}

export interface KnowledgeBaseView {
  id: string
  name: string
  description: string
  provider: string
  enabled: boolean
  chunks: number
}

export interface KnowledgeChunk {
  id: number
  knowledge_base_id: string
  provider: string
  source_id: string
  title: string
  content: string
  meta: unknown
  created_at: string
  updated_at: string
}

export interface KnowledgeFile {
  name: string
  size: number
  updated_at: string
}

export interface MemoryView {
  id: number
  user_id: number
  group_id: string
  content: string
  created_at: string
}

export interface PluginView {
  plugin_id: string
  name: string
  description: string
  version: string
  kind: 'builtin' | 'wasm'
  state: string
  enabled: boolean
  installation_id: string
  commands: string[]
  subtree_id: string
  tools: string[]
  load_error: string
  created_at: string
  updated_at: string
}

export interface SkillView {
  id: string
  name: string
  description: string
  version: string
  author: string
  tags: string[]
  dir: string
  enabled: boolean
  content_len: number
}

export interface PromptFragmentView {
  id: string
  file: string
  builtin: boolean
  content: string
}

export interface StickerView {
  id: number
  object_key: string
  file_id: string
  tags: string[]
  source: string
  created_at: string
}

export interface CommandView {
  name: string
  description: string
  source: string
}

export interface AntiFraudRecord {
  id: number
  created_at: string
  platform: string
  conn_id: string
  group_id: string
  user_id: string
  nickname: string
  message_id: string
  content: string
  content_hash: string
  label_id: number
  label_key: string
  label_name: string
  confidence: number
  violation_score: number
  probabilities: number[] | null
  expert_weights: { key: string; weight: number }[] | null
  status: string
  repeat_count: number
  last_seen_at: string
  recall_status: string
  recall_error: string
  mute_status: string
  mute_error: string
  mute_seconds: number
  reviewed_by: string
  reviewed_at: string | null
  review_note: string
  notified_at: string | null
}

export interface AntiFraudScoreBucket {
  min: number
  max: number
  count: number
}

export interface AntiFraudLabelCount {
  label_key: string
  label_name: string
  count: number
}

export interface AntiFraudStats {
  pending: number
  today_total: number
  today_auto: number
  action_failed: number
  score_buckets: AntiFraudScoreBucket[] | null
  label_counts: AntiFraudLabelCount[] | null
}
