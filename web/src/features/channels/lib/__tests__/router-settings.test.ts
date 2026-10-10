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

import { channelSchema } from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  buildSettingJSON,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
} from '../channel-form'

it('preserves independent K3 flags when saving through the ordinary channel editor', () => {
  const setting = {
    k3_temperature_compatibility: true,
    k3_top_p_compatibility: false,
    k3_presence_penalty_compatibility: true,
    k3_frequency_penalty_compatibility: false,
    k3_allow_thinking_disabled: true,
  }
  const channel = channelSchema.parse({
    id: 48,
    name: 'fixture',
    type: 1,
    key: 'mock-only',
    created_time: 0,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    status: 1,
    group: 'default',
    models: 'kimi-k3',
    setting: JSON.stringify(setting),
  })
  const form = transformChannelToFormDefaults(channel)
  expect(JSON.parse(buildSettingJSON(form))).toEqual(
    expect.objectContaining(setting)
  )
})

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
