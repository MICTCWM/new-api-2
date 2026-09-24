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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Edit, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { StatusBadge, StatusBadgeList } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { getEnabledModels } from '@/features/channels/api'
import { getPricing } from '@/features/pricing/api'
import { getGroups } from '@/features/users/api'

import { SettingsSwitchField } from '../../components/settings-form-layout'
import { SettingsPageActionsPortal } from '../../components/settings-page-context'
import { SettingsSection } from '../../components/settings-section'
import { useUpdateOption } from '../../hooks/use-update-option'
import { parseGroupMappingRules } from '../group-mapping/types'
import { getModelSplitUsage } from './api'
import { ModelSplitRuleEditorDialog } from './rule-editor-dialog'
import {
  buildModelSplitUsageRows,
  parseModelSplitRules,
  resolveSplitModelQuotaDisplay,
  serializeModelSplitRules,
  strictParseModelSplitRules,
  validateModelSplitRules,
  type ModelSplitRule,
  type ModelSplitSettings,
  type ModelSplitUsageRow,
} from './types'

type Props = {
  defaultValues: ModelSplitSettings
}

export function ModelSplitSection(props: Props) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const updateOption = useUpdateOption()
  const [enabled, setEnabled] = useState(
    props.defaultValues['model_split_setting.enabled']
  )
  const [rules, setRules] = useState<ModelSplitRule[]>(() =>
    parseModelSplitRules(props.defaultValues['model_split_setting.rules'])
  )
  const [editMode, setEditMode] = useState<'visual' | 'json'>('visual')
  const [jsonText, setJsonText] = useState(() =>
    JSON.stringify(JSON.parse(serializeModelSplitRules(rules)), null, 2)
  )
  const [saving, setSaving] = useState(false)
  const [ruleEditorOpen, setRuleEditorOpen] = useState(false)
  const [editingRule, setEditingRule] = useState<ModelSplitRule | null>(null)
  const [editingIndex, setEditingIndex] = useState<number | null>(null)

  const groupsQuery = useQuery({
    queryKey: ['system-groups'],
    queryFn: getGroups,
  })
  const modelsQuery = useQuery({
    queryKey: ['enabled-channel-models'],
    queryFn: getEnabledModels,
  })
  const usageQuery = useQuery({
    queryKey: ['model-split-usage'],
    queryFn: getModelSplitUsage,
    retry: false,
  })
  const pricingQuery = useQuery({
    // Shares the Model Square cache; the silent flags keep a pricing failure
    // from firing a global toast on this page.
    queryKey: ['pricing'],
    queryFn: () =>
      getPricing({ skipErrorHandler: true, skipBusinessError: true }),
    staleTime: 5 * 60 * 1000,
    retry: false,
  })

  const groupOptions = groupsQuery.data?.data ?? []
  const modelOptions = useMemo(
    () => modelsQuery.data?.data ?? [],
    [modelsQuery.data]
  )

  const savedRules = useMemo(
    () =>
      parseModelSplitRules(props.defaultValues['model_split_setting.rules']),
    [props.defaultValues]
  )

  useEffect(() => {
    setEnabled(props.defaultValues['model_split_setting.enabled'])
    const parsed = parseModelSplitRules(
      props.defaultValues['model_split_setting.rules']
    )
    setRules(parsed)
    setJsonText(
      JSON.stringify(JSON.parse(serializeModelSplitRules(parsed)), null, 2)
    )
  }, [props.defaultValues])

  const mappingNames = useMemo(() => {
    const names = new Set<string>()
    for (const rule of rules) {
      if (rule.original_model) names.add(rule.original_model)
      if (rule.model_1) names.add(rule.model_1)
      if (rule.model_2) names.add(rule.model_2)
      if (rule.image_model) names.add(rule.image_model)
    }
    for (const rule of parseGroupMappingRules(
      props.defaultValues['group_mapping_setting.rules']
    )) {
      for (const entry of rule.entries) {
        if (entry.from) names.add(entry.from)
        if (entry.to) names.add(entry.to)
        if (entry.first_model) names.add(entry.first_model)
        if (entry.default_model) names.add(entry.default_model)
      }
    }
    return [...names]
  }, [props.defaultValues, rules])

  const groupModels = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const model of pricingQuery.data?.data ?? []) {
      const name = model.model_name?.trim()
      if (!name) continue
      for (const group of model.enable_groups ?? []) {
        const key = group.trim()
        if (!key) continue
        const names = map.get(key)
        if (!names) {
          map.set(key, [name])
        } else if (!names.includes(name)) {
          names.push(name)
        }
      }
    }
    return map
  }, [pricingQuery.data])

  const editorModelOptions = useMemo(
    () => [...modelOptions, ...mappingNames],
    [modelOptions, mappingNames]
  )

  const usageRows = useMemo(
    () => buildModelSplitUsageRows(rules, usageQuery.data ?? [], savedRules),
    [rules, usageQuery.data, savedRules]
  )

  const handleSave = async () => {
    let nextRules: ModelSplitRule[]
    if (editMode === 'json') {
      try {
        nextRules = strictParseModelSplitRules(jsonText)
      } catch (error) {
        toast.error(
          t(
            error instanceof Error ? error.message : 'Invalid rules JSON format'
          )
        )
        return
      }
    } else {
      nextRules = rules
    }

    const validationError = validateModelSplitRules(nextRules)
    if (validationError) {
      toast.error(t(validationError))
      return
    }

    const rulesJson = serializeModelSplitRules(nextRules)

    setSaving(true)
    try {
      const updates: { key: string; value: string }[] = []
      if (enabled !== props.defaultValues['model_split_setting.enabled']) {
        updates.push({
          key: 'model_split_setting.enabled',
          value: String(enabled),
        })
      }
      const origSerialized = (() => {
        try {
          return JSON.stringify(
            JSON.parse(props.defaultValues['model_split_setting.rules'] || '[]')
          )
        } catch {
          return '[]'
        }
      })()
      if (rulesJson !== origSerialized) {
        updates.push({
          key: 'model_split_setting.rules',
          value: rulesJson,
        })
      }
      if (updates.length === 0) {
        toast.info(t('No changes'))
        return
      }
      for (const update of updates) {
        await updateOption.mutateAsync(update)
      }
      await queryClient.invalidateQueries({ queryKey: ['model-split-usage'] })
    } catch {
      toast.error(t('Failed to save'))
    } finally {
      setSaving(false)
    }
  }

  const switchToJsonMode = () => {
    setJsonText(
      JSON.stringify(JSON.parse(serializeModelSplitRules(rules)), null, 2)
    )
    setEditMode('json')
  }

  const switchToVisualMode = () => {
    try {
      setRules(strictParseModelSplitRules(jsonText))
      setEditMode('visual')
    } catch (error) {
      toast.error(
        t(error instanceof Error ? error.message : 'Invalid rules JSON format')
      )
    }
  }

  return (
    <>
      <SettingsSection title={t('Model Split Routing')}>
        <Alert>
          <AlertDescription className='text-xs'>
            {t(
              'Triggered by the original model name sent by the client. Matching requests are split between two models by request count. Requests with images can be forced to a designated model. Counts are tracked independently per group with a window of 100. Failed requests do not consume a count.'
            )}
          </AlertDescription>
        </Alert>

        <SettingsSwitchField
          checked={enabled}
          onCheckedChange={setEnabled}
          label={t('Enable')}
          description={t(
            'Turn off to keep the rules saved without applying them.'
          )}
        />

        <Separator />

        <SettingsPageActionsPortal>
          <Button
            variant={editMode === 'visual' ? 'default' : 'outline'}
            size='sm'
            onClick={editMode === 'json' ? switchToVisualMode : undefined}
          >
            {t('Visual')}
          </Button>
          <Button
            variant={editMode === 'json' ? 'default' : 'outline'}
            size='sm'
            onClick={editMode === 'visual' ? switchToJsonMode : undefined}
          >
            JSON
          </Button>
          <Button
            variant='outline'
            size='sm'
            onClick={() => {
              setEditingRule(null)
              setEditingIndex(null)
              setRuleEditorOpen(true)
            }}
          >
            <Plus className='mr-1 h-3 w-3' />
            {t('Add Rule')}
          </Button>
          <Button size='sm' onClick={handleSave} disabled={saving}>
            {saving ? t('Saving...') : t('Save')}
          </Button>
        </SettingsPageActionsPortal>

        {editMode === 'visual' ? (
          <StaticDataTable
            tableClassName='min-w-max'
            data={rules}
            getRowKey={(rule) => rule.id}
            emptyClassName='text-muted-foreground py-8'
            emptyContent={t('No rules yet')}
            columns={[
              {
                id: 'name',
                header: t('Name'),
                cellClassName: 'font-medium',
                cell: (rule) => rule.name || '-',
              },
              {
                id: 'enabled',
                header: t('Status'),
                cell: (rule) => (
                  <StatusBadge
                    label={rule.enabled ? t('Enabled') : t('Disabled')}
                    variant={rule.enabled ? 'success' : 'neutral'}
                    copyable={false}
                  />
                ),
              },
              {
                id: 'groups',
                header: t('Groups'),
                cell: (rule) => (
                  <StatusBadgeList
                    items={rule.groups}
                    max={2}
                    getKey={(item) => item}
                    renderItem={(item) => (
                      <StatusBadge
                        label={item}
                        variant='neutral'
                        size='sm'
                        copyable={false}
                      />
                    )}
                  />
                ),
              },
              {
                id: 'original',
                header: t('Original Model'),
                cell: (rule) => rule.original_model || '-',
              },
              {
                id: 'split',
                header: t('Split'),
                cell: (rule) =>
                  formatSplitSummary(
                    rule.model_1,
                    rule.percent_1,
                    rule.model_2,
                    rule.percent_2
                  ),
              },
              {
                id: 'image',
                header: t('Image split'),
                cell: (rule) =>
                  rule.image_split_enabled ? rule.image_model || '-' : '-',
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (rule, index) => (
                  <div className='flex justify-end gap-1'>
                    <Button
                      variant='ghost'
                      size='icon'
                      className='h-7 w-7'
                      aria-label={t('Edit')}
                      onClick={() => {
                        setEditingRule(rule)
                        setEditingIndex(index)
                        setRuleEditorOpen(true)
                      }}
                    >
                      <Edit className='h-3 w-3' aria-hidden='true' />
                    </Button>
                    <Button
                      variant='ghost'
                      size='icon'
                      className='h-7 w-7'
                      aria-label={t('Delete')}
                      onClick={() =>
                        setRules((current) =>
                          current.filter((_, itemIndex) => itemIndex !== index)
                        )
                      }
                    >
                      <Trash2 className='h-3 w-3' aria-hidden='true' />
                    </Button>
                  </div>
                ),
              },
            ]}
          />
        ) : (
          <div className='grid gap-1.5'>
            <Label htmlFor='model-split-rules-json'>{t('Rules JSON')}</Label>
            <JsonCodeEditor
              id='model-split-rules-json'
              value={jsonText}
              onChange={setJsonText}
              heightClassName='h-[300px] min-h-[300px] max-h-[300px]'
            />
          </div>
        )}

        <Separator />

        <RemainingCountsTable
          rows={usageRows}
          loading={usageQuery.isLoading}
          error={usageQuery.isError}
          refreshing={usageQuery.isFetching}
          onRefresh={() => {
            void usageQuery.refetch()
          }}
        />
      </SettingsSection>

      <ModelSplitRuleEditorDialog
        open={ruleEditorOpen}
        onOpenChange={setRuleEditorOpen}
        rule={editingRule}
        groupOptions={groupOptions}
        modelOptions={editorModelOptions}
        groupModels={groupModels}
        onSave={(rule) => {
          setRules((current) => {
            if (
              editingIndex == null ||
              editingIndex < 0 ||
              editingIndex >= current.length
            ) {
              return [...current, rule]
            }
            const next = [...current]
            next[editingIndex] = rule
            return next
          })
          setEditingRule(null)
          setEditingIndex(null)
        }}
      />
    </>
  )
}

