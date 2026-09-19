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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { VideoLogsTable } from '../logs-table'
import type { VideoLog } from '../types'

const log: VideoLog = {
  id: 1,
  request_id: 'video-request-1',
  user_id: 8,
  model: 'kimi-k3',
  status: 'succeeded',
  error_code: '',
  video_count: 1,
  existing_images: 0,
  extracted_frames: 4,
  video_duration_ms: 12000,
  width: 160,
  height: 96,
  codec: 'h264',
  original_body_bytes: 20000,
  decoded_video_bytes: 15000,
  output_image_bytes: 4000,
  output_body_bytes: 6000,
  queue_ms: 10,
  decode_ms: 2,
  probe_ms: 15,
  analyze_ms: 20,
  select_ms: 1,
  export_ms: 50,
  rewrite_ms: 2,
  total_ms: 100,
  sampler_version: 'adaptive-v1',
  config_version: 7,
  created_at: 150,
}

function renderLogs() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <VideoLogsTable filters={{ start_time: 100, end_time: 200 }} />
    </QueryClientProvider>
  )
}

describe('video processing logs', () => {
  it('shows a useful empty state when no completed requests match the range', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { items: [], total: 0, page: 1, page_size: 20 },
      },
    })
    renderLogs()
    expect(await screen.findByText('No video processing logs')).toBeVisible()
  })
  it('opens structural details with stage timings without rendering extra media fields', async () => {
    vi.spyOn(api, 'get').mockImplementation(async (url) => ({
      data: {
        success: true,
        data: url.endsWith('/logs/1')
          ? {
              ...log,
              prompt: 'private-prompt',
              video_url: 'data:video/mp4;base64,private-media',
            }
          : { items: [log], total: 1, page: 1, page_size: 20 },
      },
    }))
    const user = userEvent.setup()
    renderLogs()
    await user.click(
      await screen.findByRole('button', { name: 'video-request-1' })
    )
    const detail = await screen.findByRole('dialog', {
      name: 'Video processing details',
    })
    expect(await within(detail).findByText('adaptive-v1')).toBeVisible()
    expect(within(detail).getByText('Frame analysis')).toBeVisible()
    expect(
      within(detail).getByRole('button', { name: 'Copy request ID' })
    ).toBeVisible()
    expect(detail).not.toHaveTextContent('private-prompt')
    expect(detail).not.toHaveTextContent('private-media')
  })
  it('requests the next page through shared pagination without changing the filters', async () => {
    vi.spyOn(api, 'get').mockImplementation(async (_url, config) => {
      const page = Number(config?.params?.page ?? 1)
      return {
        data: {
          success: true,
          data: {
            items: [{ ...log, id: page, request_id: `video-request-${page}` }],
            total: 21,
            page,
            page_size: 20,
          },
        },
      }
    })
    const user = userEvent.setup()
    renderLogs()
    await screen.findByRole('button', { name: 'video-request-1' })
    await user.click(screen.getByRole('button', { name: 'Go to next page' }))
    await waitFor(() =>
      expect(api.get).toHaveBeenLastCalledWith('/api/ai-router/video/logs', {
        params: { start_time: 100, end_time: 200, page: 2, page_size: 20 },
      })
    )
    expect(
      await screen.findByRole('button', { name: 'video-request-2' })
    ).toBeVisible()
  })
})
