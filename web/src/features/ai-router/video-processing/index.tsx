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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { lazy, Suspense, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { LoadingState } from '@/components/loading-state'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getServerErrorStatus } from '@/lib/server-error-message'

import { getVideoSettings, videoKeys } from './api'
import { VideoFilterBar } from './filters'
import { initialVideoFilters } from './lib/filters'
import { VideoLogsTable } from './logs-table'
import { VideoQueryError } from './query-error'
import { VideoRuntimeStatus } from './runtime-status'
import { VideoSettingsForm } from './settings'

const VideoPerformanceCharts = lazy(() =>
  import('./performance-charts').then((module) => ({
    default: module.VideoPerformanceCharts,
  }))
)

export function VideoProcessingTab(props: {
  onDirtyChange: (dirty: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState('settings')
  const [filters, setFilters] = useState(initialVideoFilters)
  const settings = useQuery({
    queryKey: videoKeys.settings,
    queryFn: getVideoSettings,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  if (
    settings.isError &&
    [401, 403].includes(getServerErrorStatus(settings.error) ?? 0)
  ) {
    return (
      <VideoQueryError
        error={settings.error}
        onRetry={() => void settings.refetch()}
      />
    )
  }
  return (
    <div className='space-y-6'>
      <VideoRuntimeStatus />
      <Tabs value={tab} onValueChange={setTab} className='space-y-4'>
        <TabsList aria-label={t('Video Processing')}>
          <TabsTrigger value='settings'>{t('Configuration')}</TabsTrigger>
          <TabsTrigger value='monitoring'>{t('Monitoring')}</TabsTrigger>
          <TabsTrigger value='logs'>{t('Logs')}</TabsTrigger>
        </TabsList>
        <TabsContent value='settings' keepMounted>
          {settings.isPending && <LoadingState />}
          {settings.isError && (
            <VideoQueryError
              error={settings.error}
              onRetry={() => void settings.refetch()}
            />
          )}
          {settings.data && (
            <VideoSettingsForm
              document={settings.data}
              onDirtyChange={props.onDirtyChange}
            />
          )}
        </TabsContent>
        {(tab === 'logs' || tab === 'monitoring') && (
          <VideoFilterBar
            filters={filters}
            onApply={setFilters}
            onRefresh={() => {
              void Promise.all([
                queryClient.invalidateQueries({ queryKey: videoKeys.logs }),
                queryClient.invalidateQueries({ queryKey: videoKeys.stats }),
              ])
            }}
          />
        )}
        <TabsContent value='monitoring'>
          <Suspense fallback={<LoadingState />}>
            <VideoPerformanceCharts filters={filters} />
          </Suspense>
        </TabsContent>
        <TabsContent value='logs'>
          <VideoLogsTable key={JSON.stringify(filters)} filters={filters} />
        </TabsContent>
      </Tabs>
    </div>
  )
}
