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
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import {
  createResponseRewriteRuleId,
  validateResponseRewriteRule,
  type ResponseRewriteRule,
} from './types'

const RULE_FORM_ID = 'response-rewrite-rule-form'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  rule: ResponseRewriteRule | null
  groupOptions: string[]
  onSave: (rule: ResponseRewriteRule) => void
}

export function ResponseRewriteRuleEditorDialog(props: Props) {
  const { t } = useTranslation()
  const isEdit = props.rule != null
  const [name, setName] = useState('')
  const [groups, setGroups] = useState<string[]>([])
  const [find, setFind] = useState('')
  const [replace, setReplace] = useState('')

  useEffect(() => {
    if (!props.open) return
    setName(props.rule?.name ?? '')
    setGroups(props.rule?.groups ?? [])
    setFind(props.rule?.find ?? '')
    setReplace(props.rule?.replace ?? '')
  }, [props.open, props.rule])

  const groupSelectOptions = useMemo(
    () => props.groupOptions.map((group) => ({ value: group, label: group })),
    [props.groupOptions]
  )

  const handleSave = () => {
    const errorKey = validateResponseRewriteRule({ find })
    if (errorKey) {
      toast.error(t(errorKey))
      return
    }
    props.onSave({
      id: props.rule?.id.trim() || createResponseRewriteRuleId(),
      name: name.trim(),
      groups,
      find,
      replace,
    })
    props.onOpenChange(false)
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={isEdit ? t('Edit Rule') : t('Add Rule')}
      contentClassName='sm:max-w-2xl'
      contentHeight='auto'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button type='submit' form={RULE_FORM_ID}>
            {t('Save')}
          </Button>
        </>
      }
    >
      <form
        id={RULE_FORM_ID}
        className='flex flex-col gap-4'
        onSubmit={(event) => {
          event.preventDefault()
          handleSave()
        }}
      >
        <div className='grid gap-1.5'>
          <Label htmlFor='response-rewrite-rule-name'>
            {t('Name')}{' '}
            <span className='text-muted-foreground'>({t('Optional')})</span>
          </Label>
          <Input
            id='response-rewrite-rule-name'
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={t('Rule Name')}
          />
        </div>

        <div className='grid gap-1.5'>
          <Label htmlFor='response-rewrite-rule-groups'>
            {t('Applicable Groups')}
          </Label>
          <MultiSelect
            id='response-rewrite-rule-groups'
            options={groupSelectOptions}
            selected={groups}
            onChange={setGroups}
            placeholder={t('Select groups')}
            emptyText={t('No matching items')}
          />
          <p className='text-muted-foreground text-xs'>
            {t('Leave empty to apply to all groups.')}
          </p>
        </div>

        <div className='grid gap-1.5'>
          <Label htmlFor='response-rewrite-rule-find'>
            {t('Find Fragment')}
          </Label>
          <Input
            id='response-rewrite-rule-find'
            value={find}
            onChange={(event) => setFind(event.target.value)}
            placeholder={t('Text to find in the response')}
          />
        </div>

        <div className='grid gap-1.5'>
          <Label htmlFor='response-rewrite-rule-replace'>
            {t('Replace Fragment')}
          </Label>
          <Textarea
            id='response-rewrite-rule-replace'
            value={replace}
            onChange={(event) => setReplace(event.target.value)}
            placeholder={t('Replacement text')}
            rows={3}
          />
        </div>
      </form>
    </Dialog>
  )
}
