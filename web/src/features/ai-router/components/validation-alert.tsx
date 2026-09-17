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
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import type { AIRouterValidationIssue } from '../types'

// ValidationAlert renders server-side validate findings. Errors block
// saving; warnings only inform and can be saved through.
export function ValidationAlert(props: { issues: AIRouterValidationIssue[] }) {
  const { t } = useTranslation()
  if (props.issues.length === 0) {
    return null
  }
  const errors = props.issues.filter((issue) => issue.level === 'error')
  const warnings = props.issues.filter((issue) => issue.level !== 'error')
  return (
    <div className='space-y-2'>
      {errors.length > 0 && (
        <Alert variant='destructive'>
          <AlertTitle>{t('Validation errors')}</AlertTitle>
          <AlertDescription>
            <ul className='list-disc pl-4'>
              {errors.map((issue) => (
                <li key={issue.code}>{issue.message}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
      {warnings.length > 0 && (
        <Alert>
          <AlertTitle>{t('Validation warnings')}</AlertTitle>
          <AlertDescription>
            <ul className='list-disc pl-4'>
              {warnings.map((issue) => (
                <li key={issue.code}>{issue.message}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}
