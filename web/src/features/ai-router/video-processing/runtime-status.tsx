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
import {
  Activity,
  Clock,
  Database,
  Film,
  HardDrive,
  Layers,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { StatCard } from '@/features/dashboard/components/ui/stat-card'
import { formatNumber } from '@/lib/format'

import { getVideoRuntime, videoKeys } from './api'
import { VideoQueryError } from './query-error'

export function VideoRuntimeStatus() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: videoKeys.runtime,
    queryFn: getVideoRuntime,
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
    meta: { errorToast: false },
  })
  if (query.isPending) {
    return <LoadingState message={t('Loading runtime status...')} />
  }
  if (query.isError || !query.data.available) {
    return (
      <VideoQueryError
        error={query.error}
        onRetry={() => void query.refetch()}
      />
    )
  }
  const runtime = query.data
  const queue = runtime.queue
  const mb = (bytes: number) => `${formatNumber(bytes / 1048576)} MB`
  return (
    <section className='space-y-3' aria-label={t('Video runtime')}>
      {!runtime.enabled && (
        <Alert>
          <AlertTitle>{t('Video processing is disabled')}</AlertTitle>
          <AlertDescription>
            {t(
              'New requests follow the original relay path. Running jobs are allowed to finish.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {runtime.dropped_logs > 0 && (
        <Alert variant='destructive'>
          <AlertTitle>{t('Processing logs were lost')}</AlertTitle>
          <AlertDescription>
            {t(
              'Unstored completion records: {{count}}. Check database availability and logging capacity.',
              { count: runtime.dropped_logs }
            )}
          </AlertDescription>
        </Alert>
      )}
      <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
        <StatCard
          title={t('Running jobs')}
          value={`${queue.running_jobs} / ${queue.max_running_jobs}`}
          description={t('Active video preprocessing requests')}
          icon={Activity}
        />
        <StatCard
          title={t('Waiting jobs')}
          value={`${queue.waiting_jobs} / ${queue.max_waiting_jobs}`}
          description={t('Oldest wait: {{count}} ms', {
            count: queue.oldest_wait_ms,
          })}
          icon={Clock}
        />
        <StatCard
          title={t('Waiting body bytes')}
          value={mb(queue.waiting_bytes)}
          description={t('Limit: {{value}}', {
            value: mb(queue.max_waiting_bytes),
          })}
          icon={Layers}
        />
        <StatCard
          title={t('Active FFmpeg processes')}
          value={queue.active_ffmpeg}
          description={t('Analysis and frame export')}
          icon={Film}
        />
        <StatCard
          title={t('Temporary storage')}
          value={mb(queue.temp_storage_bytes)}
          description={t('Limit: {{limit}} · Free disk: {{free}}', {
            limit: mb(queue.temp_storage_limit_bytes),
            free: mb(runtime.disk.free_bytes),
          })}
          icon={HardDrive}
        />
        <StatCard
          title={t('Completed requests')}
          value={runtime.completed_last_1m}
          description={t(
            'Last 1 / 5 / 15 minutes: {{one}} / {{five}} / {{fifteen}}',
            {
              one: runtime.completed_last_1m,
              five: runtime.completed_last_5m,
              fifteen: runtime.completed_last_15m,
            }
          )}
          icon={Database}
        />
      </div>
      <div className='flex flex-wrap items-center gap-2 text-sm'>
        <Badge variant={runtime.ffmpeg_available ? 'secondary' : 'destructive'}>
          FFmpeg: {runtime.ffmpeg_available ? t('Available') : t('Unavailable')}
        </Badge>
        <Badge
          variant={runtime.ffprobe_available ? 'secondary' : 'destructive'}
        >
          ffprobe:{' '}
          {runtime.ffprobe_available ? t('Available') : t('Unavailable')}
        </Badge>
        <span>
          {t('Configuration version {{version}}', {
            version: runtime.config_version,
          })}
        </span>
        <span className='text-muted-foreground'>
          {t('Applied at {{time}}', {
            time: new Date(runtime.config_applied_at * 1000).toLocaleString(),
          })}
        </span>
        <span className='text-muted-foreground'>
          {t('Pending logs: {{count}}', { count: runtime.pending_logs })}
        </span>
      </div>
    </section>
  )
}
