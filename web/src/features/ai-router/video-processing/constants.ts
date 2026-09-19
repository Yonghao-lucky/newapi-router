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
export const numericFields = [
  {
    key: 'max_videos_per_request',
    labelKey: 'Videos per request',
    min: 1,
    max: 16,
    step: 1,
    group: 'Input limits',
  },
  {
    key: 'max_decoded_video_mb',
    labelKey: 'Video size limit (MB)',
    min: 1,
    max: 1024,
    step: 1,
    group: 'Input limits',
  },
  {
    key: 'max_duration_seconds',
    labelKey: 'Video duration limit (seconds)',
    min: 1,
    max: 21600,
    step: 1,
    group: 'Input limits',
  },
  {
    key: 'max_width',
    labelKey: 'Maximum width',
    min: 64,
    max: 16384,
    step: 1,
    group: 'Input limits',
  },
  {
    key: 'max_height',
    labelKey: 'Maximum height',
    min: 64,
    max: 16384,
    step: 1,
    group: 'Input limits',
  },
  {
    key: 'max_pixels',
    labelKey: 'Maximum pixels',
    min: 4096,
    max: 268435456,
    step: 1,
    group: 'Input limits',
  },
  {
    key: 'max_fps',
    labelKey: 'Maximum frame rate',
    min: 1,
    max: 240,
    step: 1,
    group: 'Input limits',
  },
  {
    key: 'probe_timeout_seconds',
    labelKey: 'Probe timeout (seconds)',
    min: 1,
    max: 120,
    step: 1,
    group: 'Input limits',
  },
  {
    key: 'max_images_per_request',
    labelKey: 'Images per request',
    min: 1,
    max: 200,
    step: 1,
    group: 'Frame selection',
  },
  {
    key: 'default_max_frames',
    labelKey: 'Default frame limit',
    min: 1,
    max: 200,
    step: 1,
    group: 'Frame selection',
  },
  {
    key: 'high_detail_max_frames',
    labelKey: 'High detail frame limit',
    min: 1,
    max: 200,
    step: 1,
    group: 'Frame selection',
  },
  {
    key: 'min_frames_per_video',
    labelKey: 'Minimum frames per video',
    min: 1,
    max: 200,
    step: 1,
    group: 'Frame selection',
  },
  {
    key: 'reserve_image_slots',
    labelKey: 'Reserved image slots',
    min: 0,
    max: 199,
    step: 1,
    group: 'Frame selection',
  },
  {
    key: 'jpeg_quality',
    labelKey: 'JPEG quality',
    min: 40,
    max: 95,
    step: 1,
    group: 'Output quality',
  },
  {
    key: 'max_long_side',
    labelKey: 'Maximum image long side',
    min: 256,
    max: 4096,
    step: 1,
    group: 'Output quality',
  },
  {
    key: 'max_total_image_mb',
    labelKey: 'Total image limit (MB)',
    min: 1,
    max: 256,
    step: 1,
    group: 'Output quality',
  },
  {
    key: 'max_rewritten_body_mb',
    labelKey: 'Rewritten body limit (MB)',
    min: 1,
    max: 512,
    step: 1,
    group: 'Output quality',
  },
  {
    key: 'max_running_jobs',
    labelKey: 'Concurrent jobs',
    min: 1,
    max: 256,
    step: 1,
    group: 'Concurrency and queue',
  },
  {
    key: 'ffmpeg_threads_per_job',
    labelKey: 'FFmpeg threads per job',
    min: 1,
    max: 32,
    step: 1,
    group: 'Concurrency and queue',
  },
  {
    key: 'max_waiting_jobs',
    labelKey: 'Waiting jobs limit',
    min: 0,
    max: 10000,
    step: 1,
    group: 'Concurrency and queue',
  },
  {
    key: 'max_waiting_bytes_mb',
    labelKey: 'Waiting body limit (MB)',
    min: 1,
    max: 1048576,
    step: 1,
    group: 'Concurrency and queue',
  },
  {
    key: 'max_wait_seconds',
    labelKey: 'Queue timeout (seconds)',
    min: 1,
    max: 3600,
    step: 1,
    group: 'Concurrency and queue',
  },
  {
    key: 'max_job_seconds',
    labelKey: 'Processing timeout (seconds)',
    min: 1,
    max: 3600,
    step: 1,
    group: 'Concurrency and queue',
  },
  {
    key: 'max_running_per_user',
    labelKey: 'Concurrent jobs per user',
    min: 1,
    max: 256,
    step: 1,
    group: 'Concurrency and queue',
  },
  {
    key: 'max_waiting_per_user',
    labelKey: 'Waiting jobs per user',
    min: 0,
    max: 10000,
    step: 1,
    group: 'Concurrency and queue',
  },
  {
    key: 'max_estimated_wait_seconds',
    labelKey: 'Estimated wait limit (seconds)',
    min: 1,
    max: 3600,
    step: 1,
    group: 'Concurrency and queue',
  },
  {
    key: 'max_temp_storage_gb',
    labelKey: 'Temporary storage limit (GB)',
    min: 1,
    max: 4096,
    step: 1,
    group: 'Storage and cleanup',
  },
  {
    key: 'stale_file_ttl_minutes',
    labelKey: 'Stale file lifetime (minutes)',
    min: 5,
    max: 1440,
    step: 1,
    group: 'Storage and cleanup',
  },
  {
    key: 'retention_days',
    labelKey: 'Log retention days',
    min: 1,
    max: 90,
    step: 1,
    group: 'Logs and retention',
  },
  {
    key: 'analysis_width',
    labelKey: 'Analysis width',
    min: 64,
    max: 1024,
    step: 1,
    group: 'Advanced sampling',
  },
  {
    key: 'candidate_limit',
    labelKey: 'Candidate frame limit',
    min: 16,
    max: 50000,
    step: 1,
    group: 'Advanced sampling',
  },
  {
    key: 'short_scan_fps',
    labelKey: 'Short video scan FPS',
    min: 0.1,
    max: 30,
    step: 0.1,
    group: 'Advanced sampling',
  },
  {
    key: 'medium_scan_fps',
    labelKey: 'Medium video scan FPS',
    min: 0.1,
    max: 30,
    step: 0.1,
    group: 'Advanced sampling',
  },
  {
    key: 'long_scan_fps',
    labelKey: 'Long video scan FPS',
    min: 0.05,
    max: 10,
    step: 0.05,
    group: 'Advanced sampling',
  },
  {
    key: 'min_selected_gap_ms',
    labelKey: 'Minimum frame gap (ms)',
    min: 0,
    max: 60000,
    step: 1,
    group: 'Advanced sampling',
  },
  {
    key: 'scene_weight',
    labelKey: 'Scene change weight',
    min: 0,
    max: 1,
    step: 0.01,
    group: 'Advanced sampling',
  },
  {
    key: 'motion_weight',
    labelKey: 'Motion weight',
    min: 0,
    max: 1,
    step: 0.01,
    group: 'Advanced sampling',
  },
  {
    key: 'sharpness_weight',
    labelKey: 'Sharpness weight',
    min: 0,
    max: 1,
    step: 0.01,
    group: 'Advanced sampling',
  },
  {
    key: 'temporal_weight',
    labelKey: 'Time coverage weight',
    min: 0,
    max: 1,
    step: 0.01,
    group: 'Advanced sampling',
  },
  {
    key: 'phash_duplicate_threshold',
    labelKey: 'Duplicate frame threshold',
    min: 0,
    max: 1,
    step: 0.01,
    group: 'Advanced sampling',
  },
  {
    key: 'black_frame_threshold',
    labelKey: 'Black frame threshold',
    min: 0,
    max: 1,
    step: 0.01,
    group: 'Advanced sampling',
  },
] as const

