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

// Query keys for the AI Router feature. Keep the prefixes stable — mutations
// invalidate by prefix.
export const AI_ROUTER_QUERY_KEYS = {
  settings: ['ai-router-settings'] as const,
  targets: ['ai-router-targets'] as const,
  channels: ['ai-router-channels'] as const,
  stats: ['ai-router-stats'] as const,
  trend: ['ai-router-trend'] as const,
  channelStats: ['ai-router-channel-stats'] as const,
  logs: ['ai-router-logs'] as const,
}

// Cap bounds mirrored from relaykit dto (0–5 extra same-channel retries).
export const MIN_CHANNEL_MAX_RETRY = 0
export const MAX_CHANNEL_MAX_RETRY = 5

export const MIN_TARGET_MAX_ATTEMPTS = 1
export const MAX_TARGET_MAX_ATTEMPTS = 10

// Tool support tri-state values, mirrored from the backend profile.
export const TOOL_SUPPORT_UNVERIFIED = 'unverified'
export const TOOL_SUPPORT_SUPPORTED = 'supported'
export const TOOL_SUPPORT_UNSUPPORTED = 'unsupported'
export const TOOL_SUPPORT_OPTIONS = [
  TOOL_SUPPORT_UNVERIFIED,
  TOOL_SUPPORT_SUPPORTED,
  TOOL_SUPPORT_UNSUPPORTED,
] as const

export const CHANNEL_STATUS_ENABLED = 1
export const CHANNEL_STATUS_MANUALLY_DISABLED = 2
export const CHANNEL_STATUS_AUTO_DISABLED = 3

// Modality options for filters (values match the backend telemetry strings).
export const MODALITY_OPTIONS = ['text', 'image', 'video', 'unknown'] as const

// Success/failure filter options for logs.
export const SUCCESS_FILTER_OPTIONS = ['true', 'false'] as const
export const FALLBACK_FILTER_OPTIONS = ['true', 'false'] as const
