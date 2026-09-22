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
import { api, type ApiRequestConfig } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  AIRouterChannel,
  AIRouterChannelOrder,
  AIRouterChannelStat,
  AIRouterErrorDistribution,
  AIRouterLog,
  AIRouterLogDetail,
  AIRouterLogFilters,
  AIRouterPage,
  AIRouterSettings,
  AIRouterStats,
  AIRouterTargetPolicy,
  AIRouterTrendPoint,
  AIRouterValidationResult,
  ChannelRoutingProfile,
  TargetPolicyInput,
} from './types'

// Mutations handle their own error presentation, so skip the global business
// error toast for them.
const mutationConfig = (config: ApiRequestConfig = {}): ApiRequestConfig => ({
  ...config,
  skipBusinessError: true,
  skipErrorHandler: true,
})

// ---------------------------------------------------------------------------
// Global settings
// ---------------------------------------------------------------------------

export async function getAIRouterSettings(): Promise<AIRouterSettings> {
  const response = await api.get<{ success: boolean; data: AIRouterSettings }>(
    '/api/ai-router/settings'
  )
  return requireServerSuccess(response.data).data
}

export async function updateAIRouterSettings(
  settings: Partial<AIRouterSettings>
): Promise<AIRouterSettings> {
  const response = await api.put<{ success: boolean; data: AIRouterSettings }>(
    '/api/ai-router/settings',
    settings,
    mutationConfig()
  )
  return requireServerSuccess(response.data).data
}

// ---------------------------------------------------------------------------
// Target policies
// ---------------------------------------------------------------------------

export async function getAIRouterTargetPolicies(params: {
  page?: number
  page_size?: number
  search?: string
  group_id?: string
}): Promise<AIRouterPage<AIRouterTargetPolicy>> {
  const response = await api.get<{
    success: boolean
    data: AIRouterPage<AIRouterTargetPolicy>
  }>('/api/ai-router/targets', { params })
  return requireServerSuccess(response.data).data
}

export async function getAIRouterTargetPolicy(
  id: number
): Promise<AIRouterTargetPolicy> {
  const response = await api.get<{
    success: boolean
    data: AIRouterTargetPolicy
  }>(`/api/ai-router/targets/${id}`)
  return requireServerSuccess(response.data).data
}

export async function createAIRouterTargetPolicy(
  input: TargetPolicyInput
): Promise<{ policy: AIRouterTargetPolicy } & AIRouterValidationResult> {
  const response = await api.post<{
    success: boolean
    data: { policy: AIRouterTargetPolicy } & AIRouterValidationResult
  }>('/api/ai-router/targets', input, mutationConfig())
  return requireServerSuccess(response.data).data
}

export async function updateAIRouterTargetPolicy(
  id: number,
  input: TargetPolicyInput
): Promise<{ policy: AIRouterTargetPolicy } & AIRouterValidationResult> {
  const response = await api.put<{
    success: boolean
    data: { policy: AIRouterTargetPolicy } & AIRouterValidationResult
  }>(`/api/ai-router/targets/${id}`, input, mutationConfig())
  return requireServerSuccess(response.data).data
}

export async function deleteAIRouterTargetPolicy(id: number): Promise<void> {
  const response = await api.delete<{ success: boolean; data: unknown }>(
    `/api/ai-router/targets/${id}`,
    mutationConfig()
  )
  requireServerSuccess(response.data)
}

// Pre-save validation. Never writes.
export async function validateAIRouterTargetPolicy(
  input: TargetPolicyInput
): Promise<AIRouterValidationResult> {
  const response = await api.post<{
    success: boolean
    data: AIRouterValidationResult
  }>(`/api/ai-router/targets/validate`, input, mutationConfig())
  return requireServerSuccess(response.data).data
}

// ---------------------------------------------------------------------------
// Channel router configuration
// ---------------------------------------------------------------------------

export async function getAIRouterChannels(params: {
  page?: number
  page_size?: number
  name?: string
  group_id?: string
  status?: string
  order?: AIRouterChannelOrder
}): Promise<AIRouterPage<AIRouterChannel>> {
  const response = await api.get<{
    success: boolean
    data: AIRouterPage<AIRouterChannel>
  }>('/api/ai-router/channels', { params })
  return requireServerSuccess(response.data).data
}

