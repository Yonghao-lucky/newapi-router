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
import { useMutation, useQuery } from '@tanstack/react-query'
import { ArrowUpDown, FileJson, FileSpreadsheet } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTablePage, useDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import {
  SideDrawerSection,
  sideDrawerContentClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { StatusBadge } from '@/components/status-badge'
import { CompactDateTimeRangePicker } from '@/features/usage-logs/components/compact-date-time-range-picker'
import { formatTimestampToDate } from '@/lib/format'

import {
  aiRouterLogFiltersToParams,
  getAIRouterChannels,
  getAIRouterLogDetail,
  getAIRouterLogs,
} from '../api'
import {
  AI_ROUTER_QUERY_KEYS,
  ERROR_CATEGORY_OPTIONS,
  FALLBACK_FILTER_OPTIONS,
  MODALITY_OPTIONS,
  STREAM_INTERRUPTED_FILTER_OPTIONS,
  SUCCESS_FILTER_OPTIONS,
} from '../constants'
import type {
  AIRouterLog,
  AIRouterLogFilters,
} from '../types'

export interface LogsTableProps {
  // Set by a channel-performance row click: applies the channel filter and
  // re-runs the query. The nonce makes repeated clicks on the same channel
  // take effect again.
  channelJump?: { channelId: string; nonce: number } | null
}

const EMPTY_FILTERS: AIRouterLogFilters = {
  start_time: '',
  end_time: '',
  model: '',
  group_id: '',
  user_id: '',
  modality: '',
  success: '',
  fallback: '',
  channel_id: '',
  http_status_code: '',
  error_category: '',
  stream_interrupted: '',
}

// Parse "[4,9,10]" into [4, 9, 10]; tolerate malformed JSON.
const parseChannelIds = (raw: string): number[] => {
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed
      .map((value) => Number(value))
      .filter((value) => Number.isInteger(value) && value > 0)
  } catch {
    return []
  }
}

const defaultFilters = (): AIRouterLogFilters => ({ ...EMPTY_FILTERS })

