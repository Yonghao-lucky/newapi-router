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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, it, vi } from 'vitest'

import { PageFooterProvider } from '@/components/layout/components/page-footer'
import { api } from '@/lib/api'

import { ChannelConfigTab } from '../channel-config'
import { PolicyDrawer } from '../components/policy-drawer'
import { AI_ROUTER_QUERY_KEYS } from '../constants'
import type {
  AIRouterChannel,
  AIRouterTargetPolicy,
  ChannelRoutingProfile,
} from '../types'

const channel: AIRouterChannel = {
  id: 1,
  name: 'Video A',
  status: 1,
  group: 'default,vip',
  models: 'kimi-k3',
  priority: 90,
  weight: 1,
  referenced_by: [],
  router: {
    router_set: true,
    enabled: true,
    max_retry: 2,
    unknown_fallback: false,
    video_priority: 100,
  },
}

function renderWithClient(children: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        <WithPageFooter>{children}</WithPageFooter>
      </QueryClientProvider>
    ),
  }
}

function WithPageFooter(props: { children: React.ReactNode }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <PageFooterProvider container={container}>
      {props.children}
      <div ref={setContainer} />
    </PageFooterProvider>
  )
}

beforeEach(() => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/group/'
          ? ['default', 'vip']
          : { items: [channel], total: 41, page: 1, page_size: 20 },
    },
  }))
})

it('sends group, status and priority order filters before pagination and resets to the first page', async () => {
  const user = userEvent.setup()
  renderWithClient(<ChannelConfigTab />)
  await screen.findByText('Video A')
  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  await waitFor(() =>
    expect(api.get).toHaveBeenLastCalledWith(
      '/api/ai-router/channels',
      expect.objectContaining({ params: expect.objectContaining({ page: 2 }) })
    )
  )
  await user.click(screen.getByRole('button', { name: 'Group' }))
  await user.click(screen.getByRole('option', { name: 'vip' }))
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(api.get).toHaveBeenLastCalledWith(
      '/api/ai-router/channels',
      expect.objectContaining({
        params: expect.objectContaining({ page: 1, group_id: 'vip' }),
      })
    )
  )
  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  await user.click(screen.getByRole('button', { name: 'Status' }))
  await user.click(screen.getByRole('option', { name: 'Auto Disabled' }))
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(api.get).toHaveBeenLastCalledWith(
      '/api/ai-router/channels',
      expect.objectContaining({
        params: expect.objectContaining({
          page: 1,
          group_id: 'vip',
          status: '3',
        }),
      })
    )
  )
  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  await user.click(screen.getByRole('button', { name: 'Video priority order' }))
  await waitFor(() =>
    expect(api.get).toHaveBeenLastCalledWith(
      '/api/ai-router/channels',
      expect.objectContaining({
        params: expect.objectContaining({
          page: 1,
          group_id: 'vip',
          status: '3',
          order: 'video_priority',
        }),
      })
    )
  )
  await user.click(screen.getByRole('button', { name: 'Reset' }))
  await waitFor(() =>
    expect(api.get).toHaveBeenLastCalledWith(
      '/api/ai-router/channels',
      expect.objectContaining({
        params: expect.objectContaining({
          page: 1,
          group_id: undefined,
          status: undefined,
        }),
      })
    )
  )
})

it.each([0, null])(
  'refreshes saved video priority %s in the list and reopened drawer and invalidates policy details',
  async (priority) => {
    const user = userEvent.setup()
    let saved = structuredClone(channel)
    vi.mocked(api.get).mockImplementation(async (url) => ({
      data: {
        success: true,
        data:
          url === '/api/group/'
            ? ['default', 'vip']
            : {
                items: [structuredClone(saved)],
                total: 1,
                page: 1,
                page_size: 20,
              },
      },
    }))
    vi.spyOn(api, 'put').mockImplementation(async (url, body) => {
      if (url.endsWith('/router')) {
        saved = {
          ...saved,
          router: (body as { router: ChannelRoutingProfile }).router,
        }
      }
      return { data: { success: true, data: {} } }
    })
    const { client } = renderWithClient(<ChannelConfigTab />)
    client.setQueryData([...AI_ROUTER_QUERY_KEYS.targets, 'detail', 7], {
      id: 7,
    })
    const row = await screen.findByRole('row', { name: /Video A/ })
    await user.click(within(row).getByRole('button', { name: 'Configure' }))
    const field = screen.getByRole('spinbutton', { name: 'Video priority' })
    expect(field).toHaveValue(100)
    await user.clear(field)
    if (priority !== null) await user.type(field, String(priority))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    await waitFor(() =>
      expect(
        client.getQueryState([...AI_ROUTER_QUERY_KEYS.targets, 'detail', 7])
          ?.isInvalidated
      ).toBe(true)
    )
    const refreshedRow = screen.getByRole('row', { name: /Video A/ })
    expect(within(refreshedRow).getAllByRole('cell')[5]).toHaveTextContent(
      priority === null ? 'Not set' : '0'
    )
    await user.click(
      within(refreshedRow).getByRole('button', { name: 'Configure' })
    )
    expect(
      screen.getByRole('spinbutton', { name: 'Video priority' })
    ).toHaveValue(priority)
  }
)

