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
import { beforeEach, expect, it, vi } from 'vitest'

import * as api from '../api'
import { ChannelDrawer } from '../components/channel-drawer'
import { GlobalConfigTab } from '../global-config'
import type { AIRouterChannel } from '../types'

vi.mock('../api', () => ({
  updateAIRouterChannelRouter: vi.fn().mockResolvedValue({}),
  updateAIRouterChannelScheduling: vi.fn().mockResolvedValue({}),
  getAIRouterSettings: vi.fn().mockResolvedValue({
    enabled: true,
    telemetry_enabled: true,
    tool_filter_enabled: false,
    max_attempts: 3,
    retention_days: 7,
    idle_timeout_seconds: 1000,
  }),
  updateAIRouterSettings: vi.fn().mockResolvedValue({}),
}))

const channel: AIRouterChannel = {
  id: 1,
  name: 'test',
  status: 1,
  group: 'default',
  models: 'kimi-k3',
  priority: 10,
  weight: 1,
  router: {
    router_set: true,
    enabled: true,
    max_retry: 2,
    unknown_fallback: false,
  },
  referenced_by: [],
}

function wrapper({ children }: { children: React.ReactNode }) {
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      {children}
    </QueryClientProvider>
  )
}

beforeEach(() => vi.clearAllMocks())

it('leaves unknown model classification off for legacy settings and saves an explicit group scope', async () => {
  const user = userEvent.setup()
  render(<GlobalConfigTab />, { wrapper })
  const groups = await screen.findByRole('combobox', {
    name: 'Unknown model HTTP 400 groups',
  })
  await user.type(groups, 'default')
  await user.keyboard('{Enter}')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({
        unknown_model_400_groups: ['default'],
      })
    )
  )
})

it('configures channel model restoration even without a Router profile and can explicitly follow global settings', async () => {
  const user = userEvent.setup()
  render(
    <ChannelDrawer
      open
      onOpenChange={vi.fn()}
      channel={{ ...channel, router: { ...channel.router, router_set: false } }}
    />,
    { wrapper }
  )
  const models = screen.getByRole('combobox', { name: 'Models to restore' })
  expect(models).toBeEnabled()
  await user.type(models, 'custom-model')
  await user.keyboard('{Enter}')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterChannelRouter).toHaveBeenLastCalledWith(
      1,
      null,
      {
        mode: 'channel',
        models: ['custom-model'],
      },
      undefined
    )
  )
  await waitFor(() =>
    expect(
      screen.getByRole('combobox', { name: 'Restore response model names' })
    ).toBeEnabled()
  )
  await user.click(
    screen.getByRole('combobox', { name: 'Restore response model names' })
  )
  await user.click(
    screen.getByRole('option', { name: 'Follow global settings' })
  )
  expect(
    screen.queryByRole('combobox', { name: 'Models to restore' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterChannelRouter).toHaveBeenLastCalledWith(
      1,
      null,
      {
        mode: 'global',
        models: ['custom-model'],
      },
      undefined
    )
  )
})

it('reads channel model tags back and disables restoration when the final tag is removed', async () => {
  const user = userEvent.setup()
  render(
    <ChannelDrawer
      open
      onOpenChange={vi.fn()}
      channel={{
        ...channel,
        response_model_alias: { mode: 'channel', models: ['kimi-k3'] },
      }}
    />,
    { wrapper }
  )
  const chip = screen
    .getAllByText('kimi-k3')
    .map((element) => element.closest('[data-slot="combobox-chip"]'))
    .find(Boolean)
  if (!chip) throw new Error('Expected the response model chip')
  await user.click(within(chip as HTMLElement).getByRole('button'))
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterChannelRouter).toHaveBeenLastCalledWith(
      1,
      expect.objectContaining({ max_retry: 2 }),
      { mode: 'channel', models: [] },
      undefined
    )
  )
})

it('saves defaults for legacy channels and preserves explicit empty groups', async () => {
  const user = userEvent.setup()
  const view = render(
    <ChannelDrawer open onOpenChange={vi.fn()} channel={channel} />,
    { wrapper }
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterChannelRouter).toHaveBeenCalledWith(
      1,
      expect.objectContaining({
        max_retry: 2,
        retry_status_codes: [429, 500, 503],
        switch_status_codes: [401, 402, 403, 404, 405, 502],
      }),
      { mode: 'channel', models: [] },
      undefined
    )
  )
  view.unmount()
  render(
    <ChannelDrawer
      open
      onOpenChange={vi.fn()}
      channel={{
        ...channel,
        router: {
          ...channel.router,
          retry_status_codes: [],
          switch_status_codes: [],
        },
      }}
    />,
    { wrapper }
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterChannelRouter).toHaveBeenLastCalledWith(
      1,
      expect.objectContaining({
        retry_status_codes: [],
        switch_status_codes: [],
      }),
      { mode: 'channel', models: [] },
      undefined
    )
  )
})

