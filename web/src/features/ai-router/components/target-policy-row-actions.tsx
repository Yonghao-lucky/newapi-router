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
import { Copy, MoreHorizontal, Pencil, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

import type { AIRouterTargetPolicy } from '../types'

export function TargetPolicyRowActions(props: {
  policy: AIRouterTargetPolicy
  onEdit: (policy: AIRouterTargetPolicy) => void
  onCopy: (policy: AIRouterTargetPolicy) => void
  onDelete: (policy: AIRouterTargetPolicy) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger
        render={<Button variant='ghost' size='sm' className='h-8 w-8 p-0' />}
      >
        <MoreHorizontal className='h-4 w-4' />
        <span className='sr-only'>{t('Open menu')}</span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align='end'>
        <DropdownMenuItem onClick={() => props.onEdit(props.policy)}>
          <Pencil className='mr-2 h-4 w-4' />
          {t('Edit')}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => props.onCopy(props.policy)}>
          <Copy className='mr-2 h-4 w-4' />
          {t('Copy')}
        </DropdownMenuItem>
        <DropdownMenuItem
          onClick={() => props.onDelete(props.policy)}
          className='text-destructive focus:text-destructive'
        >
          <Trash2 className='mr-2 h-4 w-4' />
          {t('Delete')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
