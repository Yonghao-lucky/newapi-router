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
import { ErrorState } from '@/components/error-state'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { getAIRouterChannels } from './api'
import { ChannelDrawer } from './components/channel-drawer'
import {
  AI_ROUTER_QUERY_KEYS,
  CHANNEL_STATUS_ENABLED,
  TOOL_SUPPORT_SUPPORTED,
  TOOL_SUPPORT_UNSUPPORTED,
} from './constants'
import type { AIRouterChannel, ChannelRoutingProfile } from './types'

// Tool badge: null hides the highlight (unverified / unset profile).
function toolBadgeState(profile: ChannelRoutingProfile): boolean | null {
  if (!profile.router_set || (profile.tool_support ?? '') === '') return null
  return profile.tool_support === TOOL_SUPPORT_SUPPORTED
}

// Sortable string for the Router status column.
function routerSortValue(profile: ChannelRoutingProfile): string {
  if (!profile.router_set) return 'unset'
  return profile.enabled ? 'on' : 'off'
}

// Capability badges: text defaults on, image/video/unknown default off.
function channelCapabilityBadges(
  channel: AIRouterChannel,
  t: (key: string) => string
) {
  const profile = channel.router
  // When the profile is unset, effective defaults apply (text on, others off).
  const set = profile.router_set
  const entries = [
    {
      key: 'T',
      on: set ? profile.supports_text !== false : true,
      label: t('Text'),
    },
    {
      key: 'I',
      on: set ? profile.supports_image === true : false,
      label: t('Image'),
    },
    {
      key: 'V',
      on: set ? profile.supports_video === true : false,
      label: t('Video'),
    },
    {
      key: 'U',
      on: set ? profile.unknown_fallback : false,
      label: t('Unknown'),
    },
    {
      key: 'Tool',
      on: toolBadgeState(profile),
      label:
        profile.tool_support === TOOL_SUPPORT_UNSUPPORTED
          ? t('No tools')
          : t('Tools'),
    },
  ]
  return (
    <div className='flex gap-1'>
      {entries.map((entry) => (
        <Badge
          key={entry.key}
          variant={entry.on === true ? 'secondary' : 'outline'}
          className={entry.on === true ? '' : 'text-muted-foreground'}
          title={entry.label}
        >
          {entry.key}
        </Badge>
      ))}
    </div>
  )
}

// ChannelConfigTab edits per-channel Router profiles and scheduling. It is
// the single place where priority, weight and capabilities are editable —
// the target policy drawer shows them read-only.
export function ChannelConfigTab() {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const [appliedSearch, setAppliedSearch] = useState('')
  const [drawerChannel, setDrawerChannel] = useState<AIRouterChannel | null>(
    null
  )
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })

  const { data, isLoading, isFetching, isError, error, refetch } = useQuery({
    queryKey: [
      ...AI_ROUTER_QUERY_KEYS.channels,
      {
        search: appliedSearch,
        page: pagination.pageIndex + 1,
        pageSize: pagination.pageSize,
      },
    ],
    queryFn: async () =>
      getAIRouterChannels({
        page: pagination.pageIndex + 1,
        page_size: pagination.pageSize,
        name: appliedSearch || undefined,
      }),
  })

  const channels = data?.items ?? []
  const totalCount = data?.total ?? 0

  const columns = useMemo<ColumnDef<AIRouterChannel, unknown>[]>(
    () => [
      {
        id: 'name',
        accessorFn: (row) => row.name,
        header: t('Channel'),
        cell: ({ row }) => (
          <div className='flex items-center gap-2'>
            <span className='font-medium'>{row.original.name}</span>
            <span className='text-muted-foreground text-xs'>
              #{row.original.id}
            </span>
          </div>
        ),
      },
      {
        id: 'group',
        accessorFn: (row) => row.group,
        header: t('Group'),
        cell: ({ row }) => (
          <Badge variant='outline'>{row.original.group}</Badge>
        ),
      },
      {
        id: 'status',
        accessorFn: (row) => row.status,
        header: t('Channel status'),
        cell: ({ row }) =>
          row.original.status === CHANNEL_STATUS_ENABLED ? (
            <StatusBadge variant='success' type='text'>
              {t('Enabled')}
            </StatusBadge>
          ) : (
            <StatusBadge variant='neutral' type='text'>
              {t('Disabled')}
            </StatusBadge>
          ),
      },
      {
        id: 'router',
        accessorFn: (row) => routerSortValue(row.router),
        header: t('Router status'),
        cell: ({ row }) => {
          const profile = row.original.router
          if (!profile.router_set) {
            return (
              <StatusBadge variant='neutral' type='text'>
                {t('Follow global')}
              </StatusBadge>
            )
          }
          return profile.enabled ? (
            <StatusBadge variant='success' type='text'>
              {t('Enabled')}
            </StatusBadge>
          ) : (
            <StatusBadge variant='danger' type='text'>
              {t('Disabled')}
            </StatusBadge>
          )
        },
      },
      {
        id: 'priority',
        accessorFn: (row) => row.priority,
        header: t('Priority'),
        cellClassName: 'tabular-nums',
      },
      {
        id: 'video_priority',
        accessorFn: (row) => row.router.video_priority,
        header: t('Video priority'),
        cell: ({ row }) => row.original.router.video_priority ?? t('Not set'),
        cellClassName: 'tabular-nums',
      },
      {
        id: 'weight',
        accessorFn: (row) => row.weight,
        header: t('Weight'),
        cellClassName: 'tabular-nums',
      },
      {
        id: 'capabilities',
        header: t('Capabilities'),
        cell: ({ row }) => channelCapabilityBadges(row.original, t),
      },
      {
        id: 'max_retry',
        accessorFn: (row) => row.router.max_retry ?? 0,
        header: t('Max retry'),
        cellClassName: 'tabular-nums',
      },
      {
        id: 'referenced',
        accessorFn: (row) => row.referenced_by?.length ?? 0,
        header: t('Referenced policies'),
        cell: ({ row }) => (
          <Badge variant='secondary'>
            {row.original.referenced_by?.length ?? 0}
          </Badge>
        ),
      },
      {
        id: 'actions',
        header: '',
        cell: ({ row }) => (
          <Button
            variant='outline'
            size='sm'
            onClick={() => {
              setDrawerChannel(row.original)
              setDrawerOpen(true)
            }}
          >
            {t('Configure')}
          </Button>
        ),
      },
    ],
    [t]
  )

  const { table } = useDataTable({
    data: channels,
    getRowId: (channel) => String(channel.id),
    columns,
    totalCount,
    pagination,
    onPaginationChange: setPagination,
    manualPagination: true,
    manualFiltering: true,
  })

  if (isError) {
    return (
      <ErrorState description={error?.message} onRetry={() => void refetch()} />
    )
  }

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          className='h-8 w-64'
          placeholder={t('Search channels...')}
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              setAppliedSearch(search)
              setPagination((prev) => ({ ...prev, pageIndex: 0 }))
            }
          }}
        />
        <Button
          size='sm'
          variant='outline'
          onClick={() => {
            setAppliedSearch(search)
            setPagination((prev) => ({ ...prev, pageIndex: 0 }))
          }}
        >
          {t('Search')}
        </Button>
      </div>

      <DataTablePage
        table={table}
        columns={columns}
        isLoading={isLoading}
        isFetching={isFetching}
        emptyTitle={t('No channels')}
        emptyDescription={t('Channels appear here once created.')}
        toolbarProps={null}
      />

      <ChannelDrawer
        open={drawerOpen}
        onOpenChange={setDrawerOpen}
        channel={drawerChannel}
      />
    </div>
  )
}
