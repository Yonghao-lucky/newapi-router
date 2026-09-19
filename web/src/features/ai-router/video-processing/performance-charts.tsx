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
import { VChart } from '@visactor/react-vchart'
import { useId, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { LoadingState } from '@/components/loading-state'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatNumber } from '@/lib/format'
import { useChartTheme } from '@/lib/use-chart-theme'
import { VCHART_OPTION } from '@/lib/vchart'

import { getVideoStats, videoKeys } from './api'
import { VideoQueryError } from './query-error'
import type { VideoFilters, VideoTrend } from './types'

interface ChartDatum {
  label: string
  value: number
}

function VideoDistributionChart(props: {
  title: string
  theme: ReturnType<typeof useChartTheme>
  values: ChartDatum[]
}) {
  const { t } = useTranslation()
  const theme = props.theme
  const descriptionID = useId()
  const spec = useMemo(
    () => ({
      type: 'bar' as const,
      direction: 'horizontal' as const,
      data: [{ id: 'values', values: props.values }],
      xField: 'value',
      yField: 'label',
      axes: [
        { orient: 'bottom' as const, type: 'linear' as const },
        { orient: 'left' as const, type: 'band' as const },
      ],
      background: 'transparent',
    }),
    [props.values]
  )
  return (
    <Card className='min-w-0'>
      <CardHeader>
        <CardTitle>{props.title}</CardTitle>
      </CardHeader>
      <CardContent>
        {props.values.length === 0 ? (
          <EmptyState title={t('No Data')} className='min-h-48' />
        ) : (
          <>
            <div
              className='h-64'
              role='img'
              aria-label={props.title}
              aria-describedby={descriptionID}
            >
              {theme.themeReady && (
                <VChart
                  spec={spec}
                  option={VCHART_OPTION}
                  theme={theme.resolvedTheme === 'dark' ? 'dark' : 'light'}
                />
              )}
            </div>
            <ul id={descriptionID} className='sr-only'>
              {props.values.map((point) => (
                <li key={point.label}>
                  {point.label}: {formatNumber(point.value)}
                </li>
              ))}
            </ul>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function VideoTrendChart(props: {
  values: VideoTrend[]
  latency?: boolean
  theme: ReturnType<typeof useChartTheme>
}) {
  const { t } = useTranslation()
  const theme = props.theme
  const descriptionID = useId()
  const data = useMemo(
    () =>
      props.values.flatMap((point) => {
        const time = new Date(point.time * 1000).toLocaleString()
        if (props.latency) {
          return [
            {
              time,
              kind: t('Queue time (ms)'),
              value: point.total ? point.queue_ms / point.total : 0,
            },
            {
              time,
              kind: t('Total time (ms)'),
              value: point.total ? point.total_ms / point.total : 0,
            },
          ]
        }
        return [
          { time, kind: t('Success'), value: point.success },
          { time, kind: t('Failed'), value: point.failure },
          { time, kind: t('Rejected'), value: point.rejected },
        ]
      }),
    [props.values, props.latency, t]
  )
  const spec = useMemo(
    () => ({
      type: 'line' as const,
      data: [{ id: 'trend', values: data }],
      xField: 'time',
      yField: 'value',
      seriesField: 'kind',
      legends: [{ visible: true, orient: 'top' as const }],
      axes: [
        { orient: 'bottom' as const, type: 'band' as const },
        { orient: 'left' as const, type: 'linear' as const },
      ],
      background: 'transparent',
    }),
    [data]
  )
  const title = props.latency
    ? t('Average processing time trend')
    : t('Video processing outcomes')
  return (
    <Card className='min-w-0'>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <div
          className='h-72'
          role='img'
          aria-label={title}
          aria-describedby={descriptionID}
        >
          {theme.themeReady && (
            <VChart
              spec={spec}
              option={VCHART_OPTION}
              theme={theme.resolvedTheme === 'dark' ? 'dark' : 'light'}
            />
          )}
        </div>
        <ul id={descriptionID} className='sr-only'>
          {data.map((point) => (
            <li key={`${point.time}-${point.kind}`}>
              {point.time} · {point.kind}: {formatNumber(point.value)}
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}

const stageLabels: Record<string, string> = {
  queue: 'Queue',
  decode: 'Base64 decoding',
  probe: 'Media probing',
  analyze: 'Frame analysis',
  select: 'Frame selection',
  export: 'JPEG export',
  rewrite: 'Request rewriting',
}

export function VideoPerformanceCharts(props: { filters: VideoFilters }) {
  const { t } = useTranslation()
  const theme = useChartTheme()
  const query = useQuery({
    queryKey: [...videoKeys.stats, props.filters],
    queryFn: () => getVideoStats(props.filters),
    meta: { errorToast: false },
  })
  if (query.isPending) return <LoadingState />
  if (query.isError) {
    return (
      <VideoQueryError
        error={query.error}
        onRetry={() => void query.refetch()}
      />
    )
  }
  const stats = query.data
  if (stats.total_requests === 0) {
    return (
      <EmptyState
        title={t('No video processing data')}
        description={t(
          'Choose another time range or process a video with logging enabled.'
        )}
      />
    )
  }
  const percentiles = (['p50', 'p95', 'p99'] as const).flatMap((percentile) => [
    {
      label: `${t('Queue')} ${percentile.toUpperCase()}`,
      value: stats.queue[percentile],
    },
    {
      label: `${t('Total')} ${percentile.toUpperCase()}`,
      value: stats.total[percentile],
    },
  ])
  const durations = Object.entries(stats.by_duration).map(([key, value]) => {
    if (key === 'over_1800') {
      return { label: t('Over {{count}} seconds', { count: 1800 }), value }
    }
    const [from, to] = key.split('_')
    return { label: t('{{from}}–{{to}} seconds', { from, to }), value }
  })
  return (
    <div className='space-y-4'>
      <div
        className='flex flex-wrap gap-4 text-sm'
        aria-label={t('Processing summary')}
      >
        <span>
          {t('Requests')}: {formatNumber(stats.total_requests)}
        </span>
        <span>
          {t('Success')}: {formatNumber(stats.success_count)}
        </span>
        <span>
          {t('Failed')}: {formatNumber(stats.failure_count)}
        </span>
        <span>
          {t('Rejected')}: {formatNumber(stats.rejected_count)}
        </span>
        <span>
          {t('Rewritten body ratio')}:{' '}
          {stats.original_body_bytes
            ? `${formatNumber((stats.output_body_bytes / stats.original_body_bytes) * 100)}%`
            : '—'}
        </span>
      </div>
      <div className='grid gap-4 lg:grid-cols-2'>
        <VideoTrendChart theme={theme} values={stats.trend} />
        <VideoTrendChart theme={theme} values={stats.trend} latency />
        <VideoDistributionChart
          theme={theme}
          title={t('Latency percentiles (ms)')}
          values={percentiles}
        />
        <VideoDistributionChart
          theme={theme}
          title={t('Average stage duration (ms)')}
          values={Object.entries(stats.stage_ms).map(([key, value]) => ({
            label: t(stageLabels[key] ?? key),
            value,
          }))}
        />
        <VideoDistributionChart
          theme={theme}
          title={t('Requests by codec')}
          values={Object.entries(stats.by_codec).map(([label, value]) => ({
            label,
            value,
          }))}
        />
        <VideoDistributionChart
          theme={theme}
          title={t('Video duration per request')}
          values={durations}
        />
        <VideoDistributionChart
          theme={theme}
          title={t('Extracted frame distribution')}
          values={Object.entries(stats.by_frames).map(([label, value]) => ({
            label,
            value,
          }))}
        />
        <VideoDistributionChart
          theme={theme}
          title={t('Successful request bytes (MB)')}
          values={[
            {
              label: t('Original body'),
              value: stats.original_body_bytes / 1048576,
            },
            {
              label: t('Rewritten body'),
              value: stats.output_body_bytes / 1048576,
            },
            {
              label: t('Output images'),
              value: stats.output_image_bytes / 1048576,
            },
          ]}
        />
        <VideoDistributionChart
          theme={theme}
          title={t('Video processing errors')}
          values={Object.entries(stats.by_error).map(([label, value]) => ({
            label,
            value,
          }))}
        />
      </div>
    </div>
  )
}
