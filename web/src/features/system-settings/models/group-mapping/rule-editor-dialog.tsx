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
import { ChevronDown, Plus, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'

import { SettingsSwitchField } from '../../components/settings-form-layout'
import {
  createGroupMappingRuleId,
  type GroupMappingEntry,
  type GroupMappingRule,
} from './types'

const RULE_FORM_ID = 'group-mapping-rule-form'

type MappingDraft = GroupMappingEntry & { rowId: string }

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  rule: GroupMappingRule | null
  groupOptions: string[]
  modelOptions: string[]
  onSave: (rule: GroupMappingRule) => void
}

function createEntry(from = '', to = ''): MappingDraft {
  return {
    rowId: createGroupMappingRuleId(),
    from,
    to,
    hide_original: false,
    hidden_models: to ? [to] : [],
    first_model: from,
    default_model: to,
  }
}

function toModelOptions(values: string[]) {
  return values
    .filter(Boolean)
    .filter((value, index, all) => all.indexOf(value) === index)
    .map((value) => ({ value, label: value }))
}

export function GroupMappingRuleEditorDialog(props: Props) {
  const { t } = useTranslation()
  const isEdit = Boolean(props.rule?.id && props.rule.name)
  const [name, setName] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [groups, setGroups] = useState<string[]>([])
  const [entries, setEntries] = useState<MappingDraft[]>([createEntry()])

  useEffect(() => {
    if (!props.open) return
    setName(props.rule?.name ?? '')
    setEnabled(props.rule?.enabled ?? true)
    setGroups(props.rule?.groups ?? [])
    setEntries(
      props.rule?.entries?.length
        ? props.rule.entries.map((entry) => ({
            ...createEntry(entry.from, entry.to),
            ...entry,
            hidden_models: entry.hidden_models ?? [],
          }))
        : [createEntry()]
    )
  }, [props.open, props.rule])

  const generatedModels = useMemo(() => {
    const names = [...props.modelOptions]
    for (const entry of entries) {
      names.push(
        entry.from,
        entry.to,
        entry.first_model ?? '',
        entry.default_model ?? ''
      )
    }
    return toModelOptions(names)
  }, [entries, props.modelOptions])

  const groupSelectOptions = useMemo(
    () => props.groupOptions.map((group) => ({ value: group, label: group })),
    [props.groupOptions]
  )

  const updateEntry = (rowId: string, patch: Partial<MappingDraft>) => {
    setEntries((current) =>
      current.map((entry) =>
        entry.rowId === rowId ? { ...entry, ...patch } : entry
      )
    )
  }

  const handleSave = () => {
    const trimmedName = name.trim()
    if (!trimmedName) {
      toast.error(t('Name is required'))
      return
    }
    if (groups.length === 0) {
      toast.error(t('At least one group is required'))
      return
    }
    const cleanedEntries = entries
      .map((entry) => ({
        from: entry.from.trim(),
        to: entry.to.trim(),
        hide_original: entry.hide_original,
        hidden_models: entry.hide_original
          ? (entry.hidden_models ?? [])
              .map((item) => item.trim())
              .filter(Boolean)
          : [],
        first_model: (entry.first_model || entry.from).trim(),
        default_model: (entry.default_model || entry.to).trim(),
      }))
      .filter((entry) => entry.from || entry.to)
    if (cleanedEntries.length === 0) {
      toast.error(t('At least one mapping is required'))
      return
    }
    if (cleanedEntries.some((entry) => !entry.from || !entry.to)) {
      toast.error(t('Original and replacement models are required'))
      return
    }
    const seen = new Set<string>()
    for (const entry of cleanedEntries) {
      if (seen.has(entry.from)) {
        toast.error(
          t('Duplicate source model(s): {{models}}', { models: entry.from })
        )
        return
      }
      seen.add(entry.from)
    }
    props.onSave({
      id: props.rule?.id || createGroupMappingRuleId(),
      name: trimmedName,
      enabled,
      groups,
      entries: cleanedEntries,
    })
    props.onOpenChange(false)
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={isEdit ? t('Edit Rule') : t('Add Rule')}
      contentClassName='sm:max-w-3xl'
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
        <div className='grid gap-4 md:grid-cols-2'>
          <div className='grid gap-1.5'>
            <Label htmlFor='group-mapping-rule-name'>{t('Name')}</Label>
            <Input
              id='group-mapping-rule-name'
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={t('Rule Name')}
            />
          </div>
          <SettingsSwitchField
            checked={enabled}
            onCheckedChange={setEnabled}
            label={t('Enable')}
            className='py-0'
          />
        </div>

        <div className='grid gap-1.5'>
          <Label>{t('Groups')}</Label>
          <MultiSelect
            options={groupSelectOptions}
            selected={groups}
            onChange={setGroups}
            placeholder={t('Select groups')}
            emptyText={t('No matching items')}
          />
        </div>

        <Separator />

        <div className='flex items-center justify-between gap-2'>
          <Label>{t('Mappings')}</Label>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => setEntries((current) => [...current, createEntry()])}
          >
            <Plus className='mr-1 h-3 w-3' />
            {t('Add mapping')}
          </Button>
        </div>

        <div className='flex flex-col gap-4'>
          {entries.map((entry, index) => (
            <div
              key={entry.rowId}
              className='bg-muted/30 flex flex-col gap-3 rounded-lg border p-3'
            >
              <div className='flex items-center justify-between gap-2'>
                <span className='text-sm font-medium'>
                  {t('Mapping')} {index + 1}
                </span>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon-sm'
                  disabled={entries.length === 1}
                  onClick={() =>
                    setEntries((current) =>
                      current.filter((item) => item.rowId !== entry.rowId)
                    )
                  }
                  aria-label={t('Delete')}
                >
                  <Trash2 className='h-3.5 w-3.5' />
                </Button>
              </div>

              <div className='grid gap-3 md:grid-cols-2'>
                <div className='grid gap-1.5'>
                  <Label>{t('Original Model')}</Label>
                  <ComboboxInput
                    options={generatedModels}
                    value={entry.from}
                    onValueChange={(value) =>
                      updateEntry(entry.rowId, {
                        from: value,
                        first_model: entry.first_model || value,
                      })
                    }
                    placeholder={t('Select or enter model name')}
                    allowCustomValue
                  />
                </div>
                <div className='grid gap-1.5'>
                  <Label>{t('Replacement Model')}</Label>
                  <ComboboxInput
                    options={generatedModels}
                    value={entry.to}
                    onValueChange={(value) =>
                      updateEntry(entry.rowId, {
                        to: value,
                        default_model: entry.default_model || value,
                        hidden_models:
                          entry.hide_original &&
                          (entry.hidden_models?.length ?? 0) === 0
                            ? [value]
                            : entry.hidden_models,
                      })
                    }
                    placeholder={t('Select or enter model name')}
                    allowCustomValue
                  />
                </div>
              </div>

              <SettingsSwitchField
                checked={entry.hide_original}
                onCheckedChange={(checked) =>
                  updateEntry(entry.rowId, {
                    hide_original: checked,
                    hidden_models:
                      checked &&
                      (entry.hidden_models?.length ?? 0) === 0 &&
                      entry.to
                        ? [entry.to]
                        : entry.hidden_models,
                  })
                }
                label={t('Hide original models')}
                description={t(
                  'Hidden models stay usable by mapping rules, but they are removed from the model square and user model pickers.'
                )}
                className='py-0'
              />

              {entry.hide_original ? (
                <div className='grid gap-1.5'>
                  <Label>{t('Hidden models')}</Label>
                  <MultiSelect
                    options={generatedModels}
                    selected={entry.hidden_models ?? []}
                    onChange={(values) =>
                      updateEntry(entry.rowId, { hidden_models: values })
                    }
                    allowCreate
                    placeholder={t('Select models to hide')}
                    emptyText={t('No matching items')}
                  />
                </div>
              ) : null}

              <Collapsible>
                <CollapsibleTrigger className='text-muted-foreground flex items-center gap-1 text-sm'>
                  <ChevronDown className='h-3.5 w-3.5' />
                  {t('Display and default models')}
                </CollapsibleTrigger>
                <CollapsibleContent className='mt-3 grid gap-3 md:grid-cols-2'>
                  <div className='grid gap-1.5'>
                    <Label>{t('Display model')}</Label>
                    <ComboboxInput
                      options={generatedModels}
                      value={entry.first_model || entry.from}
                      onValueChange={(value) =>
                        updateEntry(entry.rowId, { first_model: value })
                      }
                      placeholder={t('Select or enter model name')}
                      allowCustomValue
                    />
                  </div>
                  <div className='grid gap-1.5'>
                    <Label>{t('Default model')}</Label>
                    <ComboboxInput
                      options={generatedModels}
                      value={entry.default_model || entry.to}
                      onValueChange={(value) =>
                        updateEntry(entry.rowId, { default_model: value })
                      }
                      placeholder={t('Select or enter model name')}
                      allowCustomValue
                    />
                  </div>
                </CollapsibleContent>
              </Collapsible>
            </div>
          ))}
        </div>
      </form>
    </Dialog>
  )
}