export async function updateAIRouterChannelRouter(
  id: number,
  router: ChannelRoutingProfile | null
): Promise<{ id: number; router: ChannelRoutingProfile | null }> {
  const response = await api.put<{
    success: boolean
    data: { id: number; router: ChannelRoutingProfile | null }
  }>(`/api/ai-router/channels/${id}/router`, { router }, mutationConfig())
  return requireServerSuccess(response.data).data
}

export async function updateAIRouterChannelScheduling(
  id: number,
  input: { group?: string; priority?: number; weight?: number }
): Promise<{ id: number; group: string; priority: number; weight: number }> {
  const response = await api.put<{
    success: boolean
    data: { id: number; group: string; priority: number; weight: number }
  }>(`/api/ai-router/channels/${id}/scheduling`, input, mutationConfig())
  return requireServerSuccess(response.data).data
}

// ---------------------------------------------------------------------------
// Monitoring: stats, trend, per-channel stats, logs
// ---------------------------------------------------------------------------

export type AIRouterRequestCategory = 'all' | 'video' | 'text_image'

export interface AIRouterStatsParams {
  start_time: number
  end_time: number
  model?: string
  group_id?: string
  request_category?: AIRouterRequestCategory
}

export async function getAIRouterStats(
  params: AIRouterStatsParams
): Promise<AIRouterStats> {
  const response = await api.get<{ success: boolean; data: AIRouterStats }>(
    '/api/ai-router/stats',
    { params }
  )
  return requireServerSuccess(response.data).data
}

export async function getAIRouterTrend(
  params: AIRouterStatsParams
): Promise<AIRouterTrendPoint[]> {
  const response = await api.get<{
    success: boolean
    data: AIRouterTrendPoint[]
  }>('/api/ai-router/stats/trend', { params })
  return requireServerSuccess(response.data).data
}

export async function getAIRouterChannelStats(
  params: AIRouterStatsParams
): Promise<AIRouterChannelStat[]> {
  const response = await api.get<{
    success: boolean
    data: AIRouterChannelStat[]
  }>('/api/ai-router/stats/channels', { params })
  return requireServerSuccess(response.data).data
}

// Failed-attempt distribution for the two error-analysis charts.
export async function getAIRouterErrorStats(
  params: AIRouterStatsParams
): Promise<AIRouterErrorDistribution> {
  const response = await api.get<{
    success: boolean
    data: AIRouterErrorDistribution
  }>('/api/ai-router/stats/errors', { params })
  return requireServerSuccess(response.data).data
}

export async function getAIRouterLogs(params: {
  page?: number
  page_size?: number
  start_time?: number
  end_time?: number
  model?: string
  user_id?: number
  group_id?: string
  success?: string
  modality?: string
  fallback?: string
  channel_id?: number
  http_status_code?: number
  error_category?: string
  stream_interrupted?: string
}): Promise<AIRouterPage<AIRouterLog>> {
  const response = await api.get<{
    success: boolean
    data: AIRouterPage<AIRouterLog>
  }>('/api/ai-router/logs', { params })
  return requireServerSuccess(response.data).data
}

// Full attempt list of one request log (Phase 3 detail endpoint).
export async function getAIRouterLogDetail(
  id: number
): Promise<AIRouterLogDetail> {
  const response = await api.get<{
    success: boolean
    data: AIRouterLogDetail
  }>(`/api/ai-router/logs/${id}`)
  return requireServerSuccess(response.data).data
}

// Convert toolbar filter state into query params, dropping empty values.
export function aiRouterLogFiltersToParams(
  filters: AIRouterLogFilters,
  page: number,
  pageSize: number
): Parameters<typeof getAIRouterLogs>[0] {
  const params: Parameters<typeof getAIRouterLogs>[0] = {
    page,
    page_size: pageSize,
  }
  if (filters.start_time) params.start_time = Number(filters.start_time)
  if (filters.end_time) params.end_time = Number(filters.end_time)
  if (filters.model) params.model = filters.model
  if (filters.group_id) params.group_id = filters.group_id
  if (filters.user_id) params.user_id = Number(filters.user_id)
  if (filters.modality) params.modality = filters.modality
  if (filters.success) params.success = filters.success
  if (filters.fallback) params.fallback = filters.fallback
  if (filters.channel_id) params.channel_id = Number(filters.channel_id)
  if (filters.http_status_code) {
    params.http_status_code = Number(filters.http_status_code)
  }
  if (filters.error_category) params.error_category = filters.error_category
  if (filters.stream_interrupted) {
    params.stream_interrupted = filters.stream_interrupted
  }
  return params
}
