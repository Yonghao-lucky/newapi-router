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
import { CheckCircle2, RefreshCw, Timer } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'

import { ErrorState } from '@/components/error-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { StaticDataTable } from '@/components/data-table'
import { StatCard } from '@/features/dashboard/components/ui/stat-card'
import { useChartTheme } from '@/lib/use-chart-theme'
import { VCHART_OPTION } from '@/lib/vchart'

import { getAIRouterChannelStats, getAIRouterStats, getAIRouterTrend } from './api'
import { AI_ROUTER_QUERY_KEYS } from './constants'
import type { AIRouterChannelStat, AIRouterTrendPoint } from './types'
import { CompactDateTimeRangePicker } from '@/features/usage-logs/components/compact-date-time-range-picker'

// Time-range filter state shared by the stat cards and both charts.
interface MonitoringRange {
  start?: Date
  end?: Date
}

interface MonitoringTabProps {
  // Clicking a channel row jumps to the request logs filtered by that channel.
  onChannelSelect?: (channelId: number) => void
}

const formatPercent = (value: number): string =>
  `${(value * 100).toFixed(1)}%`

const formatMs = (value: number): string => {
  if (value >= 1000) return `${(value / 1000).toFixed(1)}s`
  return `${Math.round(value)}ms`
}

// Counts are rendered with a thousands separator and never as an empty cell:
// a channel with zero 429s shows "0".
const formatCount = (value: number): string => value.toLocaleString()