it('sorts full candidate rows in ordinary and video order without changing the candidate being edited', async () => {
  const user = userEvent.setup()
  const entries = [
    { id: 1, name: 'Video A', priority: 90, video_priority: 0 },
    { id: 2, name: 'Video B', priority: 70, video_priority: -10 },
    { id: 3, name: 'Ordinary C', priority: 100, video_priority: null },
    { id: 4, name: 'Ordinary D', priority: 80, video_priority: null },
  ]
  const policy: AIRouterTargetPolicy = {
    id: 7,
    model: 'kimi-k3',
    group_id: 'default',
    enabled: true,
    max_attempts: 4,
    created_at: 0,
    updated_at: 0,
    candidates: entries.map((entry) => ({
      id: entry.id,
      policy_id: 7,
      channel_id: entry.id,
      enabled: true,
      channel: {
        ...entry,
        status: 1,
        group: 'default',
        models: 'kimi-k3',
        weight: 1,
        router_enabled: true,
        router_set: true,
        max_retry: 2,
        supports_text: true,
        supports_image: true,
        supports_video: false,
        unknown_fallback: false,
        tool_support: 'supported',
      },
    })),
  }
  vi.mocked(api.get).mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/targets/7')
        ? policy
        : { items: [channel], total: 1, page: 1, page_size: 100 },
    },
  }))
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, data: { policy, issues: [], blocking: false } },
  })
  renderWithClient(
    <PolicyDrawer open currentRow={policy} onOpenChange={vi.fn()} />
  )
  await screen.findByText('Ordinary C')
  const ordinaryRows = within(screen.getByRole('table'))
    .getAllByRole('row')
    .slice(1)
  expect(
    ordinaryRows.map((row) => within(row).getAllByRole('cell')[0].textContent)
  ).toEqual(['Ordinary C#3', 'Video A#1', 'Ordinary D#4', 'Video B#2'])
  await user.click(screen.getByRole('button', { name: 'Video priority order' }))
  const videoRows = within(screen.getByRole('table'))
    .getAllByRole('row')
    .slice(1)
  expect(
    videoRows.map((row) => within(row).getAllByRole('cell')[0].textContent)
  ).toEqual(['Video A#1', 'Video B#2', 'Ordinary C#3', 'Ordinary D#4'])
  await user.click(within(videoRows[1]).getByRole('switch'))
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith(
      '/api/ai-router/targets/7',
      expect.objectContaining({
        candidates: [
          { channel_id: 1, enabled: true },
          { channel_id: 2, enabled: false },
          { channel_id: 3, enabled: true },
          { channel_id: 4, enabled: true },
        ],
      }),
      expect.anything()
    )
  )
})

it('loads later picker pages and displays newly added candidates with their current priority', async () => {
  const user = userEvent.setup()
  const firstPage = Array.from({ length: 100 }, (_, index) => ({
    ...channel,
    id: index + 1,
    name: `Channel ${index + 1}`,
  }))
  const laterChannel = {
    ...channel,
    id: 101,
    name: 'Later channel',
    priority: 200,
    router: { ...channel.router, video_priority: -5 },
  }
  vi.mocked(api.get).mockImplementation(async (_url, config) => ({
    data: {
      success: true,
      data: {
        items: config?.params?.page === 2 ? [laterChannel] : firstPage,
        total: 101,
        page: config?.params?.page ?? 1,
        page_size: 100,
      },
    },
  }))
  renderWithClient(
    <PolicyDrawer open currentRow={null} onOpenChange={vi.fn()} />
  )
  const picker = screen.getByPlaceholderText('Add channel...')
  await user.click(picker)
  await user.type(picker, 'Later channel')
  await user.click(
    await screen.findByRole('option', { name: 'Later channel (#101)' })
  )
  const row = screen.getByRole('row', { name: /Later channel/ })
  expect(within(row).getAllByRole('cell')[2]).toHaveTextContent('200')
  expect(within(row).getAllByRole('cell')[3]).toHaveTextContent('-5')
  expect(screen.queryByText(/Deleted channel/)).not.toBeInTheDocument()
})

it('keeps ordinary order when video priorities are unset and lists deleted candidates last', async () => {
  const user = userEvent.setup()
  const info = {
    status: 1,
    group: 'default',
    models: 'kimi-k3',
    weight: 1,
    router_enabled: true,
    router_set: true,
    max_retry: 2,
    supports_text: true,
    supports_image: true,
    supports_video: false,
    unknown_fallback: false,
    tool_support: 'supported',
    video_priority: null,
  }
  const policy: AIRouterTargetPolicy = {
    id: 0,
    model: 'kimi-k3',
    group_id: 'default',
    enabled: true,
    max_attempts: 3,
    created_at: 0,
    updated_at: 0,
    candidates: [
      { id: 1, policy_id: 0, channel_id: 1, enabled: true, channel: null },
      {
        id: 2,
        policy_id: 0,
        channel_id: 2,
        enabled: true,
        channel: { ...info, id: 2, name: 'Low', priority: 10 },
      },
      {
        id: 3,
        policy_id: 0,
        channel_id: 3,
        enabled: true,
        channel: { ...info, id: 3, name: 'High', priority: 20 },
      },
    ],
  }
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: { items: [], total: 0, page: 1, page_size: 100 },
    },
  })
  renderWithClient(
    <PolicyDrawer open currentRow={policy} onOpenChange={vi.fn()} />
  )
  await user.click(screen.getByRole('button', { name: 'Video priority order' }))
  const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1)
  expect(
    rows.map((row) => within(row).getAllByRole('cell')[0].textContent)
  ).toEqual(['High#3', 'Low#2', 'Deleted channel (#1)#1'])
})