export type NumericSettingKey = (typeof numericFields)[number]['key']

export const settingsGroups = [
  'Input limits',
  'Frame selection',
  'Output quality',
  'Concurrency and queue',
  'Storage and cleanup',
  'Logs and retention',
  'Advanced sampling',
] as const

export const booleanFields = [
  {
    key: 'enabled',
    labelKey: 'Enable video processing',
    group: 'Video processing scope',
  },
  {
    key: 'include_timestamps',
    labelKey: 'Include frame timestamps',
    group: 'Output quality',
  },
  {
    key: 'telemetry_enabled',
    labelKey: 'Record processing logs',
    group: 'Logs and retention',
  },
  {
    key: 'record_per_video_details',
    labelKey: 'Record per-video metadata',
    group: 'Logs and retention',
  },
] as const

export const statusLabels = {
  succeeded: 'Success',
  failed: 'Failed',
  rejected: 'Rejected',
  canceled: 'Canceled',
  timed_out: 'Timed out',
} as const

export const settingLabels: Record<string, string> = {
  ...Object.fromEntries(
    [...numericFields, ...booleanFields].map((field) => [
      field.key,
      field.labelKey,
    ])
  ),
  target_models: 'Target Models',
  allowed_mime_types: 'Allowed video formats',
  allowed_codecs: 'Allowed video codecs',
  mode: 'Video handling',
}
export const validationLabels: Record<string, string> = {
  mode_invalid: 'Choose a supported video handling mode',
  target_models_required: 'Choose at least one model',
  mime_types_required: 'Choose at least one format',
  mime_type_invalid: 'Choose a supported video format',
  codecs_required: 'Choose at least one codec',
  reserve_slots_out_of_range: 'Leave at least one image slot for video',
  value_not_finite: 'Enter a finite number',
  frame_budget_exceeds_image_limit:
    'Frame limits must fit the available image slots',
  frame_order_invalid: 'Default frames must not exceed high detail frames',
  min_frames_exceeds_default: 'Minimum frames must not exceed default frames',
  weights_sum_invalid: 'Sampling weights must sum to one',
  user_running_limit_exceeds_global:
    'Per-user limits must not exceed global limits',
  user_waiting_limit_exceeds_global:
    'Per-user limits must not exceed global limits',
  user_waiting_limit_without_queue:
    'Disable per-user waiting when the queue is disabled',
  rewritten_body_exceeds_request_limit:
    'The rewritten body limit must not exceed the gateway request limit.',
  temp_storage_exceeds_disk:
    'The temporary storage limit exceeds this node’s physical disk capacity.',
}
