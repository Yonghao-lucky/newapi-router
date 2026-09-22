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
import { expect, it } from 'vitest'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformFormDataToCreatePayload,
} from '../channel-form'

it('preserves scheduling and retry settings when editing channel capabilities', () => {
  const router = {
    enabled: true,
    video_priority: 0,
    max_retry: 2,
    retry_status_codes: [],
    switch_status_codes: [502],
    supports_image: true,
  }
  const { channel } = transformFormDataToCreatePayload({
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'test',
    models: 'kimi-k3',
    key: 'mock-only',
    settings: JSON.stringify({ router }),
    router_enabled: 'true',
    router_supports_image: 'false',
  })
  expect(JSON.parse(channel.settings ?? '').router).toEqual({
    ...router,
    supports_image: false,
  })
})

it('keeps an unconfigured router absent', () => {
  const { channel } = transformFormDataToCreatePayload({
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'test',
    models: 'kimi-k3',
    key: 'mock-only',
  })
  expect(JSON.parse(channel.settings ?? '').router).toBeUndefined()
})
