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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  VideoFilters,
  VideoLog,
  VideoLogPage,
  VideoRuntime,
  VideoSettings,
  VideoSettingsDocument,
  VideoStats,
  VideoValidation,
} from './types'

const root = '/api/ai-router/video'
export const videoKeys = {
  settings: ['ai-router', 'video', 'settings'],
  runtime: ['ai-router', 'video', 'runtime'],
  logs: ['ai-router', 'video', 'logs'],
  stats: ['ai-router', 'video', 'stats'],
} as const
interface Result<T> {
  success: boolean
  data: T
}

export async function getVideoSettings(): Promise<VideoSettingsDocument> {
  const response = await api.get<Result<VideoSettingsDocument>>(
    `${root}/settings`
  )
  return requireServerSuccess(response.data).data
}
export async function validateVideoSettings(
  settings: VideoSettings
): Promise<VideoValidation> {
  const response = await api.post<Result<VideoValidation>>(
    `${root}/settings/validate`,
    { settings }
  )
  return requireServerSuccess(response.data).data
}
export async function saveVideoSettings(input: {
  settings: VideoSettings
  expected_version: number
}): Promise<VideoSettingsDocument> {
  const response = await api.put<Result<VideoSettingsDocument>>(
    `${root}/settings`,
    input
  )
  return requireServerSuccess(response.data).data
}
export async function getVideoRuntime(): Promise<VideoRuntime> {
  const response = await api.get<Result<VideoRuntime>>(`${root}/runtime`)
  return requireServerSuccess(response.data).data
}
export async function getVideoLogs(
  params: VideoFilters & { page: number; page_size: number }
): Promise<VideoLogPage> {
  const response = await api.get<Result<VideoLogPage>>(`${root}/logs`, {
    params,
  })
  return requireServerSuccess(response.data).data
}
export async function getVideoLog(id: number): Promise<VideoLog> {
  const response = await api.get<Result<VideoLog>>(`${root}/logs/${id}`)
  return requireServerSuccess(response.data).data
}
export async function getVideoStats(params: VideoFilters): Promise<VideoStats> {
  const response = await api.get<Result<VideoStats>>(`${root}/stats`, {
    params,
  })
  return requireServerSuccess(response.data).data
}
