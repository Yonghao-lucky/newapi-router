import { api } from '@/lib/api'

import type { AIRouterLog, AIRouterSettings, AIRouterStats } from './types'

export async function getAIRouterSettings(): Promise<AIRouterSettings> {
  const res = await api.get<{ success: boolean; data: AIRouterSettings }>('/api/ai-router/settings')
  return res.data.data
}

export async function updateAIRouterSettings(
  settings: Partial<AIRouterSettings>
): Promise<AIRouterSettings> {
  const res = await api.put<{ success: boolean; data: AIRouterSettings }>(
    '/api/ai-router/settings',
    settings
  )
  return res.data.data
}

export async function getAIRouterLogs(params: {
  page?: number
  page_size?: number
  start_time?: number | string
  end_time?: number | string
  model?: string
  user_id?: number
  group_id?: string
  success?: string | boolean
  modality?: string
}): Promise<{ total: number; page: number; page_size: number; items: AIRouterLog[] }> {
  const res = await api.get<{ success: boolean; data: { total: number; page: number; page_size: number; items: AIRouterLog[] } }>(
    '/api/ai-router/logs',
    { params }
  )
  return res.data.data
}

export async function getAIRouterStats(params: {
  start_time: number
  end_time: number
  model?: string
  group_id?: string
}): Promise<AIRouterStats> {
  const res = await api.get<{ success: boolean; data: AIRouterStats }>('/api/ai-router/stats', { params })
  return res.data.data
}
