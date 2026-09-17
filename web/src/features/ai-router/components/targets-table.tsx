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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTablePage, useDataTable } from '@/components/data-table'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { requireServerSuccess } from '@/lib/server-error-message'

import { deleteAIRouterTargetPolicy, getAIRouterTargetPolicies } from '../api'
import { AI_ROUTER_QUERY_KEYS } from '../constants'
import type { AIRouterTargetPolicy } from '../types'
import { PolicyDrawer } from './policy-drawer'
import { useTargetPolicyColumns } from './target-policy-columns'
import { TargetPolicyRowActions } from './target-policy-row-actions'

// TargetsTable lists target policies with server-side search and paging.
export function TargetsTable() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [search, setSearch] = useState('')
  const [appliedSearch, setAppliedSearch] = useState('')
  const [drawerRow, setDrawerRow] = useState<AIRouterTargetPolicy | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [deleteRow, setDeleteRow] = useState<AIRouterTargetPolicy | null>(null)
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })

  const { data, isLoading, isFetching, isError, error, refetch } = useQuery({
    queryKey: [
      ...AI_ROUTER_QUERY_KEYS.targets,
      {
        search: appliedSearch,
        page: pagination.pageIndex + 1,
        pageSize: pagination.pageSize,
      },
    ],
    queryFn: async () =>
      requireServerSuccess(
        await getAIRouterTargetPolicies({
          page: pagination.pageIndex + 1,
          page_size: pagination.pageSize,
          search: appliedSearch || undefined,
        })
      ),
  })

  const policies = data?.items ?? []
  const totalCount = data?.total ?? 0

  const deleteMutation = useMutation({
    meta: { errorToast: false },
    mutationFn: async (id: number) => {
      await deleteAIRouterTargetPolicy(id)
    },
    onSuccess: () => {
      toast.success(t('Target policy deleted'))
      void queryClient.invalidateQueries({
        queryKey: AI_ROUTER_QUERY_KEYS.targets,
      })
      setDeleteRow(null)
    },
  })

  const columns = useTargetPolicyColumns({
    onEdit: (policy) => {
      setDrawerRow(policy)
      setDrawerOpen(true)
    },
    onCopy: (policy) => {
      setDrawerRow({ ...policy, id: 0, model: '' })
      setDrawerOpen(true)
    },
    onDelete: (policy) => setDeleteRow(policy),
  })

  const { table } = useDataTable({
    data: policies,
    getRowId: (policy) => String(policy.id),
    columns,
    totalCount,
    pagination,
    onPaginationChange: setPagination,
    manualPagination: true,
    manualFiltering: true,
  })

  if (isError) {
    return (
      <ErrorState
        description={error?.message}
        onRetry={() => void refetch()}
      />
    )
  }

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          className='h-8 w-64'
          placeholder={t('Search model or group...')}
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
        <div className='flex-1' />
        <Button
          size='sm'
          onClick={() => {
            setDrawerRow(null)
            setDrawerOpen(true)
          }}
        >
          {t('Create target policy')}
        </Button>
      </div>

      <DataTablePage
        table={table}
        columns={columns}
        isLoading={isLoading}
        isFetching={isFetching}
        emptyTitle={t('No target policies')}
        emptyDescription={t(
          'Create a policy to restrict Router channels per model.'
        )}
        toolbarProps={null}
      />

      <PolicyDrawer
        open={drawerOpen}
        onOpenChange={setDrawerOpen}
        currentRow={drawerRow}
      />

      <ConfirmDialog
        open={deleteRow !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteRow(null)
        }}
        title={t('Delete target policy')}
        desc={t('Delete the policy for "{{name}}"? This cannot be undone.', {
          name: deleteRow ? `${deleteRow.model} · ${deleteRow.group_id}` : '',
        })}
        confirmText={t('Delete')}
        destructive
        isLoading={deleteMutation.isPending}
        handleConfirm={() => {
          if (deleteRow) deleteMutation.mutate(deleteRow.id)
        }}
      />
    </div>
  )
}

// Keep the row-actions import used: row actions render inside the columns.
export { TargetPolicyRowActions }
