/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or (at your
option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { MonitoringTab } from '../monitoring'

vi.mock('@visactor/react-vchart', () => ({
  VChart: () => <div data-testid='chart' />,
}))

vi.mock('@/features/dashboard/components/ui/stat-card', () => ({
  StatCard: (props: { title: string; value: string | number }) => (
    <output aria-label={props.title}>{props.value}</output>
  ),
}))

function responseFor(url: string, category: string | undefined) {
  if (url === '/api/ai-router/stats') {
    return {
      total_requests: category === 'text_image' ? 0 : 10,
      success_count: 8,
      failure_count: 2,
      success_rate: 0.8,
      fallback_count: 2,
      fallback_rate: 0.2,
      avg_duration_ms: 100,
      avg_ttft_ms: 20,
      avg_attempts: 1.2,
      by_modality: {},
    }
  }
  if (url === '/api/ai-router/stats/errors') {
    return { by_category: [], by_status_code: [] }
  }
  return []
}

function renderMonitoring() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <MonitoringTab />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => ({
    data: {
      success: true,
      data: responseFor(
        url,
        (config?.params as { request_category?: string } | undefined)
          ?.request_category
      ),
    },
  }))
})

describe('AI Router monitoring request category', () => {
  it('sends the selected category with the shared model and group filters to all four endpoints', async () => {
    const user = userEvent.setup()
    renderMonitoring()
    await screen.findByRole('group', { name: 'Request category' })

    const inputs = screen.getAllByRole('textbox')
    await user.type(inputs[0], 'kimi-k3')
    await user.type(inputs[1], 'premium')
    await user.click(screen.getByRole('button', { name: 'Video' }))

    await waitFor(() => {
      const categoryCalls = vi
        .mocked(api.get)
        .mock.calls.filter(
          ([, config]) => config?.params?.request_category === 'video'
        )
      expect(categoryCalls).toHaveLength(4)
      expect(categoryCalls.map(([url]) => url).sort()).toEqual(
        [
          '/api/ai-router/stats',
          '/api/ai-router/stats/channels',
          '/api/ai-router/stats/errors',
          '/api/ai-router/stats/trend',
        ].sort()
      )
      for (const [, config] of categoryCalls) {
        expect(config?.params).toMatchObject({
          model: 'kimi-k3',
          group_id: 'premium',
          request_category: 'video',
        })
      }
    })
  })

  it('keeps the latest category visible when an earlier category resolves later', async () => {
    let resolveVideo: (() => void) | undefined
    vi.mocked(api.get).mockImplementation(async (url, config) => {
      const category = config?.params?.request_category as string | undefined
      if (url === '/api/ai-router/stats' && category === 'video') {
        await new Promise<void>((resolve) => {
          resolveVideo = resolve
        })
      }
      return {
        data: {
          success: true,
          data: responseFor(url, category),
        },
      }
    })
    const user = userEvent.setup()
    renderMonitoring()
    await screen.findByLabelText('Requests')

    await user.click(screen.getByRole('button', { name: 'Video' }))
    await user.click(screen.getByRole('button', { name: 'Text and images' }))
    await waitFor(() =>
      expect(screen.getByLabelText('Requests')).toHaveTextContent('0')
    )

    resolveVideo?.()
    await waitFor(() =>
      expect(screen.getByLabelText('Requests')).toHaveTextContent('0')
    )
    expect(
      screen.getByText('No channel activity in the selected range.')
    ).toBeVisible()
  })
})
