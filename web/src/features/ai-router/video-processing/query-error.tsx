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

import { ErrorState } from '@/components/error-state'
import { getServerErrorStatus } from '@/lib/server-error-message'

export function VideoQueryError(props: {
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation()
  const status = getServerErrorStatus(props.error)
  if (status === 400) {
    return (
      <ErrorState
        title={t('Invalid filters')}
        description={t(
          'Check the filter ranges. The maximum time range is 90 days.'
        )}
      />
    )
  }
  if (status === 401 || status === 403) {
    return (
      <ErrorState
        title={t('Administrator access required')}
        description={t(
          'Sign in with an administrator account to manage video processing.'
        )}
      />
    )
  }
  return (
    <ErrorState
      title={t('Video processing is unavailable')}
      description={t('Could not load video processing data. Try again.')}
      onRetry={props.onRetry}
    />
  )
}
