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
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import {
  handleServerError,
  markServerErrorHandled,
} from '@/lib/handle-server-error'
import {
  getServerErrorSources,
  getServerErrorStatus,
} from '@/lib/server-error-message'

import {
  getVideoSettings,
  saveVideoSettings,
  validateVideoSettings,
  videoKeys,
} from './api'
import { numericFields, validationLabels } from './constants'
import { needsVideoConfirmation, videoSettingsSchema } from './lib/settings'
import type {
  VideoSettings,
  VideoSettingsDocument,
  VideoValidationIssue,
} from './types'

const issueSchema = z.array(
  z.object({
    level: z.enum(['error', 'warning']),
    code: z.string(),
    field: z.string().optional(),
    message: z.string(),
  })
)

export function useVideoSettings(document: VideoSettingsDocument) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [baseline, setBaseline] = useState(document)
  const [issues, setIssues] = useState<VideoValidationIssue[]>([])
  const [conflict, setConflict] = useState(false)
  const [pending, setPending] = useState<VideoSettings | null>(null)
  const [resetAction, setResetAction] = useState<'defaults' | 'reload' | null>(
    null
  )
  const form = useForm<VideoSettings>({
    resolver: zodResolver(videoSettingsSchema),
    defaultValues: document.settings,
  })

  const applyIssues = (findings: VideoValidationIssue[]) => {
    const localized = findings.map((issue) => {
      const definition = numericFields.find(
        (field) => field.key === issue.field
      )
      let message = validationLabels[issue.code] ?? issue.message
      if (issue.code === 'value_out_of_range' && definition) {
        message = 'Value is outside the allowed range'
      }

      return { ...issue, message }
    })
    setIssues(localized)
    for (const issue of localized) {
      if (
        issue.level === 'error' &&
        issue.field &&
        issue.field in baseline.settings
      ) {
        form.setError(issue.field as keyof VideoSettings, {
          type: 'server',
          message: issue.message,
        })
      }
    }
  }
  const onError = (error: unknown) => {
    if (getServerErrorStatus(error) === 409) {
      setConflict(true)
      setPending(null)
      markServerErrorHandled(error)
      return
    }
    for (const source of getServerErrorSources(error)) {
      const parsed = issueSchema.safeParse(source.issues)
      if (parsed.success) {
        applyIssues(parsed.data)
        markServerErrorHandled(error)
        return
      }
    }
    handleServerError(error, t('Could not save video settings'))
  }
  const saved = (next: VideoSettingsDocument) => {
    setBaseline(next)
    form.reset(next.settings)
    setConflict(false)
    setIssues([])
    setPending(null)
    client.setQueryData(videoKeys.settings, next)
    void client.invalidateQueries({ queryKey: videoKeys.runtime })
  }
  const save = useMutation({
    mutationFn: saveVideoSettings,
    meta: { errorToast: false },
    onSuccess: (next) => {
      saved(next)
      toast.success(t('Settings saved'))
    },
    onError,
  })
  const validate = useMutation({
    mutationFn: validateVideoSettings,
    meta: { errorToast: false },
    onError,
  })
  const reload = useMutation({
    mutationFn: getVideoSettings,
    meta: { errorToast: false },
    onSuccess: saved,
    onError,
  })
  const submit = async (candidate: VideoSettings, validateOnly = false) => {
    form.clearErrors()
    setIssues([])
    const snapshot = structuredClone(candidate)
    try {
      const result = await validate.mutateAsync(snapshot)
      applyIssues(result.issues ?? [])
      if (result.blocking) return
      if (validateOnly) {
        toast.success(t('Configuration is valid'))
        return
      }
      if (
        needsVideoConfirmation(baseline.settings, snapshot) ||
        result.issues?.some((issue) => issue.level === 'warning')
      ) {
        setPending(snapshot)
        return
      }
      save.mutate({ settings: snapshot, expected_version: baseline.version })
    } catch {
      /* Mutation onError owns error presentation. */
    }
  }
  const reset = () => {
    if (resetAction === 'defaults') {
      form.reset(baseline.defaults, { keepDefaultValues: true })
      form.clearErrors()
      setIssues([])
    } else if (resetAction === 'reload') {
      reload.mutate()
    }
    setResetAction(null)
  }
  return {
    form,
    baseline,
    issues,
    conflict,
    pending,
    setPending,
    resetAction,
    setResetAction,
    reset,
    submit,
    save,
    reload,
    busy: save.isPending || validate.isPending || reload.isPending,
  }
}
