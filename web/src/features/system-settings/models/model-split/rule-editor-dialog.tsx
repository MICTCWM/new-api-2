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
import { ComboboxInput } from '@/components/ui/combobox-input'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'

import { SettingsSwitchField } from '../../components/settings-form-layout'
import {
  createModelSplitRuleId,
  validateModelSplitRule,
  type ModelSplitRule,
} from './types'

const RULE_FORM_ID = 'model-split-rule-form'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  rule: ModelSplitRule | null
  groupOptions: string[]
  modelOptions: string[]
  groupModels: Map<string, string[]>
  onSave: (rule: ModelSplitRule) => void
}

export function ModelSplitRuleEditorDialog(props: Props) {
  const { t } = useTranslation()
  const isEdit = props.rule != null
  const [name, setName] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [groups, setGroups] = useState<string[]>([])
  const [originalModel, setOriginalModel] = useState('')
  const [model1, setModel1] = useState('')
  const [percent1Text, setPercent1Text] = useState('50')
  const [model2, setModel2] = useState('')
  const [percent2Text, setPercent2Text] = useState('50')
  const [imageSplitEnabled, setImageSplitEnabled] = useState(false)
  const [imageModel, setImageModel] = useState('')

  useEffect(() => {
    if (!props.open) return
    setName(props.rule?.name ?? '')
    setEnabled(props.rule?.enabled ?? true)
    setGroups(props.rule?.groups ?? [])
    setOriginalModel(props.rule?.original_model ?? '')
    setModel1(props.rule?.model_1 ?? '')
    setPercent1Text(String(props.rule?.percent_1 ?? 50))
    setModel2(props.rule?.model_2 ?? '')
    setPercent2Text(String(props.rule?.percent_2 ?? 50))
    setImageSplitEnabled(props.rule?.image_split_enabled ?? false)
    setImageModel(props.rule?.image_model ?? '')
  }, [props.open, props.rule])

  const groupVisibleModels = useMemo(() => {
    const names: string[] = []
    for (const group of groups) {
      for (const name of props.groupModels.get(group) ?? []) {
        if (!names.includes(name)) names.push(name)
      }
    }
    return names
  }, [groups, props.groupModels])

  const generatedModels = useMemo(() => {
    const names = [
      ...groupVisibleModels,
      ...props.modelOptions,
      originalModel,
      model1,
      model2,
      imageModel,
    ]
    return names
      .filter(Boolean)
      .filter((value, index, all) => all.indexOf(value) === index)
      .map((value) => ({ value, label: value }))
  }, [
    groupVisibleModels,
    imageModel,
    model1,
    model2,
    originalModel,
    props.modelOptions,
  ])

  const imageModelOptions = useMemo(
    () =>
      [model1, model2]
        .map((value) => value.trim())
        .filter(Boolean)
        .filter((value, index, all) => all.indexOf(value) === index)
        .map((value) => ({ value, label: value })),
    [model1, model2]
  )

  const groupSelectOptions = useMemo(
    () => props.groupOptions.map((group) => ({ value: group, label: group })),
    [props.groupOptions]
  )

  const percent1 = parsePercentInput(percent1Text)
  const percent2 = parsePercentInput(percent2Text)
  const percentSum =
    percent1 != null && percent2 != null ? percent1 + percent2 : null

  const handleSave = () => {
    const nextRule = {
      name: name.trim(),
      groups,
      original_model: originalModel.trim(),
      model_1: model1.trim(),
      percent_1: percent1 ?? Number.NaN,
      model_2: model2.trim(),
      percent_2: percent2 ?? Number.NaN,
      image_split_enabled: imageSplitEnabled,
      image_model: imageModel.trim(),
    }
    const errorKey = validateModelSplitRule(nextRule)
    if (errorKey) {
      toast.error(t(errorKey))
      return
    }
    props.onSave({
      id: props.rule?.id.trim() || createModelSplitRuleId(),
      enabled,
      ...nextRule,
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
            <Label htmlFor='model-split-rule-name'>{t('Name')}</Label>
            <Input
              id='model-split-rule-name'
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
          <Label htmlFor='model-split-rule-groups'>{t('Groups')}</Label>
          <MultiSelect
            id='model-split-rule-groups'
            options={groupSelectOptions}
            selected={groups}
            onChange={setGroups}
            placeholder={t('Select groups')}
            emptyText={t('No matching items')}
          />
        </div>

        <div className='grid gap-1.5'>
          <Label htmlFor='model-split-original-model'>
            {t('Original Model')}
          </Label>
          <ComboboxInput
            id='model-split-original-model'
            options={generatedModels}
            value={originalModel}
            onValueChange={setOriginalModel}
            placeholder={t('Select or enter model name')}
            allowCustomValue
          />
        </div>

        <Separator />

        <div className='grid gap-4 md:grid-cols-2'>
          <div className='grid gap-1.5'>
            <Label htmlFor='model-split-model-1'>{t('Model 1')}</Label>
            <ComboboxInput
              id='model-split-model-1'
              options={generatedModels}
              value={model1}
              onValueChange={(value) => {
                setModel1(value)
                if (
                  imageModel &&
                  imageModel !== value &&
                  imageModel !== model2
                ) {
                  setImageModel('')
                }
              }}
              placeholder={t('Select or enter model name')}
              allowCustomValue
            />
          </div>
          <div className='grid gap-1.5'>
            <Label htmlFor='model-split-percent-1'>{t('Percent 1')}</Label>
            <Input
              id='model-split-percent-1'
              type='number'
              min={0}
              max={100}
              step={1}
              value={percent1Text}
              onChange={(event) => setPercent1Text(event.target.value)}
            />
          </div>
          <div className='grid gap-1.5'>
            <Label htmlFor='model-split-model-2'>{t('Model 2')}</Label>
            <ComboboxInput
              id='model-split-model-2'
              options={generatedModels}
              value={model2}
              onValueChange={(value) => {
                setModel2(value)
                if (
                  imageModel &&
                  imageModel !== value &&
                  imageModel !== model1
                ) {
                  setImageModel('')
                }
              }}
              placeholder={t('Select or enter model name')}
              allowCustomValue
            />
          </div>
          <div className='grid gap-1.5'>
            <Label htmlFor='model-split-percent-2'>{t('Percent 2')}</Label>
            <Input
              id='model-split-percent-2'
              type='number'
              min={0}
              max={100}
              step={1}
              value={percent2Text}
              onChange={(event) => setPercent2Text(event.target.value)}
            />
          </div>
        </div>

        <p
          className={
            percentSum === 100
              ? 'text-muted-foreground text-xs'
              : 'text-destructive text-xs'
          }
        >
          {t('Percents must add up to 100')} ({percentSum ?? '-'} / 100)
        </p>

        <SettingsSwitchField
          checked={imageSplitEnabled}
          onCheckedChange={setImageSplitEnabled}
          label={t('Image split')}
          description={t(
            'When enabled, requests with images are forced to the selected split model.'
          )}
          className='py-0'
        />

        {imageSplitEnabled ? (
          <div className='grid gap-1.5'>
            <Label htmlFor='model-split-image-model'>{t('Image model')}</Label>
            <ComboboxInput
              id='model-split-image-model'
              options={imageModelOptions}
              value={imageModel}
              onValueChange={setImageModel}
              placeholder={t('Select a split model')}
            />
          </div>
        ) : null}
      </form>
    </Dialog>
  )
}

function parsePercentInput(raw: string): number | null {
  const trimmed = raw.trim()
  if (trimmed === '') return null
  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed)) return null
  return parsed
}