function RemainingCountsTable(props: {
  rows: ModelSplitUsageRow[]
  loading: boolean
  error: boolean
  refreshing: boolean
  onRefresh: () => void
}) {
  const { t } = useTranslation()
  let emptyContent = t('No remaining usage yet')
  if (props.loading) emptyContent = t('Loading...')
  if (props.error) emptyContent = t('Failed to load remaining usage')

  return (
    <div className='flex flex-col gap-3'>
      <div className='flex items-center justify-between gap-2'>
        <span className='text-sm leading-none font-medium'>
          {t('Remaining counts')}
        </span>
        <Button
          variant='outline'
          size='sm'
          onClick={props.onRefresh}
          disabled={props.refreshing}
        >
          <RefreshCw
            className={`mr-1 h-3 w-3 ${props.refreshing ? 'animate-spin' : ''}`}
            aria-hidden='true'
          />
          {t('Refresh')}
        </Button>
      </div>
      {props.error && props.rows.length > 0 ? (
        <p className='text-destructive text-xs'>
          {t('Failed to load remaining usage')}
        </p>
      ) : null}
      <StaticDataTable
        tableClassName='min-w-max'
        data={props.rows}
        getRowKey={(row) => row.key}
        emptyClassName='text-muted-foreground py-8'
        emptyContent={emptyContent}
        columns={[
          {
            id: 'rule',
            header: t('Name'),
            cellClassName: 'font-medium',
            cell: (row) => row.rule_name || '-',
          },
          {
            id: 'group',
            header: t('Group'),
            cell: (row) => row.group || '-',
          },
          {
            id: 'window',
            header: t('Window remaining'),
            cell: (row) =>
              formatWindowRemaining(row.remaining_total, row.window),
          },
          {
            id: 'models',
            header: t('Remaining'),
            cell: (row) => <SplitModelsRemainingCell row={row} />,
          },
        ]}
      />
    </div>
  )
}

