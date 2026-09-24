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
import { StatusBadge, StatusBadgeList } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { getEnabledModels } from '@/features/channels/api'
import { getGroups } from '@/features/users/api'

import { SettingsSwitchField } from '../../components/settings-form-layout'
import { SettingsPageActionsPortal } from '../../components/settings-page-context'
import { SettingsSection } from '../../components/settings-section'
import { useUpdateOption } from '../../hooks/use-update-option'
import { GroupMappingRuleEditorDialog } from './rule-editor-dialog'
import {
  parseGroupMappingRules,
  serializeGroupMappingRules,
  type GroupMappingRule,
  type GroupMappingSettings,
} from './types'

type Props = {
  defaultValues: GroupMappingSettings
}

export function GroupMappingSection(props: Props) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const [enabled, setEnabled] = useState(
    props.defaultValues['group_mapping_setting.enabled']
  )
  const [rules, setRules] = useState<GroupMappingRule[]>(() =>
    parseGroupMappingRules(props.defaultValues['group_mapping_setting.rules'])
  )
  const [editMode, setEditMode] = useState<'visual' | 'json'>('visual')
  const [jsonText, setJsonText] = useState(() =>
    JSON.stringify(JSON.parse(serializeGroupMappingRules(rules)), null, 2)
  )
  const [saving, setSaving] = useState(false)
  const [ruleEditorOpen, setRuleEditorOpen] = useState(false)
  const [editingRule, setEditingRule] = useState<GroupMappingRule | null>(null)

  const groupsQuery = useQuery({
    queryKey: ['system-groups'],
    queryFn: getGroups,
  })
  const modelsQuery = useQuery({
    queryKey: ['enabled-channel-models'],
    queryFn: getEnabledModels,
  })

  const groupOptions = groupsQuery.data?.data ?? []
  const modelOptions = modelsQuery.data?.data ?? []

  useEffect(() => {
    setEnabled(props.defaultValues['group_mapping_setting.enabled'])
    const parsed = parseGroupMappingRules(
      props.defaultValues['group_mapping_setting.rules']
    )
    setRules(parsed)
    setJsonText(
      JSON.stringify(JSON.parse(serializeGroupMappingRules(parsed)), null, 2)
    )
  }, [props.defaultValues])

  const mappingNames = useMemo(() => {
    const names = new Set<string>()
    for (const rule of rules) {
      for (const entry of rule.entries) {
        if (entry.from) names.add(entry.from)
        if (entry.to) names.add(entry.to)
        if (entry.first_model) names.add(entry.first_model)
        if (entry.default_model) names.add(entry.default_model)
      }
    }
    return [...names]
  }, [rules])

  const handleSave = async () => {
    let rulesJson: string
    if (editMode === 'json') {
      try {
        const parsed = JSON.parse(jsonText)
        if (!Array.isArray(parsed)) {
          toast.error(t('Rules JSON must be an array'))
          return
        }
        rulesJson = serializeGroupMappingRules(
          parseGroupMappingRules(JSON.stringify(parsed))
        )
      } catch {
        toast.error(t('Invalid rules JSON format'))
        return
      }
    } else {
      rulesJson = serializeGroupMappingRules(rules)
    }

    setSaving(true)
    try {
      const updates: { key: string; value: string }[] = []
      if (enabled !== props.defaultValues['group_mapping_setting.enabled']) {
        updates.push({
          key: 'group_mapping_setting.enabled',
          value: String(enabled),
        })
      }
      const origSerialized = (() => {
        try {
          return JSON.stringify(
            JSON.parse(
              props.defaultValues['group_mapping_setting.rules'] || '[]'
            )
          )
        } catch {
          return '[]'
        }
      })()
      if (rulesJson !== origSerialized) {
        updates.push({
          key: 'group_mapping_setting.rules',
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
      JSON.stringify(JSON.parse(serializeGroupMappingRules(rules)), null, 2)
    )
    setEditMode('json')
  }

  const switchToVisualMode = () => {
    try {
      const parsed = parseGroupMappingRules(jsonText)
      setRules(parsed)
      setEditMode('visual')
    } catch {
      toast.error(t('Invalid rules JSON format'))
    }
  }

  return (
    <>
      <SettingsSection title={t('Group Model Mapping')}>
        <Alert>
          <AlertDescription className='text-xs'>
            {t(
              'Group mapping remaps requested models for selected groups. It is applied before channel selection and overrides channel model mapping when both define the same source model.'
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
                id: 'mappings',
                header: t('Mappings'),
                cell: (rule) =>
                  rule.entries.length === 0
                    ? '-'
                    : rule.entries
                        .map((entry) => `${entry.from} → ${entry.to}`)
                        .join(', '),
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
                      onClick={() => {
                        setEditingRule(rule)
                        setRuleEditorOpen(true)
                      }}
                    >
                      <Edit className='h-3 w-3' />
                    </Button>
                    <Button
                      variant='ghost'
                      size='icon'
                      className='h-7 w-7'
                      onClick={() =>
                        setRules((current) =>
                          current.filter((_, itemIndex) => itemIndex !== index)
                        )
                      }
                    >
                      <Trash2 className='h-3 w-3' />
                    </Button>
                  </div>
                ),
              },
            ]}
          />
        ) : (
          <div className='grid gap-1.5'>
            <Label htmlFor='group-mapping-rules-json'>{t('Rules JSON')}</Label>
            <JsonCodeEditor
              id='group-mapping-rules-json'
              value={jsonText}
              onChange={setJsonText}
              heightClassName='h-[300px] min-h-[300px] max-h-[300px]'
            />
          </div>
        )}
      </SettingsSection>

      <GroupMappingRuleEditorDialog
        open={ruleEditorOpen}
        onOpenChange={setRuleEditorOpen}
        rule={editingRule}
        groupOptions={groupOptions}
        modelOptions={[...modelOptions, ...mappingNames]}
        onSave={(rule) => {
          setRules((current) => {
            const existingIndex = current.findIndex(
              (item) => item.id === rule.id
            )
            if (existingIndex >= 0) {
              const next = [...current]
              next[existingIndex] = rule
              return next
            }
            return [...current, rule]
          })
          setEditingRule(null)
        }}
      />
    </>
  )
}
