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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ComboboxInput } from '@/components/ui/combobox-input'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { StaticDataTable } from '@/components/data-table'
import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  SideDrawerSection,
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { StatusBadge } from '@/components/status-badge'
import { Switch } from '@/components/ui/switch'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  createAIRouterTargetPolicy,
  getAIRouterChannels,
  getAIRouterTargetPolicy,
  updateAIRouterTargetPolicy,
  validateAIRouterTargetPolicy,
} from '../api'
import { AI_ROUTER_QUERY_KEYS, CHANNEL_STATUS_ENABLED } from '../constants'
import {
  targetPolicyFormSchema,
  transformFormToPolicyPayload,
  transformPolicyToFormDefaults,
} from '../lib/target-policy-form'
import type {
  AIRouterTargetPolicy,
  AIRouterValidationIssue,
  TargetPolicyChannelInfo,
} from '../types'
import { ValidationAlert } from './validation-alert'

// PolicyDrawer creates, edits or copies one target policy. Candidates carry
// only channel + enabled: scheduling and capabilities are read-only views of
// the Channel itself and are edited in the Channel Config tab.
export function PolicyDrawer(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Policy to edit; pass a policy with id=0 to copy, or null to create.
  currentRow: AIRouterTargetPolicy | null
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const isEditing = props.currentRow !== null && props.currentRow.id > 0
  const form = useForm({
    resolver: zodResolver(targetPolicyFormSchema),
    defaultValues: transformPolicyToFormDefaults(props.currentRow),
  })
  const [issues, setIssues] = useState<AIRouterValidationIssue[]>([])
  const [closeConfirm, setCloseConfirm] = useState(false)
  const [pendingRemove, setPendingRemove] = useState<number | null>(null)

  // Load the full policy when editing: the list row may be stale and the
  // detail endpoint refreshes the joined channel info.
  const detailQuery = useQuery({
    queryKey: [...AI_ROUTER_QUERY_KEYS.targets, 'detail', props.currentRow?.id],
    queryFn: async () => {
      const id = props.currentRow?.id ?? 0
      if (id <= 0) throw new Error('Policy id is required')
      return requireServerSuccess(await getAIRouterTargetPolicy(id))
    },
    enabled: props.open && isEditing,
  })
  const savedRow = detailQuery.data ?? props.currentRow

  useEffect(() => {
    if (!props.open) return
    setIssues([])
    setCloseConfirm(false)
    if (!isEditing || detailQuery.isSuccess) {
      form.reset(transformPolicyToFormDefaults(savedRow))
    }
  }, [props.open, savedRow, detailQuery.isSuccess, form, isEditing])

  const candidateIds = form.watch('candidates')
  const candidateChannelMap = useMemo(() => {
    const map = new Map<number, TargetPolicyChannelInfo>()
    for (const candidate of savedRow?.candidates ?? []) {
      if (candidate.channel) {
        map.set(candidate.channel_id, candidate.channel)
      }
    }
    return map
  }, [savedRow])

  // Channel picker options: every known channel minus the ones already added.
  const channelsQuery = useQuery({
    queryKey: [...AI_ROUTER_QUERY_KEYS.channels, 'picker'],
    queryFn: async () =>
      requireServerSuccess(await getAIRouterChannels({ page_size: 100 })),
    enabled: props.open,
  })
  const channelOptions = useMemo(() => {
    const added = new Set((candidateIds ?? []).map((c) => c.channel_id))
    return (channelsQuery.data?.items ?? [])
      .filter((channel) => !added.has(channel.id))
      .map((channel) => ({
        value: String(channel.id),
        label: `${channel.name} (#${channel.id})`,
        description: channel.group,
      }))
  }, [channelsQuery.data, candidateIds])

  const addCandidate = (value: string) => {
    const channelId = Number(value)
    if (!channelId) return
    const current = form.getValues('candidates') ?? []
    if (current.some((c) => c.channel_id === channelId)) return
    form.setValue(
      'candidates',
      [...current, { channel_id: channelId, enabled: true }],
      { shouldDirty: true }
    )
  }

  const removeCandidate = (channelId: number) => {
    form.setValue(
      'candidates',
      (form.getValues('candidates') ?? []).filter(
        (c) => c.channel_id !== channelId
      ),
      { shouldDirty: true }
    )
  }

  const toggleCandidate = (channelId: number, enabled: boolean) => {
    const current = form.getValues('candidates') ?? []
    form.setValue(
      'candidates',
      current.map((c) => (c.channel_id === channelId ? { ...c, enabled } : c)),
      { shouldDirty: true }
    )
  }

  const save = useMutation({
    meta: { errorToast: false },
    mutationFn: async () => {
      const payload = transformFormToPolicyPayload(form.getValues())
      const policyId = props.currentRow?.id ?? 0
      const result =
        isEditing && policyId > 0
          ? await updateAIRouterTargetPolicy(policyId, payload)
          : await createAIRouterTargetPolicy(payload)
      return result
    },
    onSuccess: (result) => {
      setIssues(result.issues ?? [])
      if (result.blocking) {
        toast.error(t('Fix validation errors before saving'))
        return
      }
      const warnings = (result.issues ?? []).filter(
        (issue) => issue.level !== 'error'
      )
      if (warnings.length > 0) {
        toast.warning(t('Saved with validation warnings'))
      } else {
        toast.success(t('Target policy saved'))
      }
      void queryClient.invalidateQueries({
        queryKey: AI_ROUTER_QUERY_KEYS.targets,
      })
      props.onOpenChange(false)
    },
  })
  const isSubmitting = save.isPending
  const dirty = form.formState.isDirty
  const close = (open: boolean) => {
    if (!open && isSubmitting) return
    if (!open && dirty) {
      setCloseConfirm(true)
      return
    }
    props.onOpenChange(open)
  }

  const candidateRows = useMemo(() => {
    return (candidateIds ?? []).map((candidate) => ({
      candidate,
      info: candidateChannelMap.get(candidate.channel_id),
    }))
  }, [candidateIds, candidateChannelMap])

  const runValidate = () => {
    const payload = transformFormToPolicyPayload(form.getValues())
    void validateAIRouterTargetPolicy(payload).then((result) => {
      setIssues(result.issues)
      if (result.blocking) {
        toast.error(t('Fix validation errors before saving'))
      } else if (result.issues.length === 0) {
        toast.success(t('Validation passed'))
      }
    })
  }

  return (
    <>
      <Sheet open={props.open} onOpenChange={close}>
        <SheetContent
          className={sideDrawerContentClassName('sm:max-w-[760px]')}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle className='pr-6 break-all'>
              {isEditing ? t('Edit target policy') : t('Create target policy')}
            </SheetTitle>
            <SheetDescription>
              {t(
                'Candidates restrict which channels the Router may select for the model.'
              )}
            </SheetDescription>
          </SheetHeader>

          <Form {...form}>
            <form
              onSubmit={(event) => {
                event.preventDefault()
                save.mutate()
              }}
              className={sideDrawerFormClassName()}
            >
              <SideDrawerSection>
                <div className='grid gap-4 sm:grid-cols-2'>
                  <FormField
                    control={form.control}
                    name='model'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Model')}</FormLabel>
                        <FormControl>
                          <Input
                            {...field}
                            disabled={isEditing}
                            placeholder='kimi-k3'
                          />
                        </FormControl>
                        <FormDescription>
                          {isEditing
                            ? t('Model cannot be changed after creation.')
                            : t('Exact model name as requested by clients.')}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='group_id'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Group')}</FormLabel>
                        <FormControl>
                          <Input {...field} disabled={isEditing} />
                        </FormControl>
                        <FormDescription>
                          {isEditing
                            ? t('Group cannot be changed after creation.')
                            : t('Token group this policy applies to.')}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='enabled'
                    render={({ field }) => (
                      <FormItem className='flex flex-row items-center justify-between gap-3 rounded-lg border p-3 sm:col-span-2'>
                        <div className='space-y-0.5'>
                          <FormLabel>{t('Enabled')}</FormLabel>
                          <FormDescription>
                            {t(
                              'Disabled policies stop restricting routing but stay configured.'
                            )}
                          </FormDescription>
                        </div>
                        <FormControl>
                          <Switch
                            checked={field.value}
                            onCheckedChange={field.onChange}
                          />
                        </FormControl>
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='max_attempts'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Max attempts')}</FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min={1}
                            max={10}
                            className='w-24'
                            value={field.value}
                            onChange={(event) =>
                              field.onChange(
                                Number.parseInt(event.target.value, 10) || 1
                              )
                            }
                          />
                        </FormControl>
                        <FormDescription>
                          {t('Distinct channels tried per request.')}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </div>
              </SideDrawerSection>

              <SideDrawerSection>
                <div className='space-y-2'>
                  <div className='flex flex-wrap items-end justify-between gap-3'>
                    <div className='min-w-48 flex-1'>
                      <p className='text-sm font-medium'>
                        {t('Candidate channels')}
                      </p>
                      <p className='text-muted-foreground text-xs'>
                        {t(
                          'Priority, weight and capabilities come from each channel and are edited in Channel Config.'
                        )}
                      </p>
                    </div>
                    <div className='w-56'>
                      <ComboboxInput
                        options={channelOptions}
                        value=''
                        onValueChange={addCandidate}
                        placeholder={t('Add channel...')}
                        emptyText={t('No channels found')}
                      />
                    </div>
                  </div>

                  {candidateRows.length === 0 ? (
                    <p className='text-muted-foreground rounded-lg border border-dashed p-4 text-center text-sm'>
                      {t('No candidates yet — add at least one channel.')}
                    </p>
                  ) : (
                    <StaticDataTable
                      columns={[
                        {
                          id: 'channel',
                          header: t('Channel'),
                          cell: (row) => (
                            <div className='flex items-center gap-2'>
                              <span className='font-medium'>
                                {row.info?.name ??
                                  `${t('Deleted channel')} (#${row.candidate.channel_id})`}
                              </span>
                              <span className='text-muted-foreground text-xs'>
                                #{row.candidate.channel_id}
                              </span>
                            </div>
                          ),
                        },
                        {
                          id: 'status',
                          header: t('Status'),
                          cell: (row) => {
                            if (!row.info) {
                              return (
                                <Badge variant='outline'>
                                  {t('Deleted channel')}
                                </Badge>
                              )
                            }
                            return row.info.status === CHANNEL_STATUS_ENABLED ? (
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
                          header: t('Priority'),
                          cellClassName: 'tabular-nums',
                          cell: (row) => row.info?.priority ?? '-',
                        },
                        {
                          id: 'weight',
                          header: t('Weight'),
                          cellClassName: 'tabular-nums',
                          cell: (row) => row.info?.weight ?? '-',
                        },
                        {
                          id: 'capabilities',
                          header: t('Capabilities'),
                          cell: (row) => renderCapabilityBadges(row.info, t),
                        },
                        {
                          id: 'included',
                          header: t('Included'),
                          cell: (row) => (
                            <Switch
                              checked={row.candidate.enabled}
                              onCheckedChange={(checked) =>
                                toggleCandidate(row.candidate.channel_id, checked)
                              }
                            />
                          ),
                        },
                        {
                          id: 'actions',
                          header: '',
                          cell: (row) => (
                            <Button
                              variant='ghost'
                              size='sm'
                              onClick={() =>
                                setPendingRemove(row.candidate.channel_id)
                              }
                            >
                              {t('Remove')}
                            </Button>
                          ),
                        },
                      ]}
                      data={candidateRows}
                      getRowKey={(row) => String(row.candidate.channel_id)}
                    />
                  )}
                </div>
              </SideDrawerSection>

              <ValidationAlert issues={issues} />
            </form>
          </Form>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              variant='outline'
              onClick={runValidate}
              disabled={isSubmitting}
            >
              {t('Validate')}
            </Button>
            <Button
              variant='outline'
              onClick={() => close(false)}
              disabled={isSubmitting}
            >
              {t('Cancel')}
            </Button>
            <Button onClick={() => save.mutate()} disabled={isSubmitting}>
              {isSubmitting ? t('Saving...') : t('Save')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      {/* Candidate removal confirmation inside the drawer. */}
      <ConfirmDialog
        open={pendingRemove !== null}
        onOpenChange={(open) => {
          if (!open) setPendingRemove(null)
        }}
        title={t('Remove candidate')}
        desc={t('Remove this channel from the candidate list?')}
        confirmText={t('Remove')}
        destructive
        handleConfirm={() => {
          if (pendingRemove !== null) removeCandidate(pendingRemove)
          setPendingRemove(null)
        }}
      />

      <ConfirmDialog
        open={closeConfirm}
        onOpenChange={setCloseConfirm}
        title={t('Discard unsaved changes?')}
        desc={t('Your changes have not been saved.')}
        confirmText={t('Discard changes')}
        destructive
        handleConfirm={() => {
          setCloseConfirm(false)
          props.onOpenChange(false)
        }}
      />
    </>
  )
}

function renderCapabilityBadges(
  info: TargetPolicyChannelInfo | undefined,
  t: (key: string) => string
) {
  if (!info) return '-'
  const entries = [
    { key: 'T', on: info.supports_text !== false, label: t('Text') },
    { key: 'I', on: info.supports_image === true, label: t('Image') },
    { key: 'V', on: info.supports_video === true, label: t('Video') },
    { key: 'U', on: info.unknown_fallback, label: t('Unknown') },
  ]
  return (
    <div className='flex gap-1'>
      {entries.map((entry) => (
        <Badge
          key={entry.key}
          variant={entry.on ? 'secondary' : 'outline'}
          className={entry.on ? '' : 'text-muted-foreground'}
          title={entry.label}
        >
          {entry.key}
        </Badge>
      ))}
    </div>
  )
}
