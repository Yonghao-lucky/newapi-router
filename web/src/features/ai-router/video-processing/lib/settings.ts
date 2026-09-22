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
import { z } from 'zod'

import { numericFields, type NumericSettingKey } from '../constants'

const numericShape = Object.fromEntries(
  numericFields.map((field) => {
    let schema = z
      .number({ error: 'Enter a finite number' })
      .min(field.min, 'Value is below the minimum')
      .max(field.max, 'Value exceeds the maximum')
    if (field.step === 1) schema = schema.int('Enter a whole number')
    return [field.key, schema]
  })
) as Record<NumericSettingKey, z.ZodNumber>

const videoDomainSchema = z
  .string()
  .trim()
  .transform((value, ctx) => {
    const invalid = () => {
      ctx.addIssue({
        code: 'custom',
        message:
          'Enter exact domain names without URLs, ports, IP addresses or wildcards',
      })
      return z.NEVER
    }
    if (!value || /[\s/:?#@*%\\[\]]/.test(value)) return invalid()
    try {
      const hostname = new URL(`https://${value}`).hostname.replace(/\.$/, '')
      if (
        hostname.length > 253 ||
        !hostname.includes('.') ||
        /^\d+\.\d+\.\d+\.\d+$/.test(hostname) ||
        /\.(localhost|local|internal|lan|home)$/.test(hostname) ||
        !hostname
          .split('.')
          .every((label) =>
            /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label)
          )
      ) {
        return invalid()
      }
      return hostname
    } catch {
      return invalid()
    }
  })

export const videoSettingsSchema = z
  .object({
    ...numericShape,
    enabled: z.boolean(),
    remote_url_enabled: z.boolean(),
    remote_url_allowlist_enabled: z.boolean(),
    remote_url_allow_unknown_size: z.boolean(),
    remote_url_allowed_hosts: z.array(videoDomainSchema),
    mode: z.literal('always_frames'),
    target_models: z
      .array(z.string().trim().min(1))
      .min(1, 'Choose at least one model'),
    allowed_mime_types: z
      .array(z.string())
      .min(1, 'Choose at least one format'),
    allowed_codecs: z.array(z.string()).min(1, 'Choose at least one codec'),
    include_timestamps: z.boolean(),
    telemetry_enabled: z.boolean(),
    record_per_video_details: z.boolean(),
  })
  .superRefine((settings, ctx) => {
    const invalid = (field: keyof typeof settings, message: string) =>
      ctx.addIssue({ code: 'custom', path: [field], message })
    if (
      settings.remote_url_enabled &&
      settings.remote_url_allowlist_enabled &&
      settings.remote_url_allowed_hosts.length === 0
    ) {
      invalid(
        'remote_url_allowed_hosts',
        'Add at least one domain before enabling the video allowlist'
      )
    }
    const ceiling =
      settings.max_images_per_request - settings.reserve_image_slots
    if (ceiling < 1) {
      invalid('reserve_image_slots', 'Leave at least one image slot for video')
    }
    if (settings.high_detail_max_frames > ceiling) {
      invalid(
        'high_detail_max_frames',
        'Frame limits must fit the available image slots'
      )
    }
    if (settings.default_max_frames > settings.high_detail_max_frames) {
      invalid(
        'default_max_frames',
        'Default frames must not exceed high detail frames'
      )
    }
    if (settings.min_frames_per_video > settings.default_max_frames) {
      invalid(
        'min_frames_per_video',
        'Minimum frames must not exceed default frames'
      )
    }
    if (settings.max_running_per_user > settings.max_running_jobs) {
      invalid(
        'max_running_per_user',
        'Per-user limits must not exceed global limits'
      )
    }
    if (settings.max_waiting_per_user > settings.max_waiting_jobs) {
      invalid(
        'max_waiting_per_user',
        'Per-user limits must not exceed global limits'
      )
    }
    const sum =
      settings.scene_weight +
      settings.motion_weight +
      settings.sharpness_weight +
      settings.temporal_weight
    if (Math.abs(sum - 1) > 0.000001) {
      invalid('scene_weight', 'Sampling weights must sum to one')
    }
  })

export type VideoSettings = z.infer<typeof videoSettingsSchema>

export function needsVideoConfirmation(
  current: VideoSettings,
  next: VideoSettings
): boolean {
  const resourceLimits: NumericSettingKey[] = [
    'remote_url_max_downloads',
    'remote_url_download_timeout_seconds',
    'max_running_jobs',
    'ffmpeg_threads_per_job',
    'max_waiting_jobs',
    'max_waiting_bytes_mb',
    'max_temp_storage_gb',
    'max_decoded_video_mb',
    'max_duration_seconds',
    'max_width',
    'max_height',
    'max_pixels',
    'max_images_per_request',
    'max_total_image_mb',
    'max_rewritten_body_mb',
    'analysis_width',
    'candidate_limit',
    'max_job_seconds',
  ]
  return (
    (!current.enabled && next.enabled) ||
    (!current.remote_url_enabled && next.remote_url_enabled) ||
    resourceLimits.some((key) => next[key] > current[key]) ||
    next.retention_days < current.retention_days
  )
}
