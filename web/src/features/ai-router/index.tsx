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
import { lazy, Suspense, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { ChannelConfigTab } from './channel-config'
import { LogsTable } from './components/logs-table'
import { TargetsTable } from './components/targets-table'
import { GlobalConfigTab } from './global-config'

const MonitoringTab = lazy(() =>
  import('./monitoring').then((module) => ({ default: module.MonitoringTab }))
)

const VideoProcessingTab = lazy(() =>
  import('./video-processing').then((module) => ({
    default: module.VideoProcessingTab,
  }))
)

// AIRouterPage is the single admin entry: global config, target policies,
// channel routing config, video processing and monitoring live in five tabs.
export function AIRouterPage() {
  const { t } = useTranslation()
  const [tab, setTab] = useState('targets')
  const [videoDirty, setVideoDirty] = useState(false)
  const [pendingTab, setPendingTab] = useState<string | null>(null)
  // Channel-performance row click: filter the request logs by that channel
  // and scroll them into view. The nonce makes a repeat click on the same
  // channel re-apply the filter.
  const [channelJump, setChannelJump] = useState<{
    channelId: string
    nonce: number
  } | null>(null)
  const logsRef = useRef<HTMLDivElement>(null)

  const jumpToLogsForChannel = (channelId: number) => {
    setChannelJump((prev) => ({
      channelId: String(channelId),
      nonce: (prev?.nonce ?? 0) + 1,
    }))
    logsRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('AI Router')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <Tabs
          value={tab}
          onValueChange={(value) => {
            if (tab === 'video' && videoDirty && value !== 'video') {
              setPendingTab(value)
              return
            }
            setTab(value)
          }}
          className='space-y-4'
        >
          <TabsList
            className='h-auto max-w-full flex-wrap justify-start'
            aria-label={t('AI Router sections')}
          >
            <TabsTrigger value='targets'>{t('Target Models')}</TabsTrigger>
            <TabsTrigger value='channels'>{t('Channel Config')}</TabsTrigger>
            <TabsTrigger value='monitoring'>{t('Monitoring')}</TabsTrigger>
            <TabsTrigger value='video'>{t('Video Processing')}</TabsTrigger>
            <TabsTrigger value='settings'>{t('Global Settings')}</TabsTrigger>
          </TabsList>
          <TabsContent value='targets'>
            <TargetsTable />
          </TabsContent>
          <TabsContent value='channels'>
            <ChannelConfigTab />
          </TabsContent>
          <TabsContent value='monitoring'>
            <Suspense fallback={<LoadingState />}>
              <MonitoringTab onChannelSelect={jumpToLogsForChannel} />
            </Suspense>
            <div className='mt-6' ref={logsRef}>
              <LogsTable channelJump={channelJump} />
            </div>
          </TabsContent>
          <TabsContent value='video'>
            <Suspense fallback={<LoadingState />}>
              <VideoProcessingTab onDirtyChange={setVideoDirty} />
            </Suspense>
          </TabsContent>
          <TabsContent value='settings'>
            <GlobalConfigTab />
          </TabsContent>
        </Tabs>
        <ConfirmDialog
          open={pendingTab !== null}
          onOpenChange={(open) => {
            if (!open) setPendingTab(null)
          }}
          title={t('Discard unsaved changes?')}
          desc={t('Your unsaved video settings will be discarded.')}
          handleConfirm={() => {
            if (pendingTab) setTab(pendingTab)
            setPendingTab(null)
            setVideoDirty(false)
          }}
        />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
