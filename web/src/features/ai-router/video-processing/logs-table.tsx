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
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTablePage, useDataTable } from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { getVideoLogs, videoKeys } from './api'
import { statusLabels } from './constants'
import { VideoLogDetail } from './log-detail'
import { VideoQueryError } from './query-error'
import type { VideoFilters, VideoLog } from './types'

export function VideoLogsTable(props: { filters: VideoFilters }) {
  const { t } = useTranslation()
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })
  const [detailID, setDetailID] = useState<number | null>(null)
  const params = {
    ...props.filters,
    page: pagination.pageIndex + 1,
    page_size: pagination.pageSize,
  }
  const query = useQuery({
    queryKey: [...videoKeys.logs, params],
    queryFn: () => getVideoLogs(params),
    meta: { errorToast: false },
  })
  const columns = useMemo<ColumnDef<VideoLog>[]>(
    () => [
      {
        id: 'request',
        header: t('Request ID'),
        cell: ({ row }) => (
          <Button
            variant='link'
            className='h-auto max-w-48 justify-start truncate p-0'
            onClick={() => setDetailID(row.original.id)}
          >
            {row.original.request_id || `#${row.original.id}`}
          </Button>
        ),
      },
      {
        accessorKey: 'created_at',
        header: t('Time'),
        cell: ({ row }) =>
          new Date(row.original.created_at * 1000).toLocaleString(),
      },
      { accessorKey: 'model', header: t('Model') },
      { accessorKey: 'user_id', header: t('User ID') },
      {
        accessorKey: 'status',
        header: t('Status'),
        cell: ({ row }) => (
          <Badge
            variant={
              row.original.status === 'succeeded' ? 'secondary' : 'outline'
            }
          >
            {t(statusLabels[row.original.status])}
          </Badge>
        ),
      },
      { accessorKey: 'video_count', header: t('Videos') },
      { accessorKey: 'extracted_frames', header: t('Frames') },
      { accessorKey: 'codec', header: t('Codec') },
      { accessorKey: 'queue_ms', header: t('Queue time (ms)') },
      { accessorKey: 'total_ms', header: t('Total time (ms)') },
      { accessorKey: 'error_code', header: t('Error code') },
    ],
    [t]
  )
  const { table } = useDataTable({
    data: query.data?.items ?? [],
    columns,
    totalCount: query.data?.total ?? 0,
    getRowId: (row) => String(row.id),
    pagination,
    onPaginationChange: setPagination,
    manualPagination: true,
    manualFiltering: true,
    enableSorting: false,
    enableRowSelection: false,
  })
  if (query.isError) {
    return (
      <VideoQueryError
        error={query.error}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return (
    <>
      <DataTablePage
        table={table}
        columns={columns}
        isLoading={query.isPending}
        isFetching={query.isFetching}
        emptyTitle={t('No video processing logs')}
        emptyDescription={t(
          'Completed video requests appear here when processing logs are enabled.'
        )}
        toolbarProps={null}
        paginationInFooter={false}
        fixedHeight={false}
      />
      <VideoLogDetail id={detailID} onClose={() => setDetailID(null)} />
    </>
  )
}
