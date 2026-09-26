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
import { useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { FieldGroup, FieldLegend, FieldSet } from '@/components/ui/field'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'

import type { ChannelFormValues } from '../lib/channel-form'

export function K3ReasoningCompatibilityControl(props: {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  return (
    <FormItem
      data-disabled={props.disabled}
      className='flex items-center justify-between gap-4'
    >
      <div className='min-w-0'>
        <FormLabel>{t('Kimi K3 reasoning effort compatibility')}</FormLabel>
        <FormDescription>
          {t(
            'Before K3 validation, replace unsupported reasoning_effort strings (including none), empty strings and null with high. Keep low/high/max and omitted fields unchanged; reject other types. May change reasoning time and token usage. Other validation stays enabled.'
          )}
        </FormDescription>
      </div>
      <FormControl>
        <Switch
          disabled={props.disabled}
          checked={props.checked}
          onCheckedChange={props.onCheckedChange}
        />
      </FormControl>
    </FormItem>
  )
}

export function K3ToolChoiceCompatibilityControl(props: {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  return (
    <FormItem
      data-disabled={props.disabled}
      className='flex items-center justify-between gap-4'
    >
      <div className='min-w-0'>
        <FormLabel>{t('Kimi K3 tool choice compatibility')}</FormLabel>
        <FormDescription>
          {t(
            'Remove empty tool_choice and redundant auto/none without tools. Normalize supported selector aliases; unsupported strings use auto when tools exist. Required or named tool calls still need matching tools. May change tool selection. Does not fix every upstream tool error.'
          )}
        </FormDescription>
      </div>
      <FormControl>
        <Switch
          disabled={props.disabled}
          checked={props.checked}
          onCheckedChange={props.onCheckedChange}
        />
      </FormControl>
    </FormItem>
  )
}

export function ChatCompatibilitySettings(props: { disabled?: boolean }) {
  const { t } = useTranslation()
  const form = useFormContext<ChannelFormValues>()

  return (
    <FieldSet disabled={props.disabled}>
      <FieldLegend variant='label'>
        {t('Chat Completions compatibility')}
      </FieldLegend>
      <FieldGroup>
        <FormField
          control={form.control}
          name='chat_compatibility_models'
          render={({ field }) => (
            <FormItem data-disabled={props.disabled}>
              <FormLabel>{t('Exact upstream models')}</FormLabel>
              <FormControl>
                <Input {...field} disabled={props.disabled} />
              </FormControl>
              <FormDescription>
                {t(
                  'Comma-separated outgoing model names after mapping. These options apply only to Chat Completions on this channel. An empty list disables them.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='strict_chat_tool_messages'
          render={({ field }) => (
            <FormItem
              data-disabled={props.disabled}
              className='flex items-center justify-between gap-4'
            >
              <div>
                <FormLabel>{t('Strict tool message validation')}</FormLabel>
                <FormDescription>
                  {t(
                    'Reject incomplete or mismatched tool results with HTTP 400 without retrying another channel.'
                  )}
                </FormDescription>
              </div>
              <FormControl>
                <Switch
                  disabled={props.disabled}
                  checked={field.value === true}
                  onCheckedChange={field.onChange}
                />
              </FormControl>
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='k3_reasoning_compatibility'
          render={({ field }) => (
            <K3ReasoningCompatibilityControl
              disabled={props.disabled}
              checked={field.value === true}
              onCheckedChange={field.onChange}
            />
          )}
        />
        <FormField
          control={form.control}
          name='k3_tool_choice_compatibility'
          render={({ field }) => (
            <K3ToolChoiceCompatibilityControl
              disabled={props.disabled}
              checked={field.value === true}
              onCheckedChange={field.onChange}
            />
          )}
        />
        <FormField
          control={form.control}
          name='k3_parameter_validation'
          render={({ field }) => (
            <FormItem
              data-disabled={props.disabled}
              className='flex items-center justify-between gap-4'
            >
              <div>
                <FormLabel>{t('Kimi K3 parameter validation')}</FormLabel>
                <FormDescription>
                  {t(
                    'Reject values outside the Kimi K3 parameter contract with HTTP 400. Omitted parameters stay omitted.'
                  )}
                </FormDescription>
              </div>
              <FormControl>
                <Switch
                  disabled={props.disabled}
                  checked={field.value === true}
                  onCheckedChange={field.onChange}
                />
              </FormControl>
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='gateway_visible_content_stop'
          render={({ field }) => (
            <FormItem
              data-disabled={props.disabled}
              className='flex items-center justify-between gap-4'
            >
              <div>
                <FormLabel>{t('Gateway visible-content stop')}</FormLabel>
                <FormDescription>
                  {t(
                    'Apply stop sequences to visible text only. Continue receiving upstream output for real usage; generated tokens remain billable. Does not guarantee a nonempty answer.'
                  )}
                </FormDescription>
              </div>
              <FormControl>
                <Switch
                  disabled={props.disabled}
                  checked={field.value === true}
                  onCheckedChange={field.onChange}
                />
              </FormControl>
            </FormItem>
          )}
        />
      </FieldGroup>
    </FieldSet>
  )
}
