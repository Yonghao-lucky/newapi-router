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
import { describe, expect, it } from 'vitest'

import { videoSettingsSchema } from '../lib/settings'
import { videoDocument } from './fixture'

describe('video configuration contract', () => {
  it('preserves all server fields and explicit zero and false values', () => {
    const settings = {
      ...videoDocument.settings,
      enabled: false,
      max_waiting_jobs: 0,
      max_waiting_per_user: 0,
      include_timestamps: false,
      reserve_image_slots: 0,
    }
    expect(videoSettingsSchema.parse(settings)).toEqual(settings)
  })
  it.each([
    { max_running_per_user: 17 },
    { max_waiting_jobs: 0, max_waiting_per_user: 1 },
    { reserve_image_slots: 60 },
    { scene_weight: 0.8 },
    { max_decoded_video_mb: Number.POSITIVE_INFINITY },
    { target_models: [] },
  ])('rejects invalid bounds or cross-field constraints: %j', (patch) => {
    expect(
      videoSettingsSchema.safeParse({ ...videoDocument.settings, ...patch })
        .success
    ).toBe(false)
  })
})
