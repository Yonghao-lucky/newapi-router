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
import { Controller, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import { Card } from '@/components/ui/card'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import { booleanFields, numericFields, settingsGroups } from './constants'
import type { VideoSettings } from './types'

const listFields = [
  {
    key: 'target_models',
    labelKey: 'Target Models',
    options: [],
    allowCreate: true,
  },
  {
    key: 'allowed_mime_types',
    labelKey: 'Allowed video formats',
    options: ['video/mp4', 'video/webm', 'video/quicktime'],
    allowCreate: false,
  },
  {
    key: 'allowed_codecs',
    labelKey: 'Allowed video codecs',
    options: ['h264', 'hevc', 'vp9', 'av1'],
    allowCreate: false,
  },
] as const

export function VideoSettingsFields(props: {
  form: UseFormReturn<VideoSettings>
  disabled: boolean
}) {
  const { t } = useTranslation()
  return (
    <>
      <Card className='p-4'>
        <FieldSet disabled={props.disabled}>
          <FieldLegend>{t('Video processing scope')}</FieldLegend>
          <FieldDescription>
            {t(
              'Convert Base64 videos to images before model routing. Remote video URLs are not supported.'
            )}
          </FieldDescription>
          <FieldGroup>
            <Controller
              control={props.form.control}
              name='mode'
              render={({ field }) => (
                <Field>
                  <FieldLabel htmlFor='video-mode'>
                    {t('Video handling')}
                  </FieldLabel>
                  <Select
                    items={[
                      {
                        value: 'always_frames',
                        label: t('Always convert to frames'),
                      },
                    ]}
                    value={field.value}
                    onValueChange={field.onChange}
                    disabled={props.disabled}
                  >
                    <SelectTrigger id='video-mode'>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        <SelectItem value='always_frames'>
                          {t('Always convert to frames')}
                        </SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
            {booleanFields
              .filter(
                (definition) => definition.group === 'Video processing scope'
              )
              .map((definition) => (
                <Controller
                  key={definition.key}
                  control={props.form.control}
                  name={definition.key}
                  render={({ field }) => (
                    <Field orientation='horizontal'>
                      <FieldLabel htmlFor={`video-${field.name}`}>
                        {t(definition.labelKey)}
                      </FieldLabel>
                      <Switch
                        id={`video-${field.name}`}
                        checked={field.value}
                        onCheckedChange={field.onChange}
                        disabled={props.disabled}
                      />
                    </Field>
                  )}
                />
              ))}
            {listFields.map((definition) => (
              <Controller
                key={definition.key}
                control={props.form.control}
                name={definition.key}
                render={({ field, fieldState }) => (
                  <Field data-invalid={fieldState.invalid}>
                    <FieldLabel htmlFor={`video-${field.name}`}>
                      {t(definition.labelKey)}
                    </FieldLabel>
                    <MultiSelect
                      aria-label={t(definition.labelKey)}
                      id={`video-${field.name}`}
                      selected={field.value}
                      onChange={field.onChange}
                      options={[
                        ...new Set([...definition.options, ...field.value]),
                      ].map((value) => ({ value, label: value }))}
                      allowCreate={definition.allowCreate}
                      disabled={props.disabled}
                      aria-invalid={fieldState.invalid}
                      aria-describedby={
                        fieldState.error
                          ? `video-${field.name}-error`
                          : undefined
                      }
                    />
                    {fieldState.error && (
                      <FieldError id={`video-${field.name}-error`}>
                        {t(fieldState.error.message ?? 'Invalid value')}
                      </FieldError>
                    )}
                  </Field>
                )}
              />
            ))}
          </FieldGroup>
        </FieldSet>
      </Card>
      {settingsGroups.map((group) => (
        <Card key={group} className='p-4'>
          <FieldSet disabled={props.disabled}>
            <FieldLegend>{t(group)}</FieldLegend>
            {group === 'Concurrency and queue' && (
              <FieldDescription>
                {t(
                  'New limits apply to new jobs. Reducing capacity does not stop running jobs.'
                )}
              </FieldDescription>
            )}
            {group === 'Frame selection' && (
              <FieldDescription>
                {t(
                  'Existing images and reserved slots count toward the request image limit.'
                )}
              </FieldDescription>
            )}
            {group === 'Advanced sampling' && (
              <FieldDescription>
                {t(
                  'Scene, motion, sharpness and time coverage weights must sum to one.'
                )}
              </FieldDescription>
            )}
            <FieldGroup className='grid gap-4 sm:grid-cols-2 xl:grid-cols-3'>
              {numericFields
                .filter((field) => field.group === group)
                .map((definition) => (
                  <Controller
                    key={definition.key}
                    control={props.form.control}
                    name={definition.key}
                    render={({ field, fieldState }) => (
                      <Field data-invalid={fieldState.invalid}>
                        <FieldLabel htmlFor={`video-${field.name}`}>
                          {t(definition.labelKey)}
                        </FieldLabel>
                        <Input
                          {...field}
                          id={`video-${field.name}`}
                          type='number'
                          min={definition.min}
                          max={definition.max}
                          step={definition.step}
                          value={Number.isNaN(field.value) ? '' : field.value}
                          onChange={(event) =>
                            field.onChange(
                              event.target.value === ''
                                ? Number.NaN
                                : Number(event.target.value)
                            )
                          }
                          aria-invalid={fieldState.invalid}
                          aria-describedby={
                            fieldState.error
                              ? `video-${field.name}-error`
                              : undefined
                          }
                        />
                        <FieldDescription>
                          {t('Range: {{min}}–{{max}}', {
                            min: definition.min,
                            max: definition.max,
                          })}
                        </FieldDescription>
                        {fieldState.error && (
                          <FieldError id={`video-${field.name}-error`}>
                            {t(fieldState.error.message ?? 'Invalid value')}
                          </FieldError>
                        )}
                      </Field>
                    )}
                  />
                ))}
              {booleanFields
                .filter((definition) => definition.group === group)
                .map((definition) => (
                  <Controller
                    key={definition.key}
                    control={props.form.control}
                    name={definition.key}
                    render={({ field }) => (
                      <Field orientation='horizontal'>
                        <FieldLabel htmlFor={`video-${field.name}`}>
                          {t(definition.labelKey)}
                        </FieldLabel>
                        <Switch
                          id={`video-${field.name}`}
                          checked={field.value}
                          onCheckedChange={field.onChange}
                          disabled={props.disabled}
                        />
                      </Field>
                    )}
                  />
                ))}
            </FieldGroup>
          </FieldSet>
        </Card>
      ))}
    </>
  )
}
