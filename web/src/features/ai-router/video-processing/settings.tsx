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
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { FormNavigationGuard } from '@/features/system-settings/components/form-navigation-guard'

import { ValidationAlert } from '../components/validation-alert'
import { settingLabels } from './constants'
import { VideoSettingsFields } from './settings-fields'
import type { VideoSettings, VideoSettingsDocument } from './types'
import { useVideoSettings } from './use-video-settings'

export function VideoSettingsForm(props: {
  document: VideoSettingsDocument
  onDirtyChange: (dirty: boolean) => void
}) {
  const { t } = useTranslation()
  const editor = useVideoSettings(props.document)
  const dirty = editor.form.formState.isDirty
  const onDirtyChange = props.onDirtyChange
  useEffect(() => {
    onDirtyChange(dirty)
    return () => onDirtyChange(false)
  }, [dirty, onDirtyChange])
  const changes = Object.entries(editor.pending ?? {})
    .filter(
      ([key, value]) =>
        JSON.stringify(value) !==
        JSON.stringify(editor.baseline.settings[key as keyof VideoSettings])
    )
    .map(([key, value]) => ({
      key,
      before: editor.baseline.settings[key as keyof VideoSettings],
      after: value,
    }))
  const label = (key: string) => t(settingLabels[key] ?? key)
  const display = (value: unknown): string => {
    if (typeof value === 'boolean') return value ? t('Enabled') : t('Disabled')
    if (Array.isArray(value)) return value.join(', ')
    return String(value)
  }
  return (
    <>
      <FormNavigationGuard when={dirty} />
      <form
        onSubmit={editor.form.handleSubmit((values) => editor.submit(values))}
        className='space-y-4'
        aria-label={t('Video processing settings')}
      >
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <p className='text-muted-foreground text-sm'>
            {t('Configuration version {{version}}', {
              version: editor.baseline.version,
            })}
            {dirty && (
              <span className='ml-2 font-medium'>{t('Unsaved changes')}</span>
            )}
          </p>
          <div className='flex flex-wrap gap-2'>
            <Button
              type='button'
              variant='outline'
              disabled={editor.busy}
              onClick={() => editor.setResetAction('defaults')}
            >
              {t('Restore defaults')}
            </Button>
            <Button
              type='button'
              variant='outline'
              disabled={editor.busy}
              onClick={() => editor.setResetAction('reload')}
            >
              {t('Reload')}
            </Button>
            <Button
              type='button'
              variant='outline'
              disabled={editor.busy}
              onClick={() =>
                void editor.form.handleSubmit((values) =>
                  editor.submit(values, true)
                )()
              }
            >
              {t('Validate')}
            </Button>
            <Button
              type='submit'
              disabled={editor.busy || editor.conflict || !dirty}
            >
              {editor.busy ? t('Saving...') : t('Save')}
            </Button>
          </div>
        </div>
        {editor.conflict && (
          <Alert variant='destructive'>
            <AlertTitle>{t('Configuration changed elsewhere')}</AlertTitle>
            <AlertDescription>
              {t(
                'Your edits are preserved. Reload the latest settings before saving again.'
              )}
            </AlertDescription>
          </Alert>
        )}
        <ValidationAlert
          issues={editor.issues.map((issue) => ({
            ...issue,
            message: `${issue.field ? `${label(issue.field)}: ` : ''}${t(issue.message)}`,
          }))}
        />
        <VideoSettingsFields
          form={editor.form}
          disabled={editor.busy || editor.pending !== null}
        />
        <Card>
          <CardHeader>
            <CardTitle>{t('Deployment information')}</CardTitle>
          </CardHeader>
          <CardContent>
            <dl className='grid gap-3 text-sm sm:grid-cols-2'>
              <div>
                <dt className='text-muted-foreground'>FFmpeg</dt>
                <dd className='break-all'>
                  {editor.baseline.deployment.ffmpeg_path}
                </dd>
              </div>
              <div>
                <dt className='text-muted-foreground'>ffprobe</dt>
                <dd className='break-all'>
                  {editor.baseline.deployment.ffprobe_path}
                </dd>
              </div>
              <div>
                <dt className='text-muted-foreground'>
                  {t('Temporary directory')}
                </dt>
                <dd className='break-all'>
                  {editor.baseline.deployment.temp_dir}
                </dd>
              </div>
              <div>
                <dt className='text-muted-foreground'>
                  {t('Hardware decoding')}
                </dt>
                <dd>
                  {editor.baseline.deployment.hwaccel_enabled
                    ? t('Enabled')
                    : t('Disabled')}
                </dd>
              </div>
            </dl>
          </CardContent>
        </Card>
      </form>
      <ConfirmDialog
        open={editor.pending !== null}
        onOpenChange={(open) => {
          if (!open && !editor.busy) editor.setPending(null)
        }}
        title={t('Apply video processing changes?')}
        desc={t(
          'Review resource increases and retention changes before applying this configuration.'
        )}
        isLoading={editor.save.isPending}
        handleConfirm={() => {
          if (editor.pending) {
            editor.save.mutate({
              settings: editor.pending,
              expected_version: editor.baseline.version,
            })
          }
        }}
      >
        <StaticDataTable
          className='max-h-64 overflow-y-auto'
          data={changes}
          columns={[
            {
              id: 'setting',
              header: t('Setting'),
              cell: (row) => label(row.key),
            },
            {
              id: 'before',
              header: t('Current'),
              cell: (row) => display(row.before),
            },
            {
              id: 'after',
              header: t('New'),
              cell: (row) => display(row.after),
            },
          ]}
        />
      </ConfirmDialog>
      <ConfirmDialog
        open={editor.resetAction !== null}
        onOpenChange={(open) => {
          if (!open) editor.setResetAction(null)
        }}
        title={t('Discard unsaved changes?')}
        desc={
          editor.resetAction === 'defaults'
            ? t('Load the server defaults into this form. Save to apply them.')
            : t(
                'Reload the latest saved configuration and discard local edits.'
              )
        }
        handleConfirm={editor.reset}
      />
    </>
  )
}
