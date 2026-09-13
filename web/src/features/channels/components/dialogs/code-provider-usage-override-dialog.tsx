/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useQueryClient } from '@tanstack/react-query'
import { AlertCircle, Loader2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import {
  getCommandUsage,
  getOpencodeUsage,
  setCommandUsageOverride,
  setOpencodeUsageOverride,
} from '../../api'
import { CHANNEL_TYPE_COMMAND, CHANNEL_TYPE_OPENCODE } from '../../constants'
import type {
  CodeProviderUsageOverrideRequest,
  OpencodeUsageWindow,
} from '../../types'
import { useChannels } from '../channels-provider'

type UsageField =
  | 'five_hour_used_usd'
  | 'week_used_usd'
  | 'month_used_usd'
  | 'pool_used_usd'
  | 'peak_pool_used_usd'
  | 'off_peak_pool_used_usd'
  | 'flash_pool_used_usd'
  | 'v41_pool_used_usd'
  | 'pro_pool_used_usd'

type UsageForm = Record<UsageField, string>

const emptyUsageForm: UsageForm = {
  five_hour_used_usd: '0',
  week_used_usd: '0',
  month_used_usd: '0',
  pool_used_usd: '0',
  peak_pool_used_usd: '0',
  off_peak_pool_used_usd: '0',
  flash_pool_used_usd: '0',
  v41_pool_used_usd: '0',
  pro_pool_used_usd: '0',
}

function formatUsageValue(window?: OpencodeUsageWindow) {
  return window ? String(window.used_usd) : '0'
}

function getUsageWindowValue(data: {
  five_hour: OpencodeUsageWindow
  week: OpencodeUsageWindow
  month: OpencodeUsageWindow
  pool: OpencodeUsageWindow
  peak_pool?: OpencodeUsageWindow
  off_peak_pool?: OpencodeUsageWindow
  flash_pool?: OpencodeUsageWindow
  v41_pool?: OpencodeUsageWindow
  pro_pool?: OpencodeUsageWindow
}): UsageForm {
  return {
    five_hour_used_usd: formatUsageValue(data.five_hour),
    week_used_usd: formatUsageValue(data.week),
    month_used_usd: formatUsageValue(data.month),
    pool_used_usd: formatUsageValue(data.pool),
    peak_pool_used_usd: formatUsageValue(data.peak_pool),
    off_peak_pool_used_usd: formatUsageValue(data.off_peak_pool),
    flash_pool_used_usd: formatUsageValue(data.flash_pool),
    v41_pool_used_usd: formatUsageValue(data.v41_pool),
    pro_pool_used_usd: formatUsageValue(data.pro_pool),
  }
}

type CodeProviderUsageOverrideDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function CodeProviderUsageOverrideDialog({
  open,
  onOpenChange,
}: CodeProviderUsageOverrideDialogProps) {
  const { t } = useTranslation()
  const { currentRow } = useChannels()
  const queryClient = useQueryClient()
  const [isLoading, setIsLoading] = useState(false)
  const [isSaving, setIsSaving] = useState(false)
  const [errorMessage, setErrorMessage] = useState('')
  const [fields, setFields] = useState<UsageForm>(emptyUsageForm)
  const [splitPools, setSplitPools] = useState(false)
  const [activePools, setActivePools] = useState<{
    flash: boolean
    v41: boolean
    pro: boolean
  }>({ flash: true, v41: false, pro: true })

  const isCodeProvider =
    currentRow?.type === CHANNEL_TYPE_COMMAND ||
    currentRow?.type === CHANNEL_TYPE_OPENCODE
  const isCommand = currentRow?.type === CHANNEL_TYPE_COMMAND

  useEffect(() => {
    if (!open || !currentRow || !isCodeProvider) return

    let cancelled = false
    const loadUsage = async () => {
      setIsLoading(true)
      setErrorMessage('')
      try {
        const response = isCommand
          ? await getCommandUsage(currentRow.id)
          : await getOpencodeUsage(currentRow.id)
        if (!response.success || !response.data) {
          throw new Error(
            response.message || t('Failed to load usage settings')
          )
        }
        if (cancelled) return
        setFields(getUsageWindowValue(response.data))
        setSplitPools(
          Boolean(
            isCommand &&
            'split_pools' in response.data &&
            response.data.split_pools
          )
        )
        setActivePools({
          flash:
            !('flash_pool_active' in response.data) ||
            Boolean(response.data.flash_pool_active),
          v41:
            'v41_pool_active' in response.data &&
            Boolean(response.data.v41_pool_active),
          pro:
            !('pro_pool_active' in response.data) ||
            Boolean(response.data.pro_pool_active),
        })
      } catch (error) {
        if (cancelled) return
        setErrorMessage(
          error instanceof Error
            ? error.message
            : t('Failed to load usage settings')
        )
      } finally {
        if (!cancelled) setIsLoading(false)
      }
    }
    void loadUsage()
    return () => {
      cancelled = true
    }
  }, [currentRow, isCodeProvider, isCommand, open, t])

  if (!currentRow || !isCodeProvider) return null

  const handleFieldChange = (field: UsageField, value: string) => {
    setFields((previous) => ({ ...previous, [field]: value }))
  }

  const parseField = (field: UsageField) => {
    const value = Number(fields[field])
    return Number.isFinite(value) && value >= 0 ? value : null
  }

  const handleSave = async () => {
    const poolFields: UsageField[] = []
    if (isCommand && splitPools) {
      if (activePools.flash) poolFields.push('flash_pool_used_usd')
      if (activePools.v41) poolFields.push('v41_pool_used_usd')
      if (activePools.pro) poolFields.push('pro_pool_used_usd')
    }
    const requiredFields: UsageField[] = [
      'five_hour_used_usd',
      'week_used_usd',
      'month_used_usd',
      'pool_used_usd',
      ...poolFields,
    ]
    const values = new Map<UsageField, number>()
    for (const field of requiredFields) {
      const value = parseField(field)
      if (value === null) {
        setErrorMessage(t('Enter a valid non-negative amount'))
        return
      }
      values.set(field, value)
    }

    const microAmount = (field: UsageField) =>
      Math.round((values.get(field) ?? 0) * 1_000_000)
    if (isCommand && splitPools) {
      const poolSum = poolFields.reduce(
        (sum, field) => sum + microAmount(field),
        0
      )
      if (microAmount('pool_used_usd') !== poolSum) {
        setErrorMessage(t('Total pool must equal the enabled model pool usage'))
        return
      }
    }

    const valueFor = (field: UsageField) => values.get(field) ?? 0
    const data: CodeProviderUsageOverrideRequest = {
      five_hour_used_usd: valueFor('five_hour_used_usd'),
      week_used_usd: valueFor('week_used_usd'),
      month_used_usd: valueFor('month_used_usd'),
    }
    if (isCommand) {
      data.pool_used_usd = valueFor('pool_used_usd')
      if (splitPools) {
        if (activePools.flash) {
          data.flash_pool_used_usd = valueFor('flash_pool_used_usd')
        }
        if (activePools.v41) {
          data.v41_pool_used_usd = valueFor('v41_pool_used_usd')
        }
        if (activePools.pro) {
          data.pro_pool_used_usd = valueFor('pro_pool_used_usd')
        }
      }
    } else {
      data.pool_used_usd = valueFor('pool_used_usd')
    }

    setIsSaving(true)
    setErrorMessage('')
    try {
      const response = isCommand
        ? await setCommandUsageOverride(currentRow.id, data)
        : await setOpencodeUsageOverride(currentRow.id, data)
      if (!response.success) {
        throw new Error(response.message || t('Failed to save usage settings'))
      }
      toast.success(t('Usage settings saved'))
      await queryClient.invalidateQueries({ queryKey: ['code-provider-usage'] })
      onOpenChange(false)
    } catch (error) {
      setErrorMessage(
        error instanceof Error
          ? error.message
          : t('Failed to save usage settings')
      )
    } finally {
      setIsSaving(false)
    }
  }

  const handleClose = () => {
    if (isSaving) return
    setErrorMessage('')
    onOpenChange(false)
  }

  const renderField = (field: UsageField, label: string) => (
    <div className='space-y-2' key={field}>
      <Label htmlFor={`usage-${field}`}>{t(label)}</Label>
      <div className='relative'>
        <Input
          id={`usage-${field}`}
          type='number'
          min='0'
          step='0.000001'
          inputMode='decimal'
          value={fields[field]}
          onChange={(event) => handleFieldChange(field, event.target.value)}
          disabled={isLoading || isSaving}
        />
        <span className='text-muted-foreground pointer-events-none absolute inset-y-0 end-2 flex items-center text-xs'>
          USD
        </span>
      </div>
    </div>
  )

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) =>
        nextOpen ? onOpenChange(true) : handleClose()
      }
      title={t('Fill usage settings')}
      description={
        <>
          {t('Set the upstream usage already consumed for this channel.')}{' '}
          <strong>{currentRow.name}</strong>
        </>
      }
      contentClassName='sm:max-w-2xl'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button variant='outline' onClick={handleClose} disabled={isSaving}>
            {t('Cancel')}
          </Button>
          <Button onClick={handleSave} disabled={isLoading || isSaving}>
            {isSaving ? <Loader2 className='animate-spin' /> : null}
            {isSaving ? t('Saving...') : t('Save settings')}
          </Button>
        </>
      }
    >
      {isLoading ? (
        <div className='flex items-center justify-center py-12'>
          <Loader2 className='text-muted-foreground size-8 animate-spin' />
        </div>
      ) : (
        <div className='space-y-4'>
          {errorMessage ? (
            <Alert variant='destructive'>
              <AlertCircle />
              <AlertDescription>{errorMessage}</AlertDescription>
            </Alert>
          ) : null}
          <div className='grid gap-4 sm:grid-cols-2'>
            {renderField('five_hour_used_usd', '5-hour used')}
            {renderField('week_used_usd', 'Weekly used')}
            {renderField('month_used_usd', 'Monthly used')}
            {renderField('pool_used_usd', 'Total pool used')}
            {isCommand && splitPools && activePools.flash
              ? renderField('flash_pool_used_usd', 'Flash pool used')
              : null}
            {isCommand && splitPools && activePools.v41
              ? renderField('v41_pool_used_usd', '4.1 pool used')
              : null}
            {isCommand && splitPools && activePools.pro
              ? renderField('pro_pool_used_usd', 'Pro pool used')
              : null}
          </div>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Amounts are in USD. Saving resets all usage windows to now; RPM state is preserved.'
            )}
          </p>
        </div>
      )}
    </Dialog>
  )
}
