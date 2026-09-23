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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'

import { getAIRouterSettings, updateAIRouterSettings } from './api'
import {
  AI_ROUTER_QUERY_KEYS,
  MAX_TARGET_MAX_ATTEMPTS,
  MIN_TARGET_MAX_ATTEMPTS,
} from './constants'

// GlobalConfigTab edits the shared AI Router options. unknown_policy is
// fixed by V1 and shown read-only.
export function GlobalConfigTab() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [enabled, setEnabled] = useState(false)
  const [telemetryEnabled, setTelemetryEnabled] = useState(false)
  const [toolFilterEnabled, setToolFilterEnabled] = useState(false)
  const [maxAttempts, setMaxAttempts] = useState(3)
  const [retentionDays, setRetentionDays] = useState(7)
  const [idleTimeout, setIdleTimeout] = useState(1000)
  const [responseModelAliases, setResponseModelAliases] = useState<string[]>([])

  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: [...AI_ROUTER_QUERY_KEYS.settings],
    queryFn: getAIRouterSettings,
  })

  useEffect(() => {
    if (!data) return
    setEnabled(data.enabled)
    setTelemetryEnabled(data.telemetry_enabled)
    setToolFilterEnabled(data.tool_filter_enabled)
    setMaxAttempts(data.max_attempts)
    setRetentionDays(data.retention_days)
    setIdleTimeout(data.idle_timeout_seconds ?? 1000)
    setResponseModelAliases(data.response_model_aliases ?? [])
  }, [data])

  const save = useMutation({
    meta: { errorToast: false },
    mutationFn: () =>
      updateAIRouterSettings({
        enabled,
        telemetry_enabled: telemetryEnabled,
        tool_filter_enabled: toolFilterEnabled,
        max_attempts: maxAttempts,
        retention_days: retentionDays,
        idle_timeout_seconds: idleTimeout,
        response_model_aliases: responseModelAliases,
      }),
    onSuccess: () => {
      toast.success(t('Settings saved'))
      void queryClient.invalidateQueries({
        queryKey: AI_ROUTER_QUERY_KEYS.settings,
      })
    },
  })

  if (isError) {
    return (
      <ErrorState description={error?.message} onRetry={() => void refetch()} />
    )
  }

  if (isLoading) {
    return (
      <div className='space-y-3'>
        <Skeleton className='h-16 w-full' />
        <Skeleton className='h-16 w-full' />
        <Skeleton className='h-40 w-full' />
      </div>
    )
  }

  return (
    <div className='max-w-2xl space-y-6'>
      <div className='space-y-3 rounded-lg border p-4'>
        <div className='flex items-center justify-between gap-3'>
          <div className='space-y-0.5'>
            <Label htmlFor='ai-router-enabled'>{t('AI Router')}</Label>
            <p className='text-muted-foreground text-xs'>
              {t('Master switch for AI Router routing.')}
            </p>
          </div>
          <Switch
            id='ai-router-enabled'
            checked={enabled}
            onCheckedChange={setEnabled}
          />
        </div>
        <div className='flex items-center justify-between gap-3 border-t pt-3'>
          <div className='space-y-0.5'>
            <Label htmlFor='ai-router-telemetry'>{t('Telemetry')}</Label>
            <p className='text-muted-foreground text-xs'>
              {t('Record routing decisions to the monitoring logs.')}
            </p>
          </div>
          <Switch
            id='ai-router-telemetry'
            checked={telemetryEnabled}
            onCheckedChange={setTelemetryEnabled}
          />
        </div>
        <div className='flex items-center justify-between gap-3 border-t pt-3'>
          <div className='space-y-0.5'>
            <Label htmlFor='ai-router-tool-filter'>
              {t('Tool filter switch')}
            </Label>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Skip channels whose tool support does not match the request.'
              )}
            </p>
          </div>
          <Switch
            id='ai-router-tool-filter'
            checked={toolFilterEnabled}
            onCheckedChange={setToolFilterEnabled}
          />
        </div>
      </div>

      <div className='space-y-2 rounded-lg border p-4'>
        <Label htmlFor='ai-router-response-model-aliases'>
          {t('Restore response model names')}
        </Label>
        <MultiSelect
          id='ai-router-response-model-aliases'
          aria-label={t('Restore response model names')}
          aria-describedby='ai-router-response-model-aliases-help'
          selected={responseModelAliases}
          onChange={setResponseModelAliases}
          options={[
            ...new Set([
              ...(data?.target_models ?? []),
              ...responseModelAliases,
            ]),
          ].map((value) => ({ value, label: value }))}
          allowCreate
          disabled={save.isPending}
        />
        <p
          id='ai-router-response-model-aliases-help'
          className='text-muted-foreground text-xs'
        >
          {t(
            'Exact client model names, such as kimi-k3. Empty disables rewriting. For AI Router Chat Completions only, restore the response model to the requested name in JSON and every SSE event. Other fields and upstream requests stay unchanged.'
          )}
        </p>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Disabled by default. Empty channel model tags disable restoration. Global settings apply only to channels that explicitly follow them.'
          )}
        </p>
      </div>

      <div className='grid gap-4 sm:grid-cols-2'>
        <div className='space-y-1.5'>
          <Label htmlFor='ai-router-idle-timeout'>
            {t('No-progress timeout (seconds)')}
          </Label>
          <Input
            id='ai-router-idle-timeout'
            type='number'
            min={1}
            max={86400}
            step={1}
            value={idleTimeout}
            onChange={(event) => setIdleTimeout(Number(event.target.value))}
          />
          <p className='text-muted-foreground text-xs'>
            {t(
              'Retries share this budget. Generation progress resets it; heartbeats do not.'
            )}
          </p>
        </div>
        <div className='space-y-1.5'>
          <Label htmlFor='ai-router-max-attempts'>
            {t('Default max attempts')}
          </Label>
          <Input
            id='ai-router-max-attempts'
            type='number'
            min={MIN_TARGET_MAX_ATTEMPTS}
            max={MAX_TARGET_MAX_ATTEMPTS}
            value={maxAttempts}
            onChange={(event) =>
              setMaxAttempts(
                Number.parseInt(event.target.value, 10) ||
                  MIN_TARGET_MAX_ATTEMPTS
              )
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('Distinct channels tried per request (1–10).')}
          </p>
        </div>
        <div className='space-y-1.5'>
          <Label htmlFor='ai-router-retention'>{t('Log retention days')}</Label>
          <Input
            id='ai-router-retention'
            type='number'
            min={1}
            max={30}
            value={retentionDays}
            onChange={(event) =>
              setRetentionDays(Number.parseInt(event.target.value, 10) || 1)
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t('Monitoring logs older than this are cleaned up (1–30).')}
          </p>
        </div>
      </div>

      <div className='rounded-lg border border-dashed p-4'>
        <p className='text-sm font-medium'>
          {t('Unknown modality policy (fixed)')}
        </p>
        <p className='text-muted-foreground mt-1 text-xs'>
          {t(
            'Requests with unknown modality types only fall back to channels whose full capability is verified. This policy is not configurable in V1.5.'
          )}
        </p>
      </div>

      <div className='flex justify-end gap-2'>
        <Button
          variant='outline'
          onClick={() => void refetch()}
          disabled={save.isPending}
        >
          {t('Refresh')}
        </Button>
        <Button
          onClick={() => save.mutate()}
          disabled={
            save.isPending ||
            !Number.isInteger(idleTimeout) ||
            idleTimeout < 1 ||
            idleTimeout > 86400
          }
        >
          {save.isPending ? t('Saving...') : t('Save')}
        </Button>
      </div>
    </div>
  )
}
