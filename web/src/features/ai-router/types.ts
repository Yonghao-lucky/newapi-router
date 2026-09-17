/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

// Shared AI Router types. Router settings come from the V1 global option;
// policies/candidates and channel router profiles come from V1.5 Phase 1.

export interface AIRouterSettings {
  enabled: boolean
  target_models: string[]
  max_attempts: number
  unknown_policy: string
  tool_filter_enabled: boolean
  telemetry_enabled: boolean
  retention_days: number
}

// Capability values are tri-state: null means "unset" (channel default).
export interface ChannelRoutingProfile {
  enabled: boolean
  router_set?: boolean
  supports_text?: boolean | null
  supports_image?: boolean | null
  supports_video?: boolean | null
  unknown_fallback: boolean
  tool_support?: string
  max_retry: number
}

export interface TargetPolicyChannelInfo {
  id: number
  name: string
  status: number
  group: string
  models: string
  priority: number
  weight: number
  router_enabled: boolean
  router_set: boolean
  max_retry: number
  supports_text: boolean | null
  supports_image: boolean | null
  supports_video: boolean | null
  unknown_fallback: boolean
  tool_support: string
}

export interface AIRouterTargetCandidate {
  id: number
  policy_id: number
  channel_id: number
  enabled: boolean
  // Present for list/get responses; null when the channel was deleted.
  channel: TargetPolicyChannelInfo | null
}

export interface AIRouterTargetPolicy {
  id: number
  model: string
  group_id: string
  enabled: boolean
  max_attempts: number
  created_at: number
  updated_at: number
  candidates: AIRouterTargetCandidate[]
}

export interface TargetPolicyCandidateInput {
  channel_id: number
  enabled: boolean
}

export interface TargetPolicyInput {
  model: string
  group_id: string
  enabled: boolean
  max_attempts: number
  candidates: TargetPolicyCandidateInput[]
}

export interface AIRouterValidationIssue {
  level: 'error' | 'warning'
  code: string
  message: string
}

export interface AIRouterValidationResult {
  issues: AIRouterValidationIssue[]
  blocking: boolean
}

export interface AIRouterPolicyRef {
  policy_id: number
  model: string
  group_id: string
  enabled: boolean
}

export interface AIRouterChannel {
  id: number
  name: string
  status: number
  group: string
  models: string
  priority: number
  weight: number
  router: ChannelRoutingProfile
  referenced_by: AIRouterPolicyRef[]
}

export interface AIRouterSchedulingInput {
  group?: string
  priority?: number
  weight?: number
}

export interface AIRouterLog {
  id: number
  request_id: string
  user_id: number
  group_id: string
  model: string
  modality: string
  has_tools: boolean
  unknown_types: string
  attempts: number
  max_attempts: number
  // JSON array of channel ids in attempt order, e.g. "[4,9,10]".
  selected_channels: string
  final_channel_id: number
  success: boolean
  ttft_ms: number
  total_duration_ms: number
  error_message: string
  created_at: number
}

export interface AIRouterStats {
  total_requests: number
  success_count: number
  failure_count: number
  success_rate: number
  fallback_count: number
  fallback_rate: number
  avg_duration_ms: number
  avg_ttft_ms: number
  avg_attempts: number
  by_modality: Record<string, number>
}

export interface AIRouterTrendPoint {
  time: number
  total: number
  success: number
  failure: number
}

export interface AIRouterChannelStat {
  channel_id: number
  attempts: number
  finals: number
  success_finals: number
  failure_finals: number
  success_rate: number
  avg_duration_ms: number
  avg_ttft_ms: number
  // Phase 3 per-attempt error counts; zero for channels whose traffic
  // predates attempt telemetry.
  count_429: number
  count_5xx: number
  count_timeout: number
  count_connection: number
  count_stream_interrupted: number
  count_other: number
}

// Error-analysis charts: failed attempts by category and by upstream HTTP
// status. Attempts that never got a response carry no status code, so the
// status chart can hold fewer rows than the category chart.
export interface AIRouterErrorCategoryStat {
  error_category: string
  count: number
}

export interface AIRouterStatusCodeStat {
  http_status_code: number
  count: number
}

export interface AIRouterErrorDistribution {
  by_category: AIRouterErrorCategoryStat[]
  by_status_code: AIRouterStatusCodeStat[]
}

export interface AIRouterPage<T> {
  total: number
  page: number
  page_size: number
  items: T[]
}

// Shared filter shape for the logs/monitoring toolbars. Times are unix
// seconds; empty strings mean "not set".
export interface AIRouterLogFilters {
  start_time: string
  end_time: string
  model: string
  group_id: string
  user_id: string
  modality: string
  success: string
  fallback: string
  channel_id: string
  // Phase 3 error filters: exact status code, category enum and the
  // stream-interruption flag; empty string = not set.
  http_status_code: string
  error_category: string
  stream_interrupted: string
}

// Per-attempt row of the request-log detail drawer.
export interface AIRouterAttemptLog {
  id: number
  request_log_id: number
  request_id: string
  attempt_seq: number
  channel_retry_no: number
  channel_id: number
  channel_name: string
  modality: string
  http_status_code: number | null
  error_category: string
  retryable: boolean
  duration_ms: number
  ttft_ms: number | null
  success: boolean
  created_at: number
}

// Response of GET /api/ai-router/logs/:id.
export interface AIRouterLogDetail {
  log: AIRouterLog
  attempts: AIRouterAttemptLog[]
}