export function MonitoringTab({ onChannelSelect }: MonitoringTabProps = {}) {
  const { t } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()
  const [range, setRange] = useState<MonitoringRange>({
    start: dayjs().startOf('day').toDate(),
    end: dayjs().endOf('day').toDate(),
  })
  const [model, setModel] = useState('')
  const [groupId, setGroupId] = useState('')
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null)

  const params = useMemo(() => {
    const start = range.start ?? dayjs().startOf('day').toDate()
    const end = range.end ?? dayjs().endOf('day').toDate()
    return {
      start_time: Math.floor(start.getTime() / 1000),
      end_time: Math.floor(end.getTime() / 1000),
      model: model || undefined,
      group_id: groupId || undefined,
    }
  }, [range, model, groupId])

  const statsQuery = useQuery({
    queryKey: [...AI_ROUTER_QUERY_KEYS.stats, params],
    queryFn: async () => getAIRouterStats(params),
  })
  const trendQuery = useQuery({
    queryKey: [...AI_ROUTER_QUERY_KEYS.trend, params],
    queryFn: async () => getAIRouterTrend(params),
  })
  const channelStatsQuery = useQuery({
    queryKey: [...AI_ROUTER_QUERY_KEYS.channelStats, params],
    queryFn: async () => getAIRouterChannelStats(params),
  })

  const isLoading = statsQuery.isLoading || trendQuery.isLoading
  const isError = statsQuery.isError || trendQuery.isError || channelStatsQuery.isError
  const error =
    statsQuery.error ?? trendQuery.error ?? channelStatsQuery.error
  const refetchAll = () => {
    void statsQuery.refetch()
    void trendQuery.refetch()
    void channelStatsQuery.refetch()
    setLastRefreshedAt(new Date())
  }

  const stats = statsQuery.data
  const trend = useMemo(() => trendQuery.data ?? [], [trendQuery.data])
  const channelStats = useMemo(
    () => channelStatsQuery.data ?? [],
    [channelStatsQuery.data]
  )
  const chartTheme = resolvedTheme === 'dark' ? 'dark' : 'light'

  const trendData = useMemo(
    () =>
      trend.map((point: AIRouterTrendPoint) => ({
        time: dayjs(point.time * 1000).format('MM-DD HH:mm'),
        success: point.success,
        failure: point.failure,
      })),
    [trend]
  )

  const trendChartSpec = useMemo(() => {
    return {
      type: 'common' as const,
      data: [
        { id: 'success', values: trendData.map((d) => ({ time: d.time, type: t('Success'), count: d.success })) },
        { id: 'failure', values: trendData.map((d) => ({ time: d.time, type: t('Failed'), count: d.failure })) },
      ],
      series: [
        {
          type: 'bar' as const,
          dataIndex: 0,
          xField: 'time',
          yField: 'count',
          seriesField: 'type',
          stack: true,
          bar: { style: { cornerRadius: [4, 4, 0, 0] } },
        },
        {
          type: 'bar' as const,
          dataIndex: 1,
          xField: 'time',
          yField: 'count',
          seriesField: 'type',
          stack: true,
          bar: { style: { cornerRadius: [4, 4, 0, 0] } },
        },
      ],
      axes: [
        { orient: 'bottom' as const, type: 'band' as const },
        { orient: 'left' as const, type: 'linear' as const },
      ],
      legends: [{ visible: true, orient: 'top' as const, position: 'start' }],
      background: 'transparent',
    }
  }, [trendData, t])

  const channelChartSpec = useMemo(() => {
    const data = channelStats
      .slice(0, 10)
      .map((stat: AIRouterChannelStat) => ({
        channel: `#${stat.channel_id}`,
        Attempts: stat.attempts,
        Finals: stat.finals,
      }))
    return {
      type: 'common' as const,
      data: [
        { id: 'attempts', values: data.map((d) => ({ channel: d.channel, Metric: t('Attempts'), Count: d.Attempts })) },
        { id: 'finals', values: data.map((d) => ({ channel: d.channel, Metric: t('Final selections'), Count: d.Finals })) },
      ],
      series: [
        {
          type: 'bar' as const,
          dataIndex: 0,
          xField: 'channel',
          yField: 'Count',
          seriesField: 'Metric',
        },
        {
          type: 'bar' as const,
          dataIndex: 1,
          xField: 'channel',
          yField: 'Count',
          seriesField: 'Metric',
        },
      ],
      axes: [
        { orient: 'bottom' as const, type: 'band' as const },
        { orient: 'left' as const, type: 'linear' as const },
      ],
      legends: [{ visible: true, orient: 'top' as const, position: 'start' }],
      background: 'transparent',
    }
  }, [channelStats, t])

  if (isError) {
    return <ErrorState description={error?.message} onRetry={refetchAll} />
  }

  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-center gap-2'>
        <CompactDateTimeRangePicker
          start={range.start}
          end={range.end}
          onChange={({ start, end }) => setRange({ start, end })}
        />
        <Input
          className='h-8 w-44'
          placeholder={t('Filter by model...')}
          value={model}
          onChange={(event) => setModel(event.target.value)}
        />
        <Input
          className='h-8 w-44'
          placeholder={t('Filter by group...')}
          value={groupId}
          onChange={(event) => setGroupId(event.target.value)}
        />
        <Button size='sm' variant='outline' onClick={refetchAll}>
          <RefreshCw className='h-4 w-4' />
          {t('Refresh')}
        </Button>
        {lastRefreshedAt && (
          <span className='text-muted-foreground text-xs'>
            {t('Updated at')} {dayjs(lastRefreshedAt).format('HH:mm:ss')}
          </span>
        )}
      </div>

      <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-3'>
        <StatCard
          title={t('Requests')}
          value={stats?.total_requests ?? 0}
          description={t('Routing requests in range')}
          icon={Timer}
          loading={isLoading}
        />
        <StatCard
          title={t('Success rate')}
          value={stats ? formatPercent(stats.success_rate) : '0%'}
          description={t('Requests that ended in success')}
          icon={CheckCircle2}
          loading={isLoading}
        />
        <StatCard
          title={t('Fallback rate')}
          value={stats ? formatPercent(stats.fallback_rate) : '0%'}
          description={t('Requests that needed more than one channel')}
          icon={RefreshCw}
          loading={isLoading}
        />
        <StatCard
          title={t('Avg total duration')}
          value={stats ? formatMs(stats.avg_duration_ms) : '-'}
          description={t('Average end-to-end duration')}
          icon={Timer}
          loading={isLoading}
        />
        <StatCard
          title={t('Avg TTFT')}
          value={stats ? formatMs(stats.avg_ttft_ms) : '-'}
          description={t('Average time to first token')}
          icon={Timer}
          loading={isLoading}
        />
        <StatCard
          title={t('Avg attempts')}
          value={stats ? stats.avg_attempts.toFixed(1) : '-'}
          description={t('Average channel attempts per request')}
          icon={RefreshCw}
          loading={isLoading}
        />
      </div>

      <div className='grid gap-4 lg:grid-cols-2'>
        <Card className='p-4'>
          <p className='mb-3 text-sm font-medium'>{t('Request trend')}</p>
          <div className='h-72'>
            {themeReady ? (
              <VChart
                spec={trendChartSpec}
                option={VCHART_OPTION}
                theme={chartTheme}
              />
            ) : (
              <Skeleton className='h-full w-full' />
            )}
          </div>
        </Card>
        <Card className='p-4'>
          <p className='mb-3 text-sm font-medium'>{t('Channel performance')}</p>
          <div className='h-72'>
            {themeReady ? (
              <VChart
                spec={channelChartSpec}
                option={VCHART_OPTION}
                theme={chartTheme}
              />
            ) : (
              <Skeleton className='h-full w-full' />
            )}
          </div>
        </Card>
      </div>

      <Card className='p-4'>
        <p className='mb-3 text-sm font-medium'>
          {t('Channel breakdown')}
        </p>
        {channelStats.length === 0 ? (
          <p className='text-muted-foreground rounded-lg border border-dashed p-4 text-center text-sm'>
            {t('No channel activity in the selected range.')}
          </p>
        ) : (
          <StaticDataTable
            columns={[
              {
                id: 'channel',
                header: t('Channel'),
                cell: (row) => (
                  <span className='font-medium tabular-nums'>
                    #{row.channel_id}
                  </span>
                ),
              },
              {
                id: 'attempts',
                header: t('Attempts'),
                className: 'text-right',
                cellClassName: 'text-right tabular-nums',
                cell: (row) => formatCount(row.attempts),
              },
              {
                id: 'success_rate',
                header: t('Success rate'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (row) => (
                  <Badge variant={row.success_rate >= 0.9 ? 'secondary' : 'outline'}>
                    {formatPercent(row.success_rate)}
                  </Badge>
                ),
              },
              ...([
                ['count_429', '429'],
                ['count_5xx', '5xx'],
                ['count_timeout', 'timeout'],
                ['count_connection', 'connection'],
                ['count_stream_interrupted', 'Stream interrupted'],
                ['count_other', 'Other'],
              ] as const).map(([field, label]) => ({
                id: field,
                header: t(label),
                className: 'text-right',
                cellClassName: 'text-right tabular-nums',
                cell: (row: AIRouterChannelStat) => formatCount(row[field]),
              })),
              {
                id: 'avg_duration',
                header: t('Avg duration'),
                className: 'text-right',
                cellClassName: 'text-right tabular-nums',
                cell: (row) => formatMs(row.avg_duration_ms),
              },
              {
                id: 'avg_ttft',
                header: t('Avg TTFT'),
                className: 'text-right',
                cellClassName: 'text-right tabular-nums',
                cell: (row) =>
                  row.avg_ttft_ms > 0 ? formatMs(row.avg_ttft_ms) : '-',
              },
            ]}
            data={channelStats}
            getRowKey={(row) => String(row.channel_id)}
            onRowClick={(row) => onChannelSelect?.(row.channel_id)}
          />
        )}
      </Card>
    </div>
  )
}
