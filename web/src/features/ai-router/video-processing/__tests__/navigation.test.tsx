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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { AIRouterPage } from '../../index'
import { videoDocument, videoRuntime } from './fixture'

it('keeps edits across video views and asks before leaving the video tab', async () => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url.endsWith('/video/settings')) {
      return { data: { success: true, data: videoDocument } }
    }
    if (url.endsWith('/video/runtime')) {
      return { data: { success: true, data: videoRuntime } }
    }
    return {
      data: {
        success: true,
        data: { items: [], total: 0, page: 1, page_size: 20 },
      },
    }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const root = createRootRoute()
  const route = createRoute({
    getParentRoute: () => root,
    path: '/',
    component: AIRouterPage,
  })
  const router = createRouter({
    routeTree: root.addChildren([route]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await user.click(await screen.findByRole('tab', { name: 'Video Processing' }))
  const input = await screen.findByLabelText('Video duration limit (seconds)')
  await user.clear(input)
  await user.type(input, '1200')
  await user.click(screen.getByRole('tab', { name: 'Logs' }))
  expect(await screen.findByText('No video processing logs')).toBeVisible()
  await user.click(screen.getByRole('tab', { name: 'Configuration' }))
  expect(input).toBeVisible()
  expect(input).toHaveValue(1200)
  await user.click(screen.getByRole('tab', { name: 'Target Models' }))
  let confirm = await screen.findByRole('alertdialog', {
    name: 'Discard unsaved changes?',
  })
  await user.click(within(confirm).getByRole('button', { name: 'Cancel' }))
  expect(screen.getByRole('tab', { name: 'Video Processing' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(input).toHaveValue(1200)
  await user.click(screen.getByRole('tab', { name: 'Target Models' }))
  confirm = await screen.findByRole('alertdialog', {
    name: 'Discard unsaved changes?',
  })
  await user.click(within(confirm).getByRole('button', { name: 'Continue' }))
  await waitFor(() =>
    expect(screen.getByRole('tab', { name: 'Target Models' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
  )
})
