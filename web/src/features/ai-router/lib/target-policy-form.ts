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
import { z } from 'zod'

import { MAX_TARGET_MAX_ATTEMPTS, MIN_TARGET_MAX_ATTEMPTS } from '../constants'
import type { AIRouterTargetPolicy } from '../types'

export const targetPolicyFormSchema = z.object({
  model: z.string().min(1, 'Model is required'),
  group_id: z.string().min(1, 'Group is required'),
  enabled: z.boolean(),
  k3_strict_validation: z.boolean(),
  max_attempts: z
    .number()
    .int()
    .min(MIN_TARGET_MAX_ATTEMPTS, 'Max attempts is out of range')
    .max(MAX_TARGET_MAX_ATTEMPTS, 'Max attempts is out of range'),
  // Ordered candidate list; enabled flags live with each entry.
  candidates: z.array(
    z.object({
      channel_id: z.number().int(),
      enabled: z.boolean(),
    })
  ),
})

export type TargetPolicyFormValues = z.infer<typeof targetPolicyFormSchema>

export function transformPolicyToFormDefaults(
  policy?: AIRouterTargetPolicy | null
): TargetPolicyFormValues {
  return {
    model: policy?.model ?? '',
    group_id: policy?.group_id ?? 'default',
    enabled: policy?.enabled ?? true,
    k3_strict_validation: policy?.k3_strict_validation ?? false,
    max_attempts: policy?.max_attempts ?? 3,
    candidates:
      policy?.candidates.map((candidate) => ({
        channel_id: candidate.channel_id,
        enabled: candidate.enabled,
      })) ?? [],
  }
}

export function transformFormToPolicyPayload(values: TargetPolicyFormValues) {
  return {
    model: values.model.trim(),
    group_id: values.group_id.trim(),
    enabled: values.enabled,
    k3_strict_validation: values.k3_strict_validation,
    max_attempts: values.max_attempts,
    candidates: values.candidates,
  }
}
