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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { LoadingState } from '@/components/loading-state'
import { formatNumber } from '@/lib/format'

import { getVideoLog, videoKeys } from './api'
import { statusLabels } from './constants'
import { VideoQueryError } from './query-error'

const stages = [
  { key: 'queue_ms', labelKey: 'Queue' },
  { key: 'decode_ms', labelKey: 'Base64 decoding' },
  { key: 'probe_ms', labelKey: 'Media probing' },
  { key: 'analyze_ms', labelKey: 'Frame analysis' },
  { key: 'select_ms', labelKey: 'Frame selection' },
  { key: 'export_ms', labelKey: 'JPEG export' },
  { key: 'rewrite_ms', labelKey: 'Request rewriting' },
] as const

export function VideoLogDetail(props: {
  id: number | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: [...videoKeys.logs, 'detail', props.id],
    queryFn: async () => (props.id === null ? null : getVideoLog(props.id)),
    enabled: props.id !== null,
    meta: { errorToast: false },
  })
  const log = query.data
  return (
    <Dialog
      open={props.id !== null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Video processing details')}
      description={t(
        'Processing metadata only. Media and prompts are never stored in these logs.'
      )}
      contentClassName='sm:max-w-3xl'
    >
      {query.isPending && <LoadingState />}
      {query.isError && (
        <VideoQueryError
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      )}
      {log && (
        <div className='space-y-4'>
          <div className='flex items-center gap-2'>
            <span className='min-w-0 break-all'>{log.request_id}</span>
            <CopyButton
              value={log.request_id}
              aria-label={t('Copy request ID')}
            />
          </div>
          <dl className='grid gap-3 text-sm sm:grid-cols-2'>
            <div>
              <dt>{t('Time')}</dt>
              <dd>{new Date(log.created_at * 1000).toLocaleString()}</dd>
            </div>
            <div>
              <dt>{t('Videos')}</dt>
              <dd>{log.video_count}</dd>
            </div>
            <div>
              <dt>{t('Resolution')}</dt>
              <dd>
                {log.width} × {log.height}
              </dd>
            </div>
            <div>
              <dt>{t('Model')}</dt>
              <dd>{log.model}</dd>
            </div>
            <div>
              <dt>{t('User ID')}</dt>
              <dd>{log.user_id}</dd>
            </div>
            <div>
              <dt>{t('Status')}</dt>
              <dd>{t(statusLabels[log.status])}</dd>
            </div>
            <div>
              <dt>{t('Error code')}</dt>
              <dd className='break-all'>{log.error_code || '—'}</dd>
            </div>
            <div>
              <dt>{t('Sampler version')}</dt>
              <dd>{log.sampler_version}</dd>
            </div>
            <div>
              <dt>{t('Configuration version')}</dt>
              <dd>{log.config_version}</dd>
            </div>
            <div>
              <dt>{t('Video duration (ms)')}</dt>
              <dd>{formatNumber(log.video_duration_ms)}</dd>
            </div>
            <div>
              <dt>{t('Codec')}</dt>
              <dd>{log.codec || '—'}</dd>
            </div>
            <div>
              <dt>{t('Existing images')}</dt>
              <dd>{log.existing_images}</dd>
            </div>
            <div>
              <dt>{t('Extracted frames')}</dt>
              <dd>{log.extracted_frames}</dd>
            </div>
            <div>
              <dt>{t('Original body bytes')}</dt>
              <dd>{formatNumber(log.original_body_bytes)}</dd>
            </div>
            <div>
              <dt>{t('Decoded video bytes')}</dt>
              <dd>{formatNumber(log.decoded_video_bytes)}</dd>
            </div>
            <div>
              <dt>{t('Output image bytes')}</dt>
              <dd>{formatNumber(log.output_image_bytes)}</dd>
            </div>
            <div>
              <dt>{t('Rewritten body bytes')}</dt>
              <dd>{formatNumber(log.output_body_bytes)}</dd>
            </div>
            <div>
              <dt>{t('Total time (ms)')}</dt>
              <dd>{formatNumber(log.total_ms)}</dd>
            </div>
          </dl>
          <StaticDataTable
            data={stages.map((stage) => ({
              stage: t(stage.labelKey),
              ms: log[stage.key],
            }))}
            columns={[
              {
                id: 'stage',
                header: t('Processing stage'),
                cell: (row) => row.stage,
              },
              {
                id: 'time',
                header: t('Duration (ms)'),
                cell: (row) => formatNumber(row.ms),
              },
            ]}
          />
          {log.videos && log.videos.length > 0 && (
            <StaticDataTable
              data={log.videos}
              columns={[
                { id: 'index', header: t('Video'), cell: (row) => row.index },
                {
                  id: 'codec',
                  header: t('Codec'),
                  cell: (row) => row.codec || '—',
                },
                {
                  id: 'duration',
                  header: t('Duration (ms)'),
                  cell: (row) => formatNumber(row.duration_ms),
                },
                {
                  id: 'dimensions',
                  header: t('Resolution'),
                  cell: (row) => `${row.width} × ${row.height}`,
                },
                {
                  id: 'frames',
                  header: t('Frames'),
                  cell: (row) => row.frames,
                },
              ]}
            />
          )}
        </div>
      )}
    </Dialog>
  )
}