// LogsTable lists AI Router request logs with rich filters, a compact
// attempt chain, a detail drawer and CSV/JSON export. Prompt and response
// bodies are never rendered — the telemetry does not store them.
export function LogsTable({ channelJump }: LogsTableProps = {}) {
  const { t } = useTranslation()
  const [filters, setFilters] = useState<AIRouterLogFilters>(defaultFilters)
  const [appliedFilters, setAppliedFilters] = useState<AIRouterLogFilters>(
    defaultFilters
  )
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })
  const [detailRow, setDetailRow] = useState<AIRouterLog | null>(null)

  // A channel click on the performance table sets both the draft and the
  // applied filter, so the list refreshes without a second click on Search.
  useEffect(() => {
    if (!channelJump) return
    setFilters((prev) => ({ ...prev, channel_id: channelJump.channelId }))
    setAppliedFilters((prev) => ({
      ...prev,
      channel_id: channelJump.channelId,
    }))
    setPagination((prev) => ({ ...prev, pageIndex: 0 }))
  }, [channelJump])

  // Per-attempt rows of the open drawer (Phase 3 detail endpoint). Old logs
  // have no rows; the chain view alone still renders.
  const detailQuery = useQuery({
    queryKey: [...AI_ROUTER_QUERY_KEYS.logs, 'detail', detailRow?.id],
    queryFn: async () => {
      if (detailRow === null) return null
      return getAIRouterLogDetail(detailRow.id)
    },
    enabled: detailRow !== null,
  })

  const params = useMemo(
    () => aiRouterLogFiltersToParams(appliedFilters, pagination.pageIndex + 1, pagination.pageSize),
    [appliedFilters, pagination]
  )

  const { data, isLoading, isFetching, isError, error, refetch } = useQuery({
    queryKey: [...AI_ROUTER_QUERY_KEYS.logs, params],
    queryFn: async () => getAIRouterLogs(params),
  })

  // Channel options for the attempt-channel filter (first 100 channels).
  const channelsQuery = useQuery({
    queryKey: [...AI_ROUTER_QUERY_KEYS.channels, 'filter-options'],
    queryFn: () => getAIRouterChannels({ page_size: 100 }),
  })

  const channelNameMap = useMemo(() => {
    const map = new Map<number, string>()
    for (const channel of channelsQuery.data?.items ?? []) {
      map.set(channel.id, channel.name)
    }
    return map
  }, [channelsQuery.data])

  const exportMutation = useMutation({
    meta: { errorToast: false },
    mutationFn: async (format: 'csv' | 'json') => {
      // Export up to 1000 rows of the current filter client-side; the
      // backend has no dedicated export endpoint in V1.5.
      const rows = await getAIRouterLogs({
        ...aiRouterLogFiltersToParams(appliedFilters, 1, 1000),
        page_size: 1000,
      })
      return { format, items: rows.items }
    },
    onSuccess: ({ format, items }) => {
      if (items.length === 0) {
        toast.warning(t('No logs to export'))
        return
      }
      const timestamp = new Date()
        .toISOString()
        .slice(0, 19)
        .replaceAll(/[:T]/g, '-')
      let blob: Blob
      if (format === 'json') {
        blob = new Blob([JSON.stringify(items, null, 2)], {
          type: 'application/json',
        })
      } else {
        const header = [
          'id',
          'request_id',
          'created_at',
          'model',
          'group_id',
          'user_id',
          'modality',
          'has_tools',
          'attempts',
          'max_attempts',
          'selected_channels',
          'final_channel_id',
          'success',
          'ttft_ms',
          'total_duration_ms',
          'error_message',
        ]
        const escape = (value: string | number | boolean): string => {
          const text = String(value)
          return /[",\n]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text
        }
        const lines = [header.join(',')]
        for (const item of items) {
          lines.push(
            [
              item.id,
              item.request_id,
              item.created_at,
              escape(item.model),
              escape(item.group_id),
              item.user_id,
              item.modality,
              item.has_tools,
              item.attempts,
              item.max_attempts,
              escape(item.selected_channels),
              item.final_channel_id,
              item.success,
              item.ttft_ms,
              item.total_duration_ms,
              escape(item.error_message),
            ].join(',')
          )
        }
        blob = new Blob([lines.join('\n')], { type: 'text/csv' })
      }
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `ai-router-logs-${format}-${timestamp}.${format}`
      anchor.click()
      URL.revokeObjectURL(url)
      toast.success(t('Export started'))
    },
  })

  const logs = data?.items ?? []
  const totalCount = data?.total ?? 0

  const columns = useMemo(
    () => [
      {
        id: 'time',
        accessorFn: (row: AIRouterLog) => row.created_at,
        header: t('Time'),
        cell: ({ row }: { row: { original: AIRouterLog } }) => (
          <span className='text-muted-foreground text-xs'>
            {formatTimestampToDate(row.original.created_at)}
          </span>
        ),
      },
      {
        id: 'model',
        accessorFn: (row: AIRouterLog) => row.model,
        header: t('Model'),
        cell: ({ row }: { row: { original: AIRouterLog } }) => (
          <span className='font-medium'>{row.original.model}</span>
        ),
      },
      {
        id: 'group',
        accessorFn: (row: AIRouterLog) => row.group_id,
        header: t('Group'),
        cell: ({ row }: { row: { original: AIRouterLog } }) => (
          <Badge variant='outline'>{row.original.group_id}</Badge>
        ),
      },
      {
        id: 'chain',
        header: t('Attempt chain'),
        cell: ({ row }: { row: { original: AIRouterLog } }) => {
          const log = row.original
          const channelIds = parseChannelIds(log.selected_channels)
          // Occurrence count per channel id: a channel may appear twice in
          // one chain (same-channel retries), so keys need a suffix beyond
          // the id itself.
          const seen = new Map<number, number>()
          return (
            <div className='flex max-w-md flex-wrap items-center gap-1'>
              {channelIds.length === 0 ? (
                <span className='text-muted-foreground text-xs'>-</span>
              ) : (
                channelIds.map((channelId, index) => {
                  const isFinal = channelId === log.final_channel_id
                  const name =
                    channelNameMap.get(channelId) ?? `#${channelId}`
                  const occurrence = seen.get(channelId) ?? 0
                  seen.set(channelId, occurrence + 1)
                  return (
                    <span
                      key={`${log.id}-${channelId}-${occurrence}`}
                      className='flex items-center gap-1'
                    >
                      {index > 0 && (
                        <span className='text-muted-foreground text-xs'>→</span>
                      )}
                      <span
                        className={
                          isFinal
                            ? 'text-foreground text-xs font-medium'
                            : 'text-muted-foreground text-xs'
                        }
                      >
                        {name} (#{channelId})
                      </span>
                      {isFinal && (
                        <Badge
                          variant={log.success ? 'secondary' : 'destructive'}
                          className='text-[10px]'
                        >
                          {log.success ? t('Success') : t('Failed')}
                        </Badge>
                      )}
                    </span>
                  )
                })
              )}
              {log.attempts > channelIds.length && (
                <Badge variant='outline' className='text-[10px]'>
                  {t('{{count}} retries', { count: log.attempts - channelIds.length })}
                </Badge>
              )}
            </div>
          )
        },
      },
      {
        id: 'result',
        accessorFn: (row: AIRouterLog) => (row.success ? '1' : '0'),
        header: t('Result'),
        cell: ({ row }: { row: { original: AIRouterLog } }) =>
          row.original.success ? (
            <StatusBadge variant='success' type='text'>
              {t('Success')}
            </StatusBadge>
          ) : (
            <StatusBadge variant='danger' type='text'>
              {t('Failed')}
            </StatusBadge>
          ),
      },
      {
        id: 'duration',
        accessorFn: (row: AIRouterLog) => row.total_duration_ms,
        header: t('Duration'),
        cellClassName: 'tabular-nums',
        cell: ({ row }: { row: { original: AIRouterLog } }) => (
          <span className='tabular-nums'>
            {row.original.total_duration_ms}ms
          </span>
        ),
      },
      {
        id: 'actions',
        header: '',
        cell: ({ row }: { row: { original: AIRouterLog } }) => (
          <Button
            variant='ghost'
            size='sm'
            onClick={() => setDetailRow(row.original)}
          >
            {t('Details')}
          </Button>
        ),
      },
    ],
    [t, channelNameMap]
  )

  const { table } = useDataTable({
    data: logs,
    getRowId: (log) => String(log.id),
    columns,
    totalCount,
    pagination,
    onPaginationChange: setPagination,
    manualPagination: true,
    manualFiltering: true,
  })

  const setFilter = (patch: Partial<AIRouterLogFilters>) => {
    setFilters((prev) => ({ ...prev, ...patch }))
  }

  const applyFilters = (next?: AIRouterLogFilters) => {
    setAppliedFilters(next ?? filters)
    setPagination((prev) => ({ ...prev, pageIndex: 0 }))
  }

  const resetFilters = () => {
    setFilters(defaultFilters())
    setAppliedFilters(defaultFilters())
    setPagination((prev) => ({ ...prev, pageIndex: 0 }))
  }

  if (isError) {
    return (
      <ErrorState description={error?.message} onRetry={() => void refetch()} />
    )
  }

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <CompactDateTimeRangePicker
          start={
            filters.start_time ? new Date(Number(filters.start_time) * 1000) : undefined
          }
          end={filters.end_time ? new Date(Number(filters.end_time) * 1000) : undefined}
          onChange={({ start, end }) =>
            setFilter({
              start_time: start ? String(Math.floor(start.getTime() / 1000)) : '',
              end_time: end ? String(Math.floor(end.getTime() / 1000)) : '',
            })
          }
        />
        <Input
          className='h-8 w-36'
          placeholder={t('Model')}
          value={filters.model}
          onChange={(event) => setFilter({ model: event.target.value })}
        />
        <Input
          className='h-8 w-36'
          placeholder={t('Group')}
          value={filters.group_id}
          onChange={(event) => setFilter({ group_id: event.target.value })}
        />
        <Input
          className='h-8 w-24'
          placeholder={t('User ID')}
          value={filters.user_id}
          onChange={(event) => setFilter({ user_id: event.target.value })}
        />
        <Select
          value={filters.modality}
          onValueChange={(value) => setFilter({ modality: value ?? '' })}
        >
          <SelectTrigger className='w-32' aria-label={t('Modality')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value=''>{t('All modalities')}</SelectItem>
              {MODALITY_OPTIONS.map((option) => (
                <SelectItem key={option} value={option}>
                  {t(option)}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <Select
          value={filters.success}
          onValueChange={(value) => setFilter({ success: value ?? '' })}
        >
          <SelectTrigger className='w-32' aria-label={t('Result')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value=''>{t('All results')}</SelectItem>
              {SUCCESS_FILTER_OPTIONS.map((option) => (
                <SelectItem key={option} value={option}>
                  {option === 'true' ? t('Success') : t('Failed')}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <Select
          value={filters.fallback}
          onValueChange={(value) => setFilter({ fallback: value ?? '' })}
        >
          <SelectTrigger className='w-32' aria-label={t('Fallback')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value=''>{t('All requests')}</SelectItem>
              {FALLBACK_FILTER_OPTIONS.map((option) => (
                <SelectItem key={option} value={option}>
                  {option === 'true' ? t('With fallback') : t('No fallback')}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <Select
          value={filters.channel_id}
          onValueChange={(value) => setFilter({ channel_id: value ?? '' })}
        >
          <SelectTrigger className='w-40' aria-label={t('Channel')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value=''>{t('All channels')}</SelectItem>
              {(channelsQuery.data?.items ?? []).map((channel) => (
                <SelectItem key={channel.id} value={String(channel.id)}>
                  {channel.name} (#{channel.id})
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <Input
          className='h-8 w-28'
          placeholder={t('Status code')}
          value={filters.http_status_code}
          onChange={(event) =>
            setFilter({ http_status_code: event.target.value })
          }
        />
        <Select
          value={filters.error_category}
          onValueChange={(value) => setFilter({ error_category: value ?? '' })}
        >
          <SelectTrigger className='w-36' aria-label={t('Error category')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value=''>{t('All error categories')}</SelectItem>
              {ERROR_CATEGORY_OPTIONS.map((option) => (
                <SelectItem key={option} value={option}>
                  {t(option)}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <Select
          value={filters.stream_interrupted}
          onValueChange={(value) =>
            setFilter({ stream_interrupted: value ?? '' })
          }
        >
          <SelectTrigger className='w-40' aria-label={t('Stream interrupted')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value=''>{t('All streams')}</SelectItem>
              {STREAM_INTERRUPTED_FILTER_OPTIONS.map((option) => (
                <SelectItem key={option} value={option}>
                  {option === 'true' ? t('Interrupted') : t('Not interrupted')}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <Button size='sm' onClick={() => applyFilters()}>
          {t('Search')}
        </Button>
        <Button size='sm' variant='outline' onClick={resetFilters}>
          {t('Reset')}
        </Button>
        <div className='flex-1' />
        <Button
          size='sm'
          variant='outline'
          onClick={() => exportMutation.mutate('csv')}
          disabled={exportMutation.isPending}
        >
          <FileSpreadsheet className='h-4 w-4' />
          CSV
        </Button>
        <Button
          size='sm'
        variant='outline'
          onClick={() => exportMutation.mutate('json')}
          disabled={exportMutation.isPending}
        >
          <FileJson className='h-4 w-4' />
          JSON
        </Button>
      </div>

      <DataTablePage
        table={table}
        columns={columns}
        isLoading={isLoading}
        isFetching={isFetching}
        emptyTitle={t('No request logs')}
        emptyDescription={t(
          'Logs appear once AI Router handles requests with telemetry enabled.'
        )}
        toolbarProps={null}
      />

      <Sheet
        open={detailRow !== null}
        onOpenChange={(open) => {
          if (!open) setDetailRow(null)
        }}
      >
        <SheetContent className={sideDrawerContentClassName('sm:max-w-[560px]')}>
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle className='pr-6 break-all'>
              {t('Request')} {detailRow?.request_id}
            </SheetTitle>
            <SheetDescription>
              {detailRow
                ? `${detailRow.model} · ${detailRow.group_id}`
                : ''}
            </SheetDescription>
          </SheetHeader>
          <div className='flex flex-col gap-4 overflow-y-auto px-4 py-4 sm:px-6'>
            <SideDrawerSection>
              <div className='grid gap-2 text-sm'>
                <div className='flex justify-between gap-2'>
                  <span className='text-muted-foreground'>{t('Time')}</span>
                  <span>
                    {detailRow
                      ? formatTimestampToDate(detailRow.created_at)
                      : ''}
                  </span>
                </div>
                <div className='flex justify-between gap-2'>
                  <span className='text-muted-foreground'>{t('User ID')}</span>
                  <span>{detailRow?.user_id ?? '-'}</span>
                </div>
                <div className='flex justify-between gap-2'>
                  <span className='text-muted-foreground'>{t('Modality')}</span>
                  <span>{detailRow?.modality ?? '-'}</span>
                </div>
                <div className='flex justify-between gap-2'>
                  <span className='text-muted-foreground'>{t('Tool calls')}</span>
                  <span>
                    {detailRow?.has_tools ? t('Yes') : t('No')}
                  </span>
                </div>
                <div className='flex justify-between gap-2'>
                  <span className='text-muted-foreground'>{t('Attempts')}</span>
                  <span className='tabular-nums'>
                    {detailRow?.attempts ?? 0} / {detailRow?.max_attempts ?? 0}
                  </span>
                </div>
                <div className='flex justify-between gap-2'>
                  <span className='text-muted-foreground'>{t('TTFT')}</span>
                  <span className='tabular-nums'>
                    {detailRow?.ttft_ms ? `${detailRow.ttft_ms}ms` : '-'}
                  </span>
                </div>
                <div className='flex justify-between gap-2'>
                  <span className='text-muted-foreground'>{t('Duration')}</span>
                  <span className='tabular-nums'>
                    {detailRow?.total_duration_ms ?? 0}ms
                  </span>
                </div>
                <div className='flex justify-between gap-2'>
                  <span className='text-muted-foreground'>{t('Result')}</span>
                  <span>
                    {detailRow?.success ? (
                      <StatusBadge variant='success' type='text'>
                        {t('Success')}
                      </StatusBadge>
                    ) : (
                      <StatusBadge variant='danger' type='text'>
                        {t('Failed')}
                      </StatusBadge>
                    )}
                  </span>
                </div>
              </div>
            </SideDrawerSection>

            <SideDrawerSection>
              <div className='space-y-2'>
                <p className='text-sm font-medium'>{t('Attempt chain')}</p>
                {detailRow &&
                  (() => {
                    const seenDetail = new Map<number, number>()
                    return parseChannelIds(detailRow.selected_channels).map(
                      (channelId, index, all) => {
                        const isFinal =
                          channelId === detailRow.final_channel_id
                        const isLast = index === all.length - 1
                        const occurrence = seenDetail.get(channelId) ?? 0
                        seenDetail.set(channelId, occurrence + 1)
                        return (
                        <div
                          key={`${detailRow.id}-${channelId}-${occurrence}`}
                          className='flex items-center gap-2 text-sm'
                        >
                          <span className='bg-muted text-muted-foreground flex h-5 w-5 items-center justify-center rounded-full text-[10px] tabular-nums'>
                            {index + 1}
                          </span>
                          <span className='font-medium'>
                            {channelNameMap.get(channelId) ?? `#${channelId}`}
                          </span>
                          <span className='text-muted-foreground text-xs'>
                            (#{channelId})
                          </span>
                          {isFinal && (
                            <Badge
                              variant={detailRow.success ? 'secondary' : 'destructive'}
                            >
                              {detailRow.success ? t('Success') : t('Failed')}
                            </Badge>
                          )}
                          {!isFinal && !isLast && (
                            <ArrowUpDown className='text-muted-foreground h-3 w-3 rotate-90' />
                          )}
                        </div>
                        )
                      }
                    )
                  })()}
                {detailRow && detailRow.unknown_types && (
                  <p className='text-muted-foreground text-xs'>
                    {t('Unknown types')}: {detailRow.unknown_types}
                  </p>
                )}
                {detailRow && !detailRow.success && detailRow.error_message && (
                  <p className='text-destructive mt-2 text-xs'>
                    {detailRow.error_message}
                  </p>
                )}
              </div>
            </SideDrawerSection>

            {detailRow &&
              (() => {
                const attempts = detailQuery.data?.attempts ?? []
                return (
                  attempts.length > 0 && (
                    <SideDrawerSection>
                      <div className='space-y-2'>
                        <p className='text-sm font-medium'>
                          {t('Attempt details')}
                        </p>
                        {attempts.map((attempt) => (
                          <div
                            key={attempt.id}
                            className='rounded-md border p-2 text-sm'
                          >
                            <div className='flex items-center gap-2'>
                              <span className='bg-muted text-muted-foreground flex h-5 w-5 items-center justify-center rounded-full text-[10px] tabular-nums'>
                                {attempt.attempt_seq}
                              </span>
                              <span className='font-medium'>
                                {attempt.channel_name || `#${attempt.channel_id}`}
                              </span>
                              <span className='text-muted-foreground text-xs'>
                                (#{attempt.channel_id})
                              </span>
                              {attempt.channel_retry_no > 0 && (
                                <span className='text-muted-foreground text-xs'>
                                  {t('Retry')} {attempt.channel_retry_no}
                                </span>
                              )}
                              <span className='flex-1' />
                              {attempt.success ? (
                                <StatusBadge variant='success' type='text'>
                                  {t('Success')}
                                </StatusBadge>
                              ) : (
                                <StatusBadge variant='danger' type='text'>
                                  {t(attempt.error_category)}
                                </StatusBadge>
                              )}
                            </div>
                            <div className='text-muted-foreground mt-1 flex gap-3 text-xs'>
                              <span className='tabular-nums'>
                                {attempt.http_status_code !== null
                                  ? `HTTP ${attempt.http_status_code}`
                                  : t('No response')}
                              </span>
                              <span className='tabular-nums'>
                                {attempt.duration_ms}ms
                              </span>
                              {attempt.ttft_ms !== null && (
                                <span className='tabular-nums'>
                                  TTFT {attempt.ttft_ms}ms
                                </span>
                              )}
                              {attempt.retryable && <span>{t('Retryable')}</span>}
                            </div>
                          </div>
                        ))}
                      </div>
                    </SideDrawerSection>
                  )
                )
              })()}
          </div>
        </SheetContent>
      </Sheet>
    </div>
  )
}
