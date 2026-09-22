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
  it('saves URL downloads with optional allowlist, batch domains and versioned settings, then reloads them', async () => {
    const user = userEvent.setup()
    renderSettings()
    const enabled = await screen.findByRole('switch', {
      name: 'Enable remote video URLs',
    })
    const enforced = screen.getByRole('switch', {
      name: 'Enforce video domain allowlist',
    })
    expect(enabled).not.toBeChecked()
    expect(enforced).not.toBeChecked()
    await user.click(enabled)
    const domains = screen.getByRole('combobox', {
      name: 'Allowed video domains',
    })
    await user.click(domains)
    await user.paste('Media.Example.com\nbackup.example.com')
    await user.type(domains, 'remove.example.com')
    await user.keyboard('{Enter}')
    const remoteGroup = screen.getByRole('group', { name: 'Remote video URLs' })
    expect(within(remoteGroup).getByText('remove.example.com')).toBeVisible()
    const removeButtons = within(remoteGroup).getAllByRole('button')
    expect(removeButtons).toHaveLength(3)
    await user.click(removeButtons[2])
    expect(
      within(remoteGroup).queryByText('remove.example.com')
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole('switch', { name: 'Allow downloads with unknown size' })
    )
    fireEvent.change(
      screen.getByLabelText('Video download timeout (seconds)'),
      {
        target: { value: '90' },
      }
    )
    fireEvent.change(screen.getByLabelText('Concurrent video downloads'), {
      target: { value: '4' },
    })
    await user.click(screen.getByRole('button', { name: 'Save' }))
    const confirmation = await screen.findByRole('alertdialog', {
      name: 'Apply video processing changes?',
    })
    await user.click(
      within(confirmation).getByRole('button', { name: 'Continue' })
    )
    const expected = {
      ...videoDocument.settings,
      remote_url_enabled: true,
      remote_url_allowlist_enabled: false,
      remote_url_allowed_hosts: ['media.example.com', 'backup.example.com'],
      remote_url_allow_unknown_size: false,
      remote_url_download_timeout_seconds: 90,
      remote_url_max_downloads: 4,
    }
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/ai-router/video/settings', {
        expected_version: 7,
        settings: expected,
      })
    )
    await waitFor(() =>
      expect(screen.getByText('Configuration version 8')).toBeVisible()
    )
    vi.mocked(api.get).mockResolvedValue({
      data: {
        success: true,
        data: { ...videoDocument, version: 8, settings: expected },
      },
    })
    await user.click(screen.getByRole('button', { name: 'Reload' }))
    await user.click(
      within(
        await screen.findByRole('alertdialog', {
          name: 'Discard unsaved changes?',
        })
      ).getByRole('button', { name: 'Continue' })
    )
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Validate' })).toBeEnabled()
    )
    expect(enabled).toBeChecked()
    expect(enforced).not.toBeChecked()
    expect(
      screen.getByLabelText('Video download timeout (seconds)')
    ).toHaveValue(90)
    expect(screen.getByText('media.example.com')).toBeVisible()
  })

  it('rejects an empty enforced domain list and invalid domain entries before saving', async () => {
    const user = userEvent.setup()
    renderSettings()
    await user.click(
      await screen.findByRole('switch', {
        name: 'Enable remote video URLs',
      })
    )
    await user.click(
      await screen.findByRole('switch', {
        name: 'Enforce video domain allowlist',
      })
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    const domains = screen.getByRole('combobox', {
      name: 'Allowed video domains',
    })
    expect(
      await screen.findByText(
        'Add at least one domain before enabling the video allowlist'
      )
    ).toBeVisible()
    expect(domains).toHaveAttribute('aria-invalid', 'true')
    await user.click(domains)
    await user.paste('*.example.com,https://evil.example')
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(
      await screen.findByText(
        'Enter exact domain names without URLs, ports, IP addresses or wildcards'
      )
    ).toBeVisible()
    expect(api.post).not.toHaveBeenCalled()
    expect(api.put).not.toHaveBeenCalled()
  })

  it.each(['edited decimals', 'API decimals'] as const)(
    'validates, saves and reloads legal %s with native form constraints',
    async (source) => {
      const decimalSettings = {
        ...videoDocument.settings,
        short_scan_fps: 1.25,
        medium_scan_fps: 1.375,
        long_scan_fps: 0.125,
        scene_weight: 0.405,
        motion_weight: 0.195,
        sharpness_weight: 0.155,
        temporal_weight: 0.245,
        phash_duplicate_threshold: 0.085,
        black_frame_threshold: 0.905,
      }
      let persisted = structuredClone({
        ...videoDocument,
        settings:
          source === 'API decimals' ? decimalSettings : videoDocument.settings,
      })
      vi.mocked(api.put).mockImplementation(async (_url, input) => {
        persisted = {
          ...persisted,
          version: 8,
          settings: (input as { settings: VideoSettings }).settings,
        }
        return { data: { success: true, data: structuredClone(persisted) } }
      })
      vi.mocked(api.get).mockImplementation(async () => ({
        data: { success: true, data: structuredClone(persisted) },
      }))
      const user = userEvent.setup()
      renderSettings(persisted)
      const duration = await screen.findByLabelText(
        'Video duration limit (seconds)'
      )
      const fields = [
        ['Short video scan FPS', 1.25],
        ['Medium video scan FPS', 1.375],
        ['Long video scan FPS', 0.125],
        ['Scene change weight', 0.405],
        ['Motion weight', 0.195],
        ['Sharpness weight', 0.155],
        ['Time coverage weight', 0.245],
        ['Duplicate frame threshold', 0.085],
        ['Black frame threshold', 0.905],
      ] as const
      if (source === 'edited decimals') {
        for (const [label, value] of fields) {
          fireEvent.change(screen.getByLabelText(label), {
            target: { value: String(value) },
          })
        }
      }
      fireEvent.change(duration, { target: { value: '1200' } })
      const expected = { ...decimalSettings, max_duration_seconds: 1200 }
      await user.click(screen.getByRole('button', { name: 'Validate' }))
      await waitFor(() =>
        expect(api.post).toHaveBeenCalledWith(
          '/api/ai-router/video/settings/validate',
          { settings: expected }
        )
      )
      await waitFor(() =>
        expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
      )
      for (const [label] of fields) {
        const input = screen.getByLabelText<HTMLInputElement>(label)
        expect(input.validity.stepMismatch, label).toBe(false)
        expect(input.checkValidity(), label).toBe(true)
      }
      expect(
        screen
          .getByRole<HTMLFormElement>('form', {
            name: 'Video processing settings',
          })
          .checkValidity()
      ).toBe(true)
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() =>
        expect(api.put).toHaveBeenCalledWith('/api/ai-router/video/settings', {
          expected_version: 7,
          settings: expected,
        })
      )
      await waitFor(() =>
        expect(screen.getByText('Configuration version 8')).toBeVisible()
      )
      await user.click(screen.getByRole('button', { name: 'Reload' }))
      const dialog = await screen.findByRole('alertdialog', {
        name: 'Discard unsaved changes?',
      })
      await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
      await waitFor(() =>
        expect(api.get).toHaveBeenCalledWith('/api/ai-router/video/settings')
      )
      await waitFor(() =>
        expect(screen.getByRole('button', { name: 'Validate' })).toBeEnabled()
      )
      expect(duration).toHaveValue(1200)
      for (const [label, value] of fields) {
        expect(screen.getByLabelText(label)).toHaveValue(value)
      }
      expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    }
  )

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
