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
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { VideoSettingsForm } from '../settings'
import type { VideoSettings } from '../types'
import { videoDocument } from './fixture'

function renderSettings(document = videoDocument) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const root = createRootRoute()
  const route = createRoute({
    getParentRoute: () => root,
    path: '/',
    component: () => (
      <VideoSettingsForm
        document={structuredClone(document)}
        onDirtyChange={vi.fn()}
      />
    ),
  })
  const router = createRouter({
    routeTree: root.addChildren([route]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { issues: [], blocking: false } },
  })
  vi.spyOn(api, 'put').mockImplementation(async (_url, input) => ({
    data: {
      success: true,
      data: {
        ...videoDocument,
        version: 8,
        settings: (input as { settings: VideoSettings }).settings,
      },
    },
  }))
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: videoDocument },
  })
})

describe('video settings workflow', () => {
  it('restores server defaults as unsaved changes only after confirmation', async () => {
    const user = userEvent.setup()
    renderSettings({
      ...videoDocument,
      defaults: { ...videoDocument.defaults, max_duration_seconds: 900 },
    })
    const input = await screen.findByLabelText('Video duration limit (seconds)')
    await user.click(screen.getByRole('button', { name: 'Restore defaults' }))
    const dialog = await screen.findByRole('alertdialog', {
      name: 'Discard unsaved changes?',
    })
    expect(input).toHaveValue(1800)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(input).toHaveValue(900))
    expect(screen.getByText('Unsaved changes')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
    expect(api.put).not.toHaveBeenCalled()
  })

  it('validates then saves the complete candidate with its expected version', async () => {
    const user = userEvent.setup()
    renderSettings()
    const input = await screen.findByLabelText('Video duration limit (seconds)')
    await user.clear(input)
    await user.type(input, '1200')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/ai-router/video/settings', {
        expected_version: 7,
        settings: { ...videoDocument.settings, max_duration_seconds: 1200 },
      })
    )
    expect(api.post).toHaveBeenCalledWith(
      '/api/ai-router/video/settings/validate',
      { settings: { ...videoDocument.settings, max_duration_seconds: 1200 } }
    )
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    )
    expect(screen.getByText('Configuration version 8')).toBeVisible()
  })

  it('shows validation findings on the affected field and does not save a blocked candidate', async () => {
    vi.mocked(api.post).mockResolvedValue({
      data: {
        success: true,
        data: {
          blocking: true,
          issues: [
            {
              level: 'error',
              code: 'deployment_limit',
              field: 'max_duration_seconds',
              message: 'Duration conflicts with deployment limits',
            },
          ],
        },
      },
    })
    const user = userEvent.setup()
    renderSettings()
    const input = await screen.findByLabelText('Video duration limit (seconds)')
    await user.clear(input)
    await user.type(input, '1200')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(
      screen.getAllByText('Duration conflicts with deployment limits').length
    ).toBeGreaterThan(0)
    expect(api.put).not.toHaveBeenCalled()
  })

  it('requires review of higher resource limits before saving', async () => {
    const user = userEvent.setup()
    renderSettings()
    const input = await screen.findByLabelText('Concurrent jobs', {
      exact: true,
    })
    await user.clear(input)
    await user.type(input, '32')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    const dialog = await screen.findByRole('alertdialog', {
      name: 'Apply video processing changes?',
    })
    expect(within(dialog).getByText('32')).toBeVisible()
    expect(api.put).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(api.put).toHaveBeenCalledOnce())
  })

  it('preserves local edits after a version conflict and explicitly reloads the new version', async () => {
    vi.mocked(api.put).mockRejectedValue({
      response: {
        status: 409,
        data: { code: 'video_settings_version_conflict' },
      },
    })
    vi.mocked(api.get).mockResolvedValue({
      data: { success: true, data: { ...videoDocument, version: 9 } },
    })
    const user = userEvent.setup()
    renderSettings()
    const input = await screen.findByLabelText('Video duration limit (seconds)')
    await user.clear(input)
    await user.type(input, '1200')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(
      await screen.findByText('Configuration changed elsewhere')
    ).toBeVisible()
    expect(input).toHaveValue(1200)
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Reload' }))
    const dialog = await screen.findByRole('alertdialog', {
      name: 'Discard unsaved changes?',
    })
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(input).toHaveValue(1800))
    expect(screen.getByText('Configuration version 9')).toBeVisible()
  })

  it('prevents leaving with dirty settings and validates without writing', async () => {
    const user = userEvent.setup()
    renderSettings()
    const input = await screen.findByLabelText('Video duration limit (seconds)')
    await user.clear(input)
    await user.type(input, '1200')
    const event = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    await user.click(screen.getByRole('button', { name: 'Validate' }))
    await waitFor(() => expect(api.post).toHaveBeenCalledOnce())
    expect(api.put).not.toHaveBeenCalled()
  })

  it('rejects a frame budget contradiction before calling the server', async () => {
    renderSettings()
    const input = await screen.findByLabelText('Minimum frames per video')
    fireEvent.change(input, { target: { value: '40' } })
    fireEvent.submit(
      screen.getByRole('form', { name: 'Video processing settings' })
    )
    expect(
      await screen.findByText('Minimum frames must not exceed default frames')
    ).toBeVisible()
    expect(api.post).not.toHaveBeenCalled()
    expect(api.put).not.toHaveBeenCalled()
  })
})
