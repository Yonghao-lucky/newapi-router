import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import {
  getAIRouterLogs,
  getAIRouterSettings,
  getAIRouterStats,
  updateAIRouterSettings,
} from './api'
import type { AIRouterLog, AIRouterSettings, AIRouterStats } from './types'

function successParam(value: string): 'true' | 'false' | undefined {
  if (value === 'true') {
    return 'true'
  }
  if (value === 'false') {
    return 'false'
  }
  return undefined
}

export function AIRouterPage() {
  const { t } = useTranslation()
  const [settings, setSettings] = useState<AIRouterSettings | null>(null)
  const [stats, setStats] = useState<AIRouterStats | null>(null)
  const [logs, setLogs] = useState<AIRouterLog[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [pageSize] = useState(20)
  const [saving, setSaving] = useState(false)
  const [filters, setFilters] = useState({
    startTime: '',
    endTime: '',
    model: '',
    userId: '',
    groupId: '',
    success: '',
    modality: '',
  })

  const endTime = Math.floor(Date.now() / 1000)
  const startTime = endTime - 86400

  const loadSettings = useCallback(async () => {
    try {
      const data = await getAIRouterSettings()
      setSettings(data)
    } catch {
      // handled by API layer
    }
  }, [])

  const loadStats = useCallback(async () => {
    try {
      const data = await getAIRouterStats({ start_time: startTime, end_time: endTime })
      setStats(data)
    } catch {
      // handled by API layer
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const loadLogs = useCallback(
    async (p: number, f?: typeof filters) => {
      const ff = f ?? filters
      try {
        const data = await getAIRouterLogs({
          page: p,
          page_size: pageSize,
          start_time: ff.startTime ? Number(ff.startTime) : undefined,
          end_time: ff.endTime ? Number(ff.endTime) : undefined,
          model: ff.model || undefined,
          user_id: ff.userId ? parseInt(ff.userId, 10) || undefined : undefined,
          group_id: ff.groupId || undefined,
          success: successParam(ff.success),
          modality: ff.modality || undefined,
        })
        setLogs(data.items ?? [])
        setTotal(data.total ?? 0)
        setPage(p)
      } catch {
        // handled by API layer
      }
    },
    [pageSize, filters]
  )

  useEffect(() => {
    loadSettings()
    loadStats()
    loadLogs(1)
  }, [loadSettings, loadStats, loadLogs])

  const saveSettings = async () => {
    if (!settings) return
    setSaving(true)
    try {
      const updated = await updateAIRouterSettings({
        enabled: settings.enabled,
        max_attempts: settings.max_attempts,
        telemetry_enabled: settings.telemetry_enabled,
        target_models: settings.target_models,
      })
      setSettings(updated)
    } catch {
      // handled by API layer
    } finally {
      setSaving(false)
    }
  }

  const handleQuery = () => loadLogs(1, filters)
  const handleReset = () => {
    const cleared = {
      startTime: '',
      endTime: '',
      model: '',
      userId: '',
      groupId: '',
      success: '',
      modality: '',
    }
    setFilters(cleared)
    loadLogs(1, cleared)
  }

  if (!settings) {
    return <div className='p-6'>{t('Loading...')}</div>
  }

  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  return (
    <div className='container mx-auto space-y-6 p-6'>
      <h1 className='text-2xl font-bold'>{t('AI Router')}</h1>

      <Tabs defaultValue='config'>
        <TabsList>
          <TabsTrigger value='config'>{t('Configuration')}</TabsTrigger>
          <TabsTrigger value='monitor'>{t('Monitoring')}</TabsTrigger>
        </TabsList>

        <TabsContent value='config' className='space-y-6'>
          <Card>
            <CardHeader>
              <CardTitle>{t('Global Settings')}</CardTitle>
            </CardHeader>
            <CardContent className='space-y-4'>
              <div className='flex items-center gap-4'>
                <Label htmlFor='router-enabled'>{t('Router Enabled')}</Label>
                <Switch
                  id='router-enabled'
                  checked={settings.enabled}
                  onCheckedChange={(v) => setSettings({ ...settings, enabled: v })}
                />
              </div>
              <div className='flex items-center gap-4'>
                <Label htmlFor='max-attempts'>{t('Max Attempts')} (1-10)</Label>
                <Input
                  id='max-attempts'
                  type='number'
                  min={1}
                  max={10}
                  className='w-24'
                  value={settings.max_attempts}
                  onChange={(e) =>
                    setSettings({ ...settings, max_attempts: parseInt(e.target.value, 10) || 3 })
                  }
                />
              </div>
              <div className='flex items-center gap-4'>
                <Label htmlFor='telemetry'>{t('Telemetry Enabled')}</Label>
                <Switch
                  id='telemetry'
                  checked={settings.telemetry_enabled}
                  onCheckedChange={(v) => setSettings({ ...settings, telemetry_enabled: v })}
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='target-models'>{t('Target Models')} ({t('comma separated')})</Label>
                <Input
                  id='target-models'
                  value={settings.target_models.join(', ')}
                  onChange={(e) =>
                    setSettings({
                      ...settings,
                      target_models: e.target.value
                        .split(',')
                        .map((m) => m.trim())
                        .filter(Boolean),
                    })
                  }
                />
              </div>
              <Button onClick={saveSettings} disabled={saving}>
                {saving ? t('Saving...') : t('Save Settings')}
              </Button>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value='monitor' className='space-y-6'>
          <div className='grid grid-cols-2 gap-4 md:grid-cols-3 lg:grid-cols-6'>
            <Card>
              <CardHeader className='pb-2'>
                <CardTitle className='text-sm font-medium'>{t('Total Requests')}</CardTitle>
              </CardHeader>
              <CardContent>
                <div className='text-2xl font-bold'>{stats?.total_requests ?? 0}</div>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className='pb-2'>
                <CardTitle className='text-sm font-medium'>{t('Success Rate')}</CardTitle>
              </CardHeader>
              <CardContent>
                <div className='text-2xl font-bold text-green-600'>
                  {stats ? `${(stats.success_rate * 100).toFixed(1)}%` : '-'}
                </div>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className='pb-2'>
                <CardTitle className='text-sm font-medium'>{t('Avg Duration')}</CardTitle>
              </CardHeader>
              <CardContent>
                <div className='text-2xl font-bold'>
                  {stats ? `${Math.round(stats.avg_duration_ms)} ms` : '-'}
                </div>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className='pb-2'>
                <CardTitle className='text-sm font-medium'>{t('Avg TTFT')}</CardTitle>
              </CardHeader>
              <CardContent>
                <div className='text-2xl font-bold'>
                  {stats && stats.avg_ttft_ms > 0
                    ? `${Math.round(stats.avg_ttft_ms)} ms`
                    : '-'}
                </div>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className='pb-2'>
                <CardTitle className='text-sm font-medium'>{t('Avg Attempts')}</CardTitle>
              </CardHeader>
              <CardContent>
                <div className='text-2xl font-bold'>
                  {stats ? stats.avg_attempts.toFixed(1) : '-'}
                </div>
              </CardContent>
            </Card>
          </div>

          {stats?.by_modality && Object.keys(stats.by_modality).length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle className='text-sm font-medium'>{t('By Modality')}</CardTitle>
              </CardHeader>
              <CardContent className='space-y-2'>
                {Object.entries(stats.by_modality).map(([modality, count]) => {
                  const pct =
                    stats.total_requests > 0
                      ? ((count / stats.total_requests) * 100).toFixed(1)
                      : '0.0'
                  return (
                    <div key={modality} className='flex items-center gap-3'>
                      <Badge variant='outline' className='w-16 justify-center'>
                        {modality}
                      </Badge>
                      <div className='h-2 flex-1 overflow-hidden rounded-full bg-muted'>
                        <div
                          className='h-full rounded-full bg-primary'
                          style={{ width: `${pct}%` }}
                        />
                      </div>
                      <span className='w-12 text-right text-sm tabular-nums'>{pct}%</span>
                    </div>
                  )
                })}
              </CardContent>
            </Card>
          )}

          <Card>
            <CardHeader>
              <CardTitle className='text-sm font-medium'>{t('Router Logs')}</CardTitle>
            </CardHeader>
            <CardContent>
              <div className='mb-4 grid grid-cols-2 gap-3 md:grid-cols-4'>
                <div className='space-y-1'>
                  <Label htmlFor='filter-start' className='text-xs'>
                    {t('Start Time')} (unix)
                  </Label>
                  <Input
                    id='filter-start'
                    placeholder='e.g. 1705276800'
                    className='text-xs'
                    value={filters.startTime}
                    onChange={(e) =>
                      setFilters({ ...filters, startTime: e.target.value })
                    }
                  />
                </div>
                <div className='space-y-1'>
                  <Label htmlFor='filter-end' className='text-xs'>
                    {t('End Time')} (unix)
                  </Label>
                  <Input
                    id='filter-end'
                    placeholder='e.g. 1705363199'
                    className='text-xs'
                    value={filters.endTime}
                    onChange={(e) =>
                      setFilters({ ...filters, endTime: e.target.value })
                    }
                  />
                </div>
                <div className='space-y-1'>
                  <Label htmlFor='filter-model' className='text-xs'>
                    {t('Model')}
                  </Label>
                  <Input
                    id='filter-model'
                    placeholder='kimi-k3'
                    className='text-xs'
                    value={filters.model}
                    onChange={(e) =>
                      setFilters({ ...filters, model: e.target.value })
                    }
                  />
                </div>
                <div className='space-y-1'>
                  <Label htmlFor='filter-user' className='text-xs'>
                    {t('User ID')}
                  </Label>
                  <Input
                    id='filter-user'
                    placeholder='1'
                    className='text-xs'
                    value={filters.userId}
                    onChange={(e) =>
                      setFilters({ ...filters, userId: e.target.value })
                    }
                  />
                </div>
                <div className='space-y-1'>
                  <Label htmlFor='filter-group' className='text-xs'>
                    {t('Group')}
                  </Label>
                  <Input
                    id='filter-group'
                    placeholder='default'
                    className='text-xs'
                    value={filters.groupId}
                    onChange={(e) =>
                      setFilters({ ...filters, groupId: e.target.value })
                    }
                  />
                </div>
                <div className='space-y-1'>
                  <Label htmlFor='filter-success' className='text-xs'>
                    {t('Status')}
                  </Label>
                  <NativeSelect
                    id='filter-success'
                    className='text-xs'
                    value={filters.success}
                    onChange={(e) =>
                      setFilters({ ...filters, success: e.target.value })
                    }
                  >
                    <option value=''>{t('All')}</option>
                    <option value='true'>{t('Success')}</option>
                    <option value='false'>{t('Failed')}</option>
                  </NativeSelect>
                </div>
                <div className='space-y-1'>
                  <Label htmlFor='filter-modality' className='text-xs'>
                    {t('Modality')}
                  </Label>
                  <NativeSelect
                    id='filter-modality'
                    className='text-xs'
                    value={filters.modality}
                    onChange={(e) =>
                      setFilters({ ...filters, modality: e.target.value })
                    }
                  >
                    <option value=''>{t('All')}</option>
                    <option value='text'>{t('text')}</option>
                    <option value='image'>{t('image')}</option>
                    <option value='video'>{t('video')}</option>
                    <option value='unknown'>{t('unknown')}</option>
                  </NativeSelect>
                </div>
                <div className='flex items-end gap-2'>
                  <Button size='sm' onClick={handleQuery}>
                    {t('Query')}
                  </Button>
                  <Button size='sm' variant='outline' onClick={handleReset}>
                    {t('Reset')}
                  </Button>
                </div>
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('Time')}</TableHead>
                    <TableHead>{t('Request ID')}</TableHead>
                    <TableHead>{t('Model')}</TableHead>
                    <TableHead>{t('Modality')}</TableHead>
                    <TableHead>{t('Attempts')}</TableHead>
                    <TableHead>{t('TTFT')}</TableHead>
                    <TableHead>{t('Duration')}</TableHead>
                    <TableHead>{t('Status')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {logs.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={8} className='text-center text-muted-foreground'>
                        {t('No logs found')}
                      </TableCell>
                    </TableRow>
                  ) : (
                    logs.map((log) => (
                      <TableRow key={log.id}>
                        <TableCell className='text-xs tabular-nums'>
                          {new Date(log.created_at * 1000).toLocaleTimeString()}
                        </TableCell>
                        <TableCell className='text-xs font-mono'>{log.request_id}</TableCell>
                        <TableCell>{log.model}</TableCell>
                        <TableCell>
                          <Badge variant='outline'>{log.modality}</Badge>
                        </TableCell>
                        <TableCell className='tabular-nums'>
                          {log.attempts}/{log.max_attempts}
                        </TableCell>
                        <TableCell className='tabular-nums'>
                          {log.ttft_ms > 0 ? `${log.ttft_ms} ms` : '-'}
                        </TableCell>
                        <TableCell className='tabular-nums'>
                          {log.total_duration_ms} ms
                        </TableCell>
                        <TableCell>
                          {log.success ? (
                            <Badge variant='default'>{t('Success')}</Badge>
                          ) : (
                            <Badge variant='destructive'>{t('Failed')}</Badge>
                          )}
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
              {totalPages > 1 && (
                <div className='mt-4 flex items-center justify-end gap-2'>
                  <Button
                    variant='outline'
                    size='sm'
                    disabled={page <= 1}
                    onClick={() => loadLogs(page - 1)}
                  >
                    {t('Previous')}
                  </Button>
                  <span className='text-sm'>
                    {page} / {totalPages}
                  </span>
                  <Button
                    variant='outline'
                    size='sm'
                    disabled={page >= totalPages}
                    onClick={() => loadLogs(page + 1)}
                  >
                    {t('Next')}
                  </Button>
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  )
}
