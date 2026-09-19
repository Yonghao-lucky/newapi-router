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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { VideoRuntimeStatus } from '../runtime-status'
import { videoRuntime as runtime } from './fixture'

function renderRuntime() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <VideoRuntimeStatus />
    </QueryClientProvider>
  )
}

describe('video runtime states', () => {
  it('shows a loading state while the runtime request is pending', () => {
    vi.spyOn(api, 'get').mockImplementation(() => new Promise(() => undefined))
    renderRuntime()
    expect(screen.getByText('Loading runtime status...')).toBeVisible()
  })
  it('shows configured limits, binary availability and the disabled bypass state', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: runtime },
    })
    renderRuntime()
    expect(
      await screen.findByText('Video processing is disabled')
    ).toBeVisible()
    expect(screen.getByText('3 / 16')).toBeVisible()
    expect(screen.getByText('2 / 500')).toBeVisible()
    expect(screen.getByText('ffprobe: Unavailable')).toBeVisible()
    expect(screen.getByText('Configuration version 7')).toBeVisible()
  })
  it('explains missing administrator permissions after a forbidden response', async () => {
    vi.spyOn(api, 'get').mockRejectedValue({ response: { status: 403 } })
    renderRuntime()
    expect(
      await screen.findByText('Administrator access required')
    ).toBeVisible()
    expect(screen.queryByText('Running jobs')).not.toBeInTheDocument()
  })
  it('recovers from an unavailable backend through the shared retry action', async () => {
    vi.spyOn(api, 'get')
      .mockRejectedValueOnce({ response: { status: 503 } })
      .mockResolvedValue({ data: { success: true, data: runtime } })
    const user = userEvent.setup()
    renderRuntime()
    expect(
      await screen.findByText('Video processing is unavailable')
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('3 / 16')).toBeVisible()
  })
})
