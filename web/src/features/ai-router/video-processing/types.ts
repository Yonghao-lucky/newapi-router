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
import type { VideoSettings } from './lib/settings'

export type { VideoSettings }
export interface VideoSettingsDocument {
  settings: VideoSettings
  defaults: VideoSettings
  version: number
  config_version: number
  deployment: {
    ffmpeg_path: string
    ffprobe_path: string
    temp_dir: string
    hwaccel_enabled: boolean
  }
}
export interface VideoValidationIssue {
  level: 'error' | 'warning'
  code: string
  field?: string
  message: string
}
export interface VideoValidation {
  issues: VideoValidationIssue[]
  blocking: boolean
}
export interface VideoRuntime {
  available: boolean
  reason?: string
  enabled: boolean
  config_version: number
  config_applied_at: number
  ffmpeg_available: boolean
  ffprobe_available: boolean
  completed_last_1m: number
  completed_last_5m: number
  completed_last_15m: number
  pending_logs: number
  dropped_logs: number
  disk: { total_bytes: number; free_bytes: number }
  queue: {
    running_jobs: number
    max_running_jobs: number
    waiting_jobs: number
    max_waiting_jobs: number
    waiting_bytes: number
    max_waiting_bytes: number
    active_ffmpeg: number
    temp_storage_bytes: number
    temp_storage_limit_bytes: number
    oldest_wait_ms: number
    queue_timeout_ms: number
    job_timeout_ms: number
  }
}
export interface VideoMedia {
  index: number
  duration_ms: number
  width: number
  height: number
  codec: string
  frames: number
  decoded_bytes: number
  image_bytes: number
  sampler_version: string
}
export interface VideoLog {
  id: number
  request_id: string
  user_id: number
  model: string
  status: 'succeeded' | 'failed' | 'rejected' | 'canceled' | 'timed_out'
  error_code: string
  video_count: number
  existing_images: number
  extracted_frames: number
  video_duration_ms: number
  width: number
  height: number
  codec: string
  original_body_bytes: number
  decoded_video_bytes: number
  output_image_bytes: number
  output_body_bytes: number
  queue_ms: number
  decode_ms: number
  probe_ms: number
  analyze_ms: number
  select_ms: number
  export_ms: number
  rewrite_ms: number
  total_ms: number
  sampler_version: string
  config_version: number
  created_at: number
  videos?: VideoMedia[]
}
export interface VideoFilters {
  start_time: number
  end_time: number
  model?: string
  request_id?: string
  user_id?: number
  status?: string
  error_code?: string
  codec?: string
  min_duration_ms?: number
  max_duration_ms?: number
  min_frames?: number
  max_frames?: number
}
export interface VideoLogPage {
  items: VideoLog[]
  total: number
  page: number
  page_size: number
}
export interface VideoPercentiles {
  p50: number
  p95: number
  p99: number
}
export interface VideoTrend {
  time: number
  total: number
  success: number
  failure: number
  rejected: number
  queue_ms: number
  total_ms: number
}
export interface VideoStats {
  total_requests: number
  success_count: number
  failure_count: number
  rejected_count: number
  queue: VideoPercentiles
  total: VideoPercentiles
  stage_ms: Record<string, number>
  by_codec: Record<string, number>
  by_duration: Record<string, number>
  by_frames: Record<string, number>
  by_error: Record<string, number>
  original_body_bytes: number
  output_body_bytes: number
  output_image_bytes: number
  trend: VideoTrend[]
}
