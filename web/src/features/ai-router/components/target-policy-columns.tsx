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
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { BadgeCell } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { formatTimestampToDate } from '@/lib/format'

import type { AIRouterTargetPolicy } from '../types'
import { TargetPolicyRowActions } from './target-policy-row-actions'

interface ColumnCallbacks {
  onEdit: (policy: AIRouterTargetPolicy) => void
  onCopy: (policy: AIRouterTargetPolicy) => void
  onDelete: (policy: AIRouterTargetPolicy) => void
}

export function useTargetPolicyColumns(
  callbacks: ColumnCallbacks
): ColumnDef<AIRouterTargetPolicy, unknown>[] {
  const { t } = useTranslation()
  return useMemo(
    () => [
      {
        id: 'model',
        accessorFn: (row) => row.model,
        header: t('Model'),
        cell: ({ row }) => (
          <span className='font-medium'>{row.original.model}</span>
        ),
      },
      {
        id: 'group_id',
        accessorFn: (row) => row.group_id,
        header: t('Group'),
        cell: ({ row }) => (
          <Badge variant='outline'>{row.original.group_id}</Badge>
        ),
      },
      {
        id: 'enabled',
        accessorFn: (row) => (row.enabled ? 'enabled' : 'disabled'),
        header: t('Status'),
        cell: ({ row }) =>
          row.original.enabled ? (
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
        id: 'candidates',
        accessorFn: (row) => row.candidates.length,
        header: t('Candidates'),
        cell: ({ row }) => {
          const policy = row.original
          const enabledCount = policy.candidates.filter(
            (candidate) => candidate.enabled
          ).length
          return (
            <BadgeCell>
              <Badge variant='secondary'>
                {enabledCount}/{policy.candidates.length}
              </Badge>
            </BadgeCell>
          )
        },
      },
      {
        id: 'max_attempts',
        accessorFn: (row) => row.max_attempts,
        header: t('Max attempts'),
        cellClassName: 'tabular-nums',
      },
      {
        id: 'updated_at',
        accessorFn: (row) => row.updated_at,
        header: t('Updated At'),
        cell: ({ row }) => (
          <span className='text-muted-foreground text-xs'>
            {formatTimestampToDate(row.original.updated_at)}
          </span>
        ),
      },
      {
        id: 'actions',
        header: '',
        cell: ({ row }) => (
          <TargetPolicyRowActions
            policy={row.original}
            onEdit={callbacks.onEdit}
            onCopy={callbacks.onCopy}
            onDelete={callbacks.onDelete}
          />
        ),
      },
    ],
    [t, callbacks]
  )
}
