import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { MultiSelect } from '@/components/multi-select'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { getGroups } from '@/features/users/api'

import { SettingsSwitchField } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

type LogDisplaySettings = {
  'log_timing_optimization.enabled': boolean
  'log_timing_optimization.groups': string
}

type LogDisplaySectionProps = {
  defaultValues: LogDisplaySettings
}

function parseGroups(raw: string): string[] {
  try {
    const parsed = JSON.parse(raw || '[]')
    if (!Array.isArray(parsed)) return []
    const result: string[] = []
    const seen = new Set<string>()
    for (const value of parsed) {
      const trimmed = String(value).trim()
      if (!trimmed || seen.has(trimmed)) continue
      seen.add(trimmed)
      result.push(trimmed)
    }
    return result
  } catch {
    return []
  }
}

function serializeGroups(groups: string[]): string {
  return JSON.stringify(parseGroups(JSON.stringify(groups)))
}

export function LogDisplaySection({ defaultValues }: LogDisplaySectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const defaultEnabled = defaultValues['log_timing_optimization.enabled']
  const defaultGroupsRaw = defaultValues['log_timing_optimization.groups']
  const [enabled, setEnabled] = useState(defaultEnabled)
  const [groups, setGroups] = useState<string[]>(() =>
    parseGroups(defaultGroupsRaw)
  )
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)

  // Depends on the primitive values, not the defaultValues object: the parent
  // rebuilds that object on every render and would wipe unsaved edits. Skipping
  // while a save is in flight also avoids the half-saved refetch (new groups,
  // old enabled) flipping the switch back mid-save.
  useEffect(() => {
    if (savingRef.current) return
    setEnabled(defaultEnabled)
    setGroups(parseGroups(defaultGroupsRaw))
  }, [defaultEnabled, defaultGroupsRaw])

  const groupsQuery = useQuery({
    queryKey: ['system-groups'],
    queryFn: getGroups,
  })

  const groupSelectOptions = useMemo(
    () =>
      (groupsQuery.data?.data ?? []).map((group) => ({
        value: group,
        label: group,
      })),
    [groupsQuery.data]
  )

  const groupsJson = serializeGroups(groups)
  const defaultGroupsJson = serializeGroups(parseGroups(defaultGroupsRaw))
  const dirty = enabled !== defaultEnabled || groupsJson !== defaultGroupsJson

  const handleSave = async () => {
    if (!dirty) {
      toast.info(t('No changes'))
      return
    }
    setSaving(true)
    savingRef.current = true
    try {
      // Groups first, switch last: enabling can never point at stale groups.
      if (groupsJson !== defaultGroupsJson) {
        const saved = await updateOption.mutateAsync({
          key: 'log_timing_optimization.groups',
          value: groupsJson,
        })
        if (!saved.success) return
      }
      if (enabled !== defaultEnabled) {
        await updateOption.mutateAsync({
          key: 'log_timing_optimization.enabled',
          value: String(enabled),
        })
      }
    } catch {
      toast.error(t('Failed to save'))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return (
    <SettingsSection title={t('Log Display')}>
      <Alert>
        <AlertDescription className='text-xs'>
          {t(
            'Display-only optimization: the first-token time and duration shown in usage logs are rewritten for the selected groups. Billing, token counts and upstream responses are unaffected.'
          )}
        </AlertDescription>
      </Alert>

      <SettingsSwitchField
        checked={enabled}
        onCheckedChange={setEnabled}
        label={t('Enable log display optimization')}
        description={t(
          'Rewrite the first-token time and duration of consume and error logs written by the selected groups.'
        )}
      />

      <div className='grid gap-1.5'>
        <Label htmlFor='log-timing-optimization-groups'>
          {t('Applicable Groups')}
        </Label>
        <MultiSelect
          id='log-timing-optimization-groups'
          options={groupSelectOptions}
          selected={groups}
          onChange={setGroups}
          placeholder={t('Select groups')}
          emptyText={t('No matching items')}
          disabled={!enabled}
        />
        <p className='text-muted-foreground text-xs'>
          {t(
            'Only groups selected here are optimized. When no group is selected, logs are left unchanged.'
          )}
        </p>
      </div>

      <Separator />

      <div className='space-y-2'>
        <h4 className='text-sm font-semibold'>
          {t('First-token display rules')}
        </h4>
        <ul className='text-muted-foreground list-disc space-y-1 pl-5 text-xs'>
          <li>
            {t(
              'Below 5s: show the real first-token time. 5s to 15s: show a random value between 2/3 of the real time and the real time.'
            )}
          </li>
          <li>
            {t(
              'Displays as 15.0s: show a random value between 5s and 10s. Displays above 15.0s: show a random value between 8s and 15s.'
            )}
          </li>
          <li>
            {t(
              'Duration is shortened by the hidden first-token time and never falls below the displayed first token.'
            )}
          </li>
        </ul>
      </div>

      <SettingsPageFormActions
        onSave={handleSave}
        isSaving={saving}
        saveLabel='Save log display settings'
      />
    </SettingsSection>
  )
}
