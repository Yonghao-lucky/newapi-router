export interface AIRouterSettings {
  enabled: boolean
  target_models: string[]
  max_attempts: number
  unknown_policy: string
  tool_filter_enabled: boolean
  telemetry_enabled: boolean
  retention_days: number
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
  avg_duration_ms: number
  avg_ttft_ms: number
  avg_attempts: number
  by_modality: Record<string, number>
}
