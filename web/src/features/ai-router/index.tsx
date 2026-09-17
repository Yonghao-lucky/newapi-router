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
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { SectionPageLayout } from '@/components/layout'

import { ChannelConfigTab } from './channel-config'
import { GlobalConfigTab } from './global-config'
import { MonitoringTab } from './monitoring'
import { LogsTable } from './components/logs-table'
import { TargetsTable } from './components/targets-table'

// AIRouterPage is the single admin entry: global config, target policies,
// channel routing config and monitoring live in four tabs.
export function AIRouterPage() {
  const { t } = useTranslation()
  const [tab, setTab] = useState('targets')
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
        <Tabs value={tab} onValueChange={setTab} className='space-y-4'>
          <TabsList>
            <TabsTrigger value='targets'>{t('Target Models')}</TabsTrigger>
            <TabsTrigger value='channels'>{t('Channel Config')}</TabsTrigger>
            <TabsTrigger value='monitoring'>{t('Monitoring')}</TabsTrigger>
            <TabsTrigger value='settings'>{t('Global Settings')}</TabsTrigger>
          </TabsList>
          <TabsContent value='targets'>
            <TargetsTable />
          </TabsContent>
          <TabsContent value='channels'>
            <ChannelConfigTab />
          </TabsContent>
          <TabsContent value='monitoring'>
            <MonitoringTab onChannelSelect={jumpToLogsForChannel} />
            <div className='mt-6' ref={logsRef}>
              <LogsTable channelJump={channelJump} />
            </div>
          </TabsContent>
          <TabsContent value='settings'>
            <GlobalConfigTab />
          </TabsContent>
        </Tabs>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
