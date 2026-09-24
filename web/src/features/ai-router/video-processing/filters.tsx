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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { CompactDateTimeRangePicker } from '@/features/usage-logs/components/compact-date-time-range-picker'

import { statusLabels, videoCodecOptions } from './constants'
import { initialVideoFilters } from './lib/filters'
import type { VideoFilters } from './types'

export function VideoFilterBar(props: {
  onApply: (filters: VideoFilters) => void
  onRefresh: () => void
  filters: VideoFilters
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState(props.filters)
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault()
        props.onApply(draft)
      }}
      className='space-y-3'
      aria-label={t('Video log filters')}
    >
      <CompactDateTimeRangePicker
        start={new Date(draft.start_time * 1000)}
        end={new Date(draft.end_time * 1000)}
        onChange={(range) =>
          setDraft((previous) => ({
            ...previous,
            start_time: range.start
              ? Math.floor(range.start.getTime() / 1000)
              : previous.start_time,
            end_time: range.end
              ? Math.floor(range.end.getTime() / 1000)
              : previous.end_time,
          }))
        }
      />
      <FieldGroup className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
        {[
          { key: 'model', labelKey: 'Model' },
          { key: 'request_id', labelKey: 'Request ID' },
          { key: 'error_code', labelKey: 'Error code' },
        ].map((field) => (
          <Field key={field.key}>
            <FieldLabel htmlFor={`video-filter-${field.key}`}>
              {t(field.labelKey)}
            </FieldLabel>
            <Input
              id={`video-filter-${field.key}`}
              value={
                draft[field.key as 'model' | 'request_id' | 'error_code'] ?? ''
              }
              onChange={(event) =>
                setDraft((previous) => ({
                  ...previous,
                  [field.key]: event.target.value || undefined,
                }))
              }
            />
          </Field>
        ))}
        <Field>
          <FieldLabel htmlFor='video-filter-user'>{t('User ID')}</FieldLabel>
          <Input
            id='video-filter-user'
            type='number'
            min={1}
            value={draft.user_id ?? ''}
            onChange={(event) =>
              setDraft((previous) => ({
                ...previous,
                user_id: event.target.value
                  ? Number(event.target.value)
                  : undefined,
              }))
            }
          />
        </Field>
        <Field>
          <FieldLabel htmlFor='video-filter-status'>{t('Status')}</FieldLabel>
          <Select
            items={[
              { value: '', label: t('All statuses') },
              ...Object.entries(statusLabels).map(([value, label]) => ({
                value,
                label: t(label),
              })),
            ]}
            value={draft.status ?? ''}
            onValueChange={(value) =>
              setDraft((previous) => ({
                ...previous,
                status: value || undefined,
              }))
            }
          >
            <SelectTrigger id='video-filter-status' className='w-full'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value=''>{t('All statuses')}</SelectItem>
                {Object.entries(statusLabels).map(([value, label]) => (
                  <SelectItem key={value} value={value}>
                    {t(label)}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </Field>
        <Field>
          <FieldLabel htmlFor='video-filter-codec'>{t('Codec')}</FieldLabel>
          <Select
            items={[
              { value: '', label: t('All codecs') },
              ...[...videoCodecOptions, 'mixed'].map((value) => ({
                value,
                label: value === 'mixed' ? t('Mixed') : value,
              })),
            ]}
            value={draft.codec ?? ''}
            onValueChange={(value) =>
              setDraft((previous) => ({
                ...previous,
                codec: value || undefined,
              }))
            }
          >
            <SelectTrigger id='video-filter-codec' className='w-full'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value=''>{t('All codecs')}</SelectItem>
                {[...videoCodecOptions, 'mixed'].map((codec) => (
                  <SelectItem key={codec} value={codec}>
                    {codec === 'mixed' ? t('Mixed') : codec}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </Field>
        {[
          { key: 'min_duration_ms', labelKey: 'Minimum video duration (ms)' },
          { key: 'max_duration_ms', labelKey: 'Maximum video duration (ms)' },
          { key: 'min_frames', labelKey: 'Minimum frames' },
          { key: 'max_frames', labelKey: 'Maximum frames' },
        ].map((field) => (
          <Field key={field.key}>
            <FieldLabel htmlFor={`video-filter-${field.key}`}>
              {t(field.labelKey)}
            </FieldLabel>
            <Input
              id={`video-filter-${field.key}`}
              type='number'
              min={0}
              value={
                draft[
                  field.key as
                    | 'min_duration_ms'
                    | 'max_duration_ms'
                    | 'min_frames'
                    | 'max_frames'
                ] ?? ''
              }
              onChange={(event) =>
                setDraft((previous) => ({
                  ...previous,
                  [field.key]: event.target.value
                    ? Number(event.target.value)
                    : undefined,
                }))
              }
            />
          </Field>
        ))}
      </FieldGroup>
      <div className='flex gap-2'>
        <Button type='button' variant='outline' onClick={props.onRefresh}>
          {t('Refresh')}
        </Button>
        <Button type='submit'>{t('Apply filters')}</Button>
        <Button
          type='button'
          variant='outline'
          onClick={() => {
            const next = initialVideoFilters()
            setDraft(next)
            props.onApply(next)
          }}
        >
          {t('Reset')}
        </Button>
      </div>
    </form>
  )
}