function SplitModelsRemainingCell(props: { row: ModelSplitUsageRow }) {
  return (
    <div className='flex flex-col gap-1.5 text-xs'>
      <SplitModelRemainingCell
        model={props.row.model_1}
        remaining={props.row.remaining_model_1}
        percent={props.row.percent_1}
      />
      <SplitModelRemainingCell
        model={props.row.model_2}
        remaining={props.row.remaining_model_2}
        percent={props.row.percent_2}
      />
    </div>
  )
}

function SplitModelRemainingCell(props: {
  model: string
  remaining: number | null
  percent: number
}) {
  const { t } = useTranslation()
  const { showPercent, used } = resolveSplitModelQuotaDisplay(
    props.remaining,
    props.percent
  )
  return (
    <div className='flex flex-col gap-0.5'>
      <span className='whitespace-nowrap'>
        {props.model || '-'}: <RemainingNumber value={props.remaining} />
        {showPercent ? ` / ${props.percent}` : null}
      </span>
      <span className='text-muted-foreground whitespace-nowrap'>
        {t('Used')} {used == null ? '-' : used}
      </span>
    </div>
  )
}

function RemainingNumber(props: { value: number | null }) {
  const { t } = useTranslation()
  if (props.value == null) return '-'
  if (props.value < 0) {
    return (
      <span className='text-destructive font-medium'>
        {t('{{value}} (debt)', { value: props.value })}
      </span>
    )
  }
  return <span>{props.value}</span>
}

function formatSplitSummary(
  model1: string,
  percent1: number,
  model2: string,
  percent2: number
): string {
  const left = model1 ? `${model1} ${percent1}%` : '-'
  const right = model2 ? `${model2} ${percent2}%` : '-'
  return `${left} / ${right}`
}

function formatWindowRemaining(value: number | null, window: number): string {
  if (value == null) return '-'
  return `${value} / ${window}`
}
