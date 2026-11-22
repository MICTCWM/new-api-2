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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'

import { importCommandGoAccounts } from '../../api'
import {
  COMMAND_GO_IMPORT_LIMIT,
  commandGoAccountsQueryKey,
  extractCommandGoKeys,
  getApiErrorMessage,
} from '../../lib'

interface CommandGoImportDialogProps {
  channelId: number
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function CommandGoImportDialog(props: CommandGoImportDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [text, setText] = useState('')
  const { keys, invalid } = extractCommandGoKeys(text)
  const keyCount = keys.length
  const tooManyKeys = keyCount > COMMAND_GO_IMPORT_LIMIT

  const importMutation = useMutation({
    mutationFn: async () => {
      const response = await importCommandGoAccounts(props.channelId, text)
      if (!response.success || !response.data) {
        throw new Error(response.message || t('Import failed'))
      }
      return response.data
    },
    onSuccess: (data) => {
      toast.success(
        t(
          'Import completed: {{created}} created, {{existing}} existing, {{invalid}} invalid',
          {
            created: data.created.length,
            existing: data.existing.length,
            invalid: data.invalid,
          }
        )
      )
      queryClient.invalidateQueries({
        queryKey: commandGoAccountsQueryKey(props.channelId),
      })
      setText('')
      props.onOpenChange(false)
    },
    onError: (error: unknown) => {
      toast.error(getApiErrorMessage(error, t('Import failed')))
    },
  })

  const handleSubmit = () => {
    if (keyCount === 0 || tooManyKeys || importMutation.isPending) return
    importMutation.mutate()
  }

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle>{t('Import Command Go Accounts')}</DialogTitle>
          <DialogDescription>
            {t(
              'Paste Command Code user_* keys, one per line. Up to 50 keys per import.'
            )}
          </DialogDescription>
        </DialogHeader>
        <div className='space-y-2'>
          <Textarea
            value={text}
            onChange={(event) => setText(event.target.value)}
            rows={10}
            className='font-mono text-xs'
            placeholder={t('user_xxx')}
            aria-label={t('Command Go API keys')}
          />
          <div className='flex items-center justify-between text-xs'>
            <span className='text-muted-foreground'>
              {t('{{count}} key(s) detected', { count: keyCount })}
            </span>
            {tooManyKeys && (
              <span className='text-destructive'>
                {t('A maximum of {{max}} keys can be imported at once', {
                  max: COMMAND_GO_IMPORT_LIMIT,
                })}
              </span>
            )}
            {!tooManyKeys && invalid > 0 && (
              <span className='text-muted-foreground'>
                {t('{{count}} key(s) will be skipped as invalid', {
                  count: invalid,
                })}
              </span>
            )}
          </div>
        </div>
        <DialogFooter>
          <Button
            variant='outline'
            onClick={() => props.onOpenChange(false)}
            disabled={importMutation.isPending}
          >
            {t('Cancel')}
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={keyCount === 0 || tooManyKeys || importMutation.isPending}
          >
            {importMutation.isPending && (
              <Loader2 className='size-4 animate-spin' />
            )}
            {t('Import')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
