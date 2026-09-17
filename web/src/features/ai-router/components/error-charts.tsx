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
import { VChart } from '@visactor/react-vchart'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Card } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useChartTheme } from '@/lib/use-chart-theme'
import { VCHART_OPTION } from '@/lib/vchart'

import type {
  AIRouterErrorCategoryStat,
  AIRouterStatusCodeStat,
} from '../types'

// Error-category labels: most categories already read as themselves
// (429 / 5xx / timeout / connection / other); only the stream-interruption
// enum needs a readable form.
const ERROR_CATEGORY_LABEL_KEYS: Record<string, string> = {
  stream_interrupted: 'Stream interrupted',
}
const errorCategoryLabel = (category: string): string =>
  ERROR_CATEGORY_LABEL_KEYS[category] ?? category

// No failures in range is a normal outcome, not a broken chart.
function EmptyChartHint() {
  const { t } = useTranslation()
  return (
    <p className='text-muted-foreground flex h-full items-center justify-center rounded-lg border border-dashed p-4 text-center text-sm'>
      {t('No errors in the selected range.')}
    </p>
  )
}

interface ErrorChartsProps {
  byCategory: AIRouterErrorCategoryStat[]
  byStatusCode: AIRouterStatusCodeStat[]
}

// ErrorCharts renders the two error-analysis bar charts of the monitoring
// tab: failed attempts by error category and by upstream HTTP status. Both
// share the toolbar's time/model/group filter through the caller's query.
export function ErrorCharts({ byCategory, byStatusCode }: ErrorChartsProps) {
  const { t } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()
  const chartTheme = resolvedTheme === 'dark' ? 'dark' : 'light'

  const categoryData = useMemo(
    () =>
      byCategory.map((row) => ({
        category: t(errorCategoryLabel(row.error_category)),
        count: row.count,
      })),
    [byCategory, t]
  )

  const statusData = useMemo(
    () =>
      byStatusCode.map((row) => ({
        status: String(row.http_status_code),
        count: row.count,
      })),
    [byStatusCode]
  )

  // Single-series bars: the category/status is the x axis, so no legend.
  const barSpec = (id: string, xField: string, values: object[]) => ({
    type: 'bar' as const,
    data: [{ id, values }],
    xField,
    yField: 'count',
    bar: { style: { cornerRadius: [4, 4, 0, 0] } },
    axes: [
      { orient: 'bottom' as const, type: 'band' as const },
      { orient: 'left' as const, type: 'linear' as const },
    ],
    background: 'transparent',
  })

  const renderChart = (isEmpty: boolean, spec: object) => {
    if (isEmpty) return <EmptyChartHint />
    if (!themeReady) return <Skeleton className='h-full w-full' />
    return <VChart spec={spec} option={VCHART_OPTION} theme={chartTheme} />
  }

  return (
    <div className='grid gap-4 lg:grid-cols-2'>
      <Card className='p-4'>
        <p className='mb-3 text-sm font-medium'>
          {t('Error category distribution')}
        </p>
        <div className='h-64'>
          {renderChart(
            categoryData.length === 0,
            barSpec('category', 'category', categoryData)
          )}
        </div>
      </Card>
      <Card className='p-4'>
        <p className='mb-3 text-sm font-medium'>
          {t('HTTP status code distribution')}
        </p>
        <div className='h-64'>
          {renderChart(
            statusData.length === 0,
            barSpec('status', 'status', statusData)
          )}
        </div>
      </Card>
    </div>
  )
}