it('removes status tags and rejects overlapping groups', async () => {
  const user = userEvent.setup()
  const view = render(
    <ChannelDrawer open onOpenChange={vi.fn()} channel={channel} />,
    { wrapper }
  )
  const chip = screen.getByText('429').closest('[data-slot="combobox-chip"]')
  if (!chip) throw new Error('Expected the status code chip')
  await user.click(within(chip as HTMLElement).getByRole('button'))
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterChannelRouter).toHaveBeenCalledWith(
      1,
      expect.objectContaining({ retry_status_codes: [500, 503] }),
      { mode: 'channel', models: [] },
      undefined
    )
  )
  view.unmount()
  render(
    <ChannelDrawer
      open
      onOpenChange={vi.fn()}
      channel={{
        ...channel,
        router: {
          ...channel.router,
          retry_status_codes: [503],
          switch_status_codes: [503],
        },
      }}
    />,
    { wrapper }
  )
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  expect(
    screen.getByText('Retry status code groups must not overlap.')
  ).toBeInTheDocument()
})

it('edits and saves the idle timeout independently of retry counts', async () => {
  const user = userEvent.setup()
  render(<GlobalConfigTab />, { wrapper })
  const input = await screen.findByLabelText('No-progress timeout (seconds)')
  expect(input).toHaveValue(1000)
  await user.clear(input)
  await user.type(input, '1200')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterSettings).toHaveBeenCalledWith(
      expect.objectContaining({ idle_timeout_seconds: 1200, max_attempts: 3 })
    )
  )
})

it('defaults response model aliases to disabled and saves a keyboard-created model tag', async () => {
  const user = userEvent.setup()
  render(<GlobalConfigTab />, { wrapper })
  const input = await screen.findByRole('combobox', {
    name: 'Restore response model names',
  })
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ response_model_aliases: [] })
    )
  )
  await waitFor(() => expect(input).toBeEnabled())
  const editable = screen.getByRole('combobox', {
    name: 'Restore response model names',
  })
  await user.type(editable, 'kimi-k3')
  expect(editable).toHaveValue('kimi-k3')
  await user.keyboard('{Enter}')
  expect(
    screen.getByText('kimi-k3').closest('[data-slot="combobox-chip"]')
  ).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({
        response_model_aliases: ['kimi-k3'],
        idle_timeout_seconds: 1000,
        max_attempts: 3,
      })
    )
  )
})

it('reads saved response model aliases and disables rewriting by removing the last tag', async () => {
  vi.mocked(api.getAIRouterSettings).mockResolvedValueOnce({
    enabled: true,
    target_models: ['kimi-k3'],
    unknown_policy: 'full_capability_only',
    telemetry_enabled: true,
    tool_filter_enabled: false,
    max_attempts: 3,
    retention_days: 7,
    idle_timeout_seconds: 1000,
    response_model_aliases: ['kimi-k3'],
  })
  const user = userEvent.setup()
  render(<GlobalConfigTab />, { wrapper })
  const chip = (await screen.findByText('kimi-k3')).closest(
    '[data-slot="combobox-chip"]'
  )
  if (!chip) throw new Error('Expected the response model chip')
  await user.click(within(chip as HTMLElement).getByRole('button'))
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(api.updateAIRouterSettings).toHaveBeenLastCalledWith(
      expect.objectContaining({ response_model_aliases: [] })
    )
  )
})

it.each([
  { initial: undefined, input: '0', expected: 0 },
  { initial: 100, input: '', expected: null },
  { initial: 0, input: '-10', expected: -10 },
])(
  'saves video priority $input independently of native priority',
  async ({ initial, input, expected }) => {
    const user = userEvent.setup()
    render(
      <ChannelDrawer
        open
        onOpenChange={vi.fn()}
        channel={{
          ...channel,
          router: { ...channel.router, video_priority: initial },
        }}
      />,
      { wrapper }
    )
    const field = screen.getByRole('spinbutton', { name: 'Video priority' })
    expect(field).toHaveValue(initial ?? null)
    await user.clear(field)
    if (input) await user.type(field, input)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.updateAIRouterChannelRouter).toHaveBeenCalledWith(
        1,
        expect.objectContaining({ video_priority: expected, max_retry: 2 }),
        { mode: 'channel', models: [] },
        undefined
      )
    )
    expect(api.updateAIRouterChannelScheduling).toHaveBeenCalledWith(1, {
      group: channel.group,
      priority: channel.priority,
      weight: channel.weight,
    })
  }
)
