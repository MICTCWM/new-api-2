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
import { useQuery } from '@tanstack/react-query'
import { Edit, Plus, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { MultiSelect } from '@/components/multi-select'
import { StatusBadge, StatusBadgeList } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { getGroups } from '@/features/users/api'

import { SettingsSwitchField } from '../../components/settings-form-layout'
import { SettingsPageActionsPortal } from '../../components/settings-page-context'
import { SettingsSection } from '../../components/settings-section'
import { useUpdateOption } from '../../hooks/use-update-option'
import { ResponseRewriteRuleEditorDialog } from './rule-editor-dialog'
import {
  parseResponseRewriteGroups,
  parseResponseRewriteRules,
  serializeResponseRewriteGroups,
  serializeResponseRewriteRules,
  strictParseResponseRewriteRules,
  validateResponseRewriteRules,
  type ResponseRewriteRule,
  type ResponseRewriteSettings,
} from './types'

type Props = {
  defaultValues: ResponseRewriteSettings
}

export function ResponseRewriteSection(props: Props) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const [modelIdEnabled, setModelIdEnabled] = useState(
    props.defaultValues['response_rewrite_setting.model_id_enabled']
  )
  const [modelIdGroups, setModelIdGroups] = useState<string[]>(() =>
    parseResponseRewriteGroups(
      props.defaultValues['response_rewrite_setting.model_id_groups']
    )
  )
  const [textEnabled, setTextEnabled] = useState(
    props.defaultValues['response_rewrite_setting.text_enabled']
  )
  const [rules, setRules] = useState<ResponseRewriteRule[]>(() =>
    parseResponseRewriteRules(
      props.defaultValues['response_rewrite_setting.text_rules']
    )
  )
  const [editMode, setEditMode] = useState<'visual' | 'json'>('visual')
  const [jsonText, setJsonText] = useState(() =>
    JSON.stringify(JSON.parse(serializeResponseRewriteRules(rules)), null, 2)
  )
  const [saving, setSaving] = useState(false)
  const [ruleEditorOpen, setRuleEditorOpen] = useState(false)
  const [editingRule, setEditingRule] = useState<ResponseRewriteRule | null>(
    null
  )
  const [editingIndex, setEditingIndex] = useState<number | null>(null)

  const groupsQuery = useQuery({
    queryKey: ['system-groups'],
    queryFn: getGroups,
  })

  const groupOptions = useMemo(
    () => groupsQuery.data?.data ?? [],
    [groupsQuery.data]
  )
  const groupSelectOptions = useMemo(
    () => groupOptions.map((group) => ({ value: group, label: group })),
    [groupOptions]
  )

  useEffect(() => {
    setModelIdEnabled(
      props.defaultValues['response_rewrite_setting.model_id_enabled']
    )
    setModelIdGroups(
      parseResponseRewriteGroups(
        props.defaultValues['response_rewrite_setting.model_id_groups']
      )
    )
    setTextEnabled(props.defaultValues['response_rewrite_setting.text_enabled'])
    const parsed = parseResponseRewriteRules(
      props.defaultValues['response_rewrite_setting.text_rules']
    )
    setRules(parsed)
    setJsonText(
      JSON.stringify(JSON.parse(serializeResponseRewriteRules(parsed)), null, 2)
    )
  }, [props.defaultValues])

  const handleSave = async () => {
    let nextRules: ResponseRewriteRule[]
    if (editMode === 'json') {
      try {
        nextRules = strictParseResponseRewriteRules(jsonText)
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

    // Rules are validated regardless of the text rewrite switch so the frontend
    // enforces exactly the same rules as the backend validator, which checks
    // stored rules even while text rewriting is disabled.
    const validationError = validateResponseRewriteRules(nextRules)
    if (validationError) {
      toast.error(t(validationError))
      return
    }

    const rulesJson = serializeResponseRewriteRules(nextRules)
    const groupsJson = serializeResponseRewriteGroups(modelIdGroups)

    setSaving(true)
    try {
      const updates: { key: string; value: string }[] = []
      if (
        modelIdEnabled !==
        props.defaultValues['response_rewrite_setting.model_id_enabled']
      ) {
        updates.push({
          key: 'response_rewrite_setting.model_id_enabled',
          value: String(modelIdEnabled),
        })
      }
      if (
        groupsJson !==
        normalizeJsonArray(
          props.defaultValues['response_rewrite_setting.model_id_groups']
        )
      ) {
        updates.push({
          key: 'response_rewrite_setting.model_id_groups',
          value: groupsJson,
        })
      }
      if (
        textEnabled !==
        props.defaultValues['response_rewrite_setting.text_enabled']
      ) {
        updates.push({
          key: 'response_rewrite_setting.text_enabled',
          value: String(textEnabled),
        })
      }
      if (
        rulesJson !==
        normalizeJsonArray(
          props.defaultValues['response_rewrite_setting.text_rules']
        )
      ) {
        updates.push({
          key: 'response_rewrite_setting.text_rules',
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
    } catch {
      toast.error(t('Failed to save'))
    } finally {
      setSaving(false)
    }
  }

  const switchToJsonMode = () => {
    setJsonText(
      JSON.stringify(JSON.parse(serializeResponseRewriteRules(rules)), null, 2)
    )
    setEditMode('json')
  }

  const switchToVisualMode = () => {
    try {
      setRules(strictParseResponseRewriteRules(jsonText))
      setEditMode('visual')
    } catch (error) {
      toast.error(
        t(error instanceof Error ? error.message : 'Invalid rules JSON format')
      )
    }
  }

  return (
    <>
      <SettingsSection title={t('Response Rewrite')}>
        <Alert>
          <AlertDescription className='text-xs'>
            {t(
              'Response rewrite only changes the responses returned to the client. It never affects upstream routing or billing.'
            )}
          </AlertDescription>
        </Alert>

        <div className='flex flex-col gap-3'>
          <h4 className='text-sm font-semibold'>{t('Model ID Rewrite')}</h4>
          <SettingsSwitchField
            checked={modelIdEnabled}
            onCheckedChange={setModelIdEnabled}
            label={t('Enable')}
            description={t(
              'When enabled, responses returned to the client always show the model name the client requested. Upstream routing and billing are unaffected.'
            )}
          />
          <div className='grid gap-1.5'>
            <Label htmlFor='response-rewrite-model-id-groups'>
              {t('Applicable Groups')}
            </Label>
            <MultiSelect
              id='response-rewrite-model-id-groups'
              options={groupSelectOptions}
              selected={modelIdGroups}
              onChange={setModelIdGroups}
              placeholder={t('Select groups')}
              emptyText={t('No matching items')}
            />
            <p className='text-muted-foreground text-xs'>
              {t('Leave empty to apply to all groups.')}
            </p>
          </div>
        </div>

        <Separator />

        <div className='flex flex-col gap-3'>
          <h4 className='text-sm font-semibold'>
            {t('Response Text Rewrite')}
          </h4>
          <SettingsSwitchField
            checked={textEnabled}
            onCheckedChange={setTextEnabled}
            label={t('Enable')}
            description={t(
              'Replace text fragments in responses returned to the client.'
            )}
          />

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
                  id: 'groups',
                  header: t('Applicable Groups'),
                  cell: (rule) =>
                    rule.groups.length === 0 ? (
                      <span className='text-muted-foreground'>
                        {t('All Groups')}
                      </span>
                    ) : (
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
                  id: 'find',
                  header: t('Find Fragment'),
                  cell: (rule) => rule.find || '-',
                },
                {
                  id: 'replace',
                  header: t('Replace Fragment'),
                  cell: (rule) => rule.replace || '-',
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
                            current.filter(
                              (_, itemIndex) => itemIndex !== index
                            )
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
              <Label htmlFor='response-rewrite-rules-json'>
                {t('Rules JSON')}
              </Label>
              <JsonCodeEditor
                id='response-rewrite-rules-json'
                value={jsonText}
                onChange={setJsonText}
                heightClassName='h-[300px] min-h-[300px] max-h-[300px]'
              />
            </div>
          )}
        </div>

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
            <Plus className='mr-1 h-3 w-3' aria-hidden='true' />
            {t('Add Rule')}
          </Button>
          <Button size='sm' onClick={handleSave} disabled={saving}>
            {saving ? t('Saving...') : t('Save')}
          </Button>
        </SettingsPageActionsPortal>
      </SettingsSection>

      <ResponseRewriteRuleEditorDialog
        open={ruleEditorOpen}
        onOpenChange={setRuleEditorOpen}
        rule={editingRule}
        groupOptions={groupOptions}
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

function normalizeJsonArray(value: string): string {
  try {
    const parsed = JSON.parse(value || '[]')
    // The backend serializes a nil slice as null; treat it as an empty array
    // so it does not look like a pending change.
    if (parsed === null) return '[]'
    return JSON.stringify(parsed)
  } catch {
    return '[]'
  }
}
