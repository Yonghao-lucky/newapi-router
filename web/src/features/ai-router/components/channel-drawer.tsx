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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import {
  SideDrawerSection,
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { StatusBadge } from '@/components/status-badge'

import {
  updateAIRouterChannelRouter,
  updateAIRouterChannelScheduling,
} from '../api'
import {
  AI_ROUTER_QUERY_KEYS,
  MAX_CHANNEL_MAX_RETRY,
  MIN_CHANNEL_MAX_RETRY,
  TOOL_SUPPORT_OPTIONS,
} from '../constants'
import type { AIRouterChannel } from '../types'

// Tri-state capability values: 'unset' maps to null (channel default),
// 'yes'/'no' map to explicit true/false overrides. Note the backend treats
// unset text as supported but unset image/video as NOT supported.
const CAPABILITY_UNSET = 'unset'
const CAPABILITY_YES = 'yes'
const CAPABILITY_NO = 'no'

const ROUTER_UNSET = 'unset'
const ROUTER_ON = 'on'
const ROUTER_OFF = 'off'

const TOOL_SUPPORT_LABELS: Record<string, string> = {
  unverified: 'Unverified',
  supported: 'Supported',
  unsupported: 'Unsupported',
}

const capabilityValue = (
  value: boolean | null | undefined
): 'unset' | 'yes' | 'no' => {
  if (value === true) return CAPABILITY_YES
  if (value === false) return CAPABILITY_NO
  return CAPABILITY_UNSET
}

const channelFormSchema = z.object({
  routerEnabled: z.enum([ROUTER_UNSET, ROUTER_ON, ROUTER_OFF]),
  maxRetry: z
    .number()
    .int()
    .min(MIN_CHANNEL_MAX_RETRY)
    .max(MAX_CHANNEL_MAX_RETRY),
  supportsText: z.enum([CAPABILITY_UNSET, CAPABILITY_YES, CAPABILITY_NO]),
  supportsImage: z.enum([CAPABILITY_UNSET, CAPABILITY_YES, CAPABILITY_NO]),
  supportsVideo: z.enum([CAPABILITY_UNSET, CAPABILITY_YES, CAPABILITY_NO]),
  unknownFallback: z.boolean(),
  toolSupport: z.string(),
  group: z.string().min(1),
  priority: z.number().int().min(0),
  weight: z.number().int().min(0),
})

type ChannelFormValues = z.infer<typeof channelFormSchema>

// Mirrors dto.ValidateRouter: unknown_fallback needs effective text+image+
// video. Unset text is effectively on; unset image/video are effectively off.
const hasFullCapability = (values: {
  supportsText: string
  supportsImage: string
  supportsVideo: string
}): boolean =>
  values.supportsText !== CAPABILITY_NO &&
  values.supportsImage === CAPABILITY_YES &&
  values.supportsVideo === CAPABILITY_YES

const routerEnabledValue = (
  channel: AIRouterChannel
): 'unset' | 'on' | 'off' => {
  if (!channel.router.router_set) return ROUTER_UNSET
  return channel.router.enabled ? ROUTER_ON : ROUTER_OFF
}

const toFormValues = (channel: AIRouterChannel): ChannelFormValues => {
  return {
    routerEnabled: routerEnabledValue(channel),
    maxRetry: channel.router.max_retry ?? 0,
    supportsText: capabilityValue(channel.router.supports_text),
    supportsImage: capabilityValue(channel.router.supports_image),
    supportsVideo: capabilityValue(channel.router.supports_video),
    unknownFallback: channel.router.unknown_fallback,
    toolSupport: channel.router.tool_support ?? 'unverified',
    group: channel.group,
    priority: channel.priority,
    weight: channel.weight,
  }
}

// Presentational tri-state select shared by the three capability fields.
function TriStateSelect(props: {
  value: string
  onChange: (value: string) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  return (
    <Select
      value={props.value}
      onValueChange={(value) => {
        if (value !== null) props.onChange(value)
      }}
      disabled={props.disabled}
    >
      <SelectTrigger className='w-full'>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          <SelectItem value={CAPABILITY_UNSET}>{t('Default')}</SelectItem>
          <SelectItem value={CAPABILITY_YES}>{t('Supported')}</SelectItem>
          <SelectItem value={CAPABILITY_NO}>{t('Not supported')}</SelectItem>
        </SelectGroup>
      </SelectContent>
    </Select>
  )
}

// ChannelDrawer edits one channel's Router profile and scheduling fields.
// Only the "router" settings key and group/priority/weight change; models,
// keys and every other channel setting stay untouched.
export function ChannelDrawer(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  channel: AIRouterChannel | null
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const channel = props.channel
  const form = useForm({
    resolver: zodResolver(channelFormSchema),
    defaultValues: channel ? toFormValues(channel) : undefined,
  })
  const values = form.watch()

  useEffect(() => {
    if (props.open && channel) {
      form.reset(toFormValues(channel))
    }
  }, [props.open, channel, form])

  // Client-side mirror of the backend rule: enabling unknown_fallback
  // without full capability would be rejected by the server, so block here.
  const unknownBlocked = values.unknownFallback && !hasFullCapability(values)
  const profileUnset = values.routerEnabled === ROUTER_UNSET

  const save = useMutation({
    meta: { errorToast: false },
    mutationFn: async (formValues: ChannelFormValues) => {
      if (!channel) throw new Error('Channel is required')
      const supports = (value: string): boolean | null => {
        if (value === CAPABILITY_YES) return true
        if (value === CAPABILITY_NO) return false
        return null
      }
      // Unset removes the profile so the channel falls back to defaults.
      await updateAIRouterChannelRouter(
        channel.id,
        profileUnset
          ? null
          : {
              enabled: formValues.routerEnabled === ROUTER_ON,
              router_set: true,
              supports_text: supports(formValues.supportsText),
              supports_image: supports(formValues.supportsImage),
              supports_video: supports(formValues.supportsVideo),
              unknown_fallback: formValues.unknownFallback,
              tool_support: formValues.toolSupport || 'unverified',
              max_retry: formValues.maxRetry,
            }
      )
      await updateAIRouterChannelScheduling(channel.id, {
        group: formValues.group,
        priority: formValues.priority,
        weight: formValues.weight,
      })
    },
    onSuccess: () => {
      toast.success(t('Channel configuration saved'))
      void queryClient.invalidateQueries({
        queryKey: AI_ROUTER_QUERY_KEYS.channels,
      })
      void queryClient.invalidateQueries({
        queryKey: AI_ROUTER_QUERY_KEYS.targets,
      })
      props.onOpenChange(false)
    },
  })
  const isSubmitting = save.isPending

  const close = (open: boolean) => {
    if (!open && isSubmitting) return
    props.onOpenChange(open)
  }

  const handleSave = () => {
    const parsed = channelFormSchema.safeParse(form.getValues())
    if (!parsed.success) {
      toast.error(t('Fix validation errors before saving'))
      return
    }
    if (unknownBlocked) {
      toast.error(
        t(
          'Unknown fallback requires explicit text, image and video capabilities.'
        )
      )
      return
    }
    save.mutate(parsed.data)
  }

  return (
    <Sheet open={props.open} onOpenChange={close}>
      <SheetContent className={sideDrawerContentClassName('sm:max-w-[640px]')}>
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle className='pr-6 break-all'>
              {channel ? `${channel.name} (#${channel.id})` : ''}
            </SheetTitle>
            <SheetDescription>
              {t(
                'Router profile and scheduling for this channel. Other channel settings are edited in Channels.'
              )}
            </SheetDescription>
          </SheetHeader>

          <Form {...form}>
            <form
              onSubmit={(event) => {
                event.preventDefault()
                handleSave()
              }}
              className={sideDrawerFormClassName()}
            >
              <SideDrawerSection>
                <div className='grid gap-4 sm:grid-cols-2'>
                  <FormField
                    control={form.control}
                    name='routerEnabled'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Router status')}</FormLabel>
                        <Select
                          value={field.value}
                          onValueChange={(value) => {
                            if (value !== null) field.onChange(value)
                          }}
                        >
                          <FormControl>
                            <SelectTrigger className='w-full'>
                              <SelectValue />
                            </SelectTrigger>
                          </FormControl>
                          <SelectContent>
                            <SelectGroup>
                              <SelectItem value={ROUTER_UNSET}>
                                {t('Follow global switch')}
                              </SelectItem>
                              <SelectItem value={ROUTER_ON}>
                                {t('Enabled for this channel')}
                              </SelectItem>
                              <SelectItem value={ROUTER_OFF}>
                                {t('Disabled for this channel')}
                              </SelectItem>
                            </SelectGroup>
                          </SelectContent>
                        </Select>
                        <FormDescription>
                          {t(
                            'Unset follows the global Router switch; an explicit value overrides it.'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='maxRetry'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Max retry per channel')}</FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min={MIN_CHANNEL_MAX_RETRY}
                            max={MAX_CHANNEL_MAX_RETRY}
                            className='w-full'
                            disabled={profileUnset}
                            value={field.value}
                            onChange={(event) =>
                              field.onChange(
                                Number.parseInt(event.target.value, 10) || 0
                              )
                            }
                          />
                        </FormControl>
                        <FormDescription>
                          {t(
                            'Extra same-channel retries (0–5) before the channel counts as attempted.'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </div>
              </SideDrawerSection>

              <SideDrawerSection>
                <div className='grid gap-4 sm:grid-cols-3'>
                  <FormField
                    control={form.control}
                    name='supportsText'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Text capability')}</FormLabel>
                        <FormControl>
                          <TriStateSelect
                            value={field.value}
                            onChange={field.onChange}
                            disabled={profileUnset}
                          />
                        </FormControl>
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='supportsImage'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Image capability')}</FormLabel>
                        <FormControl>
                          <TriStateSelect
                            value={field.value}
                            onChange={field.onChange}
                            disabled={profileUnset}
                          />
                        </FormControl>
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='supportsVideo'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Video capability')}</FormLabel>
                        <FormControl>
                          <TriStateSelect
                            value={field.value}
                            onChange={field.onChange}
                            disabled={profileUnset}
                          />
                        </FormControl>
                      </FormItem>
                    )}
                  />
                </div>
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Text defaults to supported; image and video default to not supported.'
                  )}
                </p>
                <FormField
                  control={form.control}
                  name='toolSupport'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Tool support')}</FormLabel>
                      <Select
                        value={
                          field.value === 'supported' ||
                          field.value === 'unsupported' ||
                          field.value === 'unverified'
                            ? field.value
                            : 'unverified'
                        }
                        onValueChange={(value) => {
                          if (value !== null) field.onChange(value)
                        }}
                        disabled={profileUnset}
                      >
                        <FormControl>
                          <SelectTrigger className='w-full'>
                            <SelectValue />
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent>
                          <SelectGroup>
                            {TOOL_SUPPORT_OPTIONS.map((option) => (
                              <SelectItem key={option} value={option}>
                                {t(TOOL_SUPPORT_LABELS[option])}
                              </SelectItem>
                            ))}
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                      <FormDescription>
                        {t(
                          'Whether tool calls survive this channel; unverified learns from traffic.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='unknownFallback'
                  render={({ field }) => (
                    <FormItem className='flex flex-row items-center justify-between gap-3 rounded-lg border p-3'>
                      <div className='space-y-0.5'>
                        <FormLabel>
                          {t('Unknown modality fallback')}
                        </FormLabel>
                        <FormDescription>
                          {t(
                            'Serve unknown modality requests. Requires full capability.'
                          )}
                        </FormDescription>
                        {unknownBlocked && (
                          <p className='text-destructive text-xs'>
                            {t(
                              'Set text, image and video capability to supported first.'
                            )}
                          </p>
                        )}
                      </div>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                          disabled={profileUnset}
                        />
                      </FormControl>
                    </FormItem>
                  )}
                />
              </SideDrawerSection>

              <SideDrawerSection>
                <div className='grid gap-4 sm:grid-cols-3'>
                  <FormField
                    control={form.control}
                    name='group'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Group')}</FormLabel>
                        <FormControl>
                          <Input {...field} />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='priority'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Priority')}</FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min={0}
                            value={field.value}
                            onChange={(event) =>
                              field.onChange(
                                Number.parseInt(event.target.value, 10) || 0
                              )
                            }
                          />
                        </FormControl>
                        <FormDescription>
                          {t('Lower values are tried first.')}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                  <FormField
                    control={form.control}
                    name='weight'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Weight')}</FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min={0}
                            value={field.value}
                            onChange={(event) =>
                              field.onChange(
                                Number.parseInt(event.target.value, 10) || 0
                              )
                            }
                          />
                        </FormControl>
                        <FormDescription>
                          {t('Random share within the same priority.')}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </div>
              </SideDrawerSection>

              {channel && channel.referenced_by.length > 0 && (
                <SideDrawerSection>
                  <div className='space-y-2'>
                    <p className='text-sm font-medium'>
                      {t('Referenced policies')}
                    </p>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Policies listing this channel as a candidate (read-only).'
                      )}
                    </p>
                    <div className='flex flex-wrap gap-2'>
                      {channel.referenced_by.map((ref) => (
                        <Badge
                          key={ref.policy_id}
                          variant='outline'
                          className='gap-1.5'
                        >
                          {ref.model} · {ref.group_id}
                          {ref.enabled ? (
                            <StatusBadge variant='success' type='text'>
                              {t('Enabled')}
                            </StatusBadge>
                          ) : (
                            <StatusBadge variant='neutral' type='text'>
                              {t('Disabled')}
                            </StatusBadge>
                          )}
                        </Badge>
                      ))}
                    </div>
                  </div>
                </SideDrawerSection>
              )}
            </form>
          </Form>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              variant='outline'
              onClick={() => close(false)}
              disabled={isSubmitting}
            >
              {t('Cancel')}
            </Button>
            <Button
              onClick={handleSave}
              disabled={isSubmitting || unknownBlocked}
            >
              {isSubmitting ? t('Saving...') : t('Save')}
            </Button>
          </SheetFooter>
        </SheetContent>
    </Sheet>
  )
}
