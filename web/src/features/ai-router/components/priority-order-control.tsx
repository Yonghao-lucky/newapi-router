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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'

import type { AIRouterChannelOrder } from '../types'

export function PriorityOrderControl(props: {
  value: AIRouterChannelOrder
  onChange: (value: AIRouterChannelOrder) => void
}) {
  const { t } = useTranslation()
  const id = useId()
  return (
    <Field className='gap-2'>
      <FieldLabel id={id}>{t('Priority display order')}</FieldLabel>
      <ToggleGroup
        aria-labelledby={id}
        variant='outline'
        size='sm'
        value={[props.value]}
        onValueChange={(values) => {
          const value = values[0]
          if (value === 'priority' || value === 'video_priority') {
            props.onChange(value)
          }
        }}
      >
        <ToggleGroupItem value='priority'>
          {t('Ordinary priority order')}
        </ToggleGroupItem>
        <ToggleGroupItem value='video_priority'>
          {t('Video priority order')}
        </ToggleGroupItem>
      </ToggleGroup>
      <FieldDescription>
        {t(
          'Display only: ordinary priority descends; video order puts configured video priorities first, then ordinary fallback. Eligible channels at the same priority use weighted selection.'
        )}
      </FieldDescription>
    </Field>
  )
}
