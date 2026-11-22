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
import { Loader2, RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { DISABLED_ROW_MOBILE } from '@/components/data-table'
import { QuotaRing } from '@/components/quota-ring'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { toIntlLocale } from '@/i18n/languages'
import { cn } from '@/lib/utils'

import {
  formatLastActivity,
  formatResetCountdown,
  getCommandGoStatusConfig,
} from '../../lib/command-go'
import type { CommandGoAccount } from '../../types'

interface CommandGoAccountCardProps {
  account: CommandGoAccount
  selected: boolean
  refreshing: boolean
  refreshDisabled?: boolean
  onSelectedChange: (checked: boolean) => void
  onRefresh: () => void
}

const COUNTDOWN_TICK_MS = 30_000

export function CommandGoAccountCard(props: CommandGoAccountCardProps) {
  const { t, i18n } = useTranslation()
  const [now, setNow] = useState(() => Date.now())
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const account = props.account
  const statusConfig = getCommandGoStatusConfig(account.status)
  const isNoQuota = account.status === 'no_quota'

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), COUNTDOWN_TICK_MS)
    return () => clearInterval(timer)
  }, [])

  const formatActivity = (timestamp: number) =>
    t(formatLastActivity(timestamp, locale))

  const windowResetText = (resetAt: number) => {
    const countdown = formatResetCountdown(resetAt, now, {
      second: t('s'),
      minute: t('m'),
      hour: t('h'),
      day: t('d'),
    })
    return countdown ? t('Resets in {{time}}', { time: countdown }) : undefined
  }

  return (
    <div
      className={cn(
        'rounded-lg border p-3 transition-colors',
        isNoQuota && DISABLED_ROW_MOBILE
      )}
    >
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div className='flex min-w-0 items-start gap-2'>
          <Checkbox
            className='mt-1'
            checked={props.selected}
            onCheckedChange={(checked) => props.onSelectedChange(checked)}
            aria-label={t('Select {{name}}', { name: account.name })}
          />
          <div className='min-w-0'>
            <div className='flex min-w-0 flex-wrap items-center gap-2'>
              <span className='truncate text-sm font-medium'>
                {account.name}
              </span>
              <StatusBadge
                label={t(statusConfig.labelKey)}
                variant={statusConfig.variant}
                size='sm'
                copyable={false}
              />
            </div>
            <div className='text-muted-foreground mt-1 font-mono text-xs'>
              {account.key_mask}
            </div>
            {account.last_error && (
              <div className='text-destructive mt-1 line-clamp-2 text-xs'>
                {account.last_error}
              </div>
            )}
          </div>
        </div>

        <Button
          variant='ghost'
          size='icon-sm'
          onClick={props.onRefresh}
          disabled={props.refreshing || props.refreshDisabled}
          aria-label={t('Refresh')}
        >
          {props.refreshing ? (
            <Loader2 className='size-4 animate-spin' />
          ) : (
            <RefreshCw className='size-4' />
          )}
        </Button>
      </div>

      <div className='text-muted-foreground mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px]'>
        <span>
          {t('Consecutive Failures')}:{' '}
          <span className='tabular-nums'>{account.consecutive_failures}</span>
        </span>
        <span>
          {t('Last Used')}: {formatActivity(account.last_used_at)}
        </span>
        <span>
          {t('Last Refreshed')}: {formatActivity(account.last_refreshed_at)}
        </span>
        {account.plan_id && (
          <span>
            {t('Plan')}: {account.plan_id}
          </span>
        )}
      </div>

      <div className='mt-3 flex flex-wrap justify-around gap-3'>
        <QuotaRing
          label={t('5-Hour Window')}
          used={account.windows.five_hour.used}
          cap={account.windows.five_hour.cap}
          percent={account.windows.five_hour.percent}
          resetText={windowResetText(account.windows.five_hour.reset_at)}
        />
        <QuotaRing
          label={t('Weekly Window')}
          used={account.windows.weekly.used}
          cap={account.windows.weekly.cap}
          percent={account.windows.weekly.percent}
          resetText={windowResetText(account.windows.weekly.reset_at)}
        />
        <QuotaRing
          label={t('Monthly Window')}
          used={account.windows.monthly.used}
          cap={account.windows.monthly.cap}
          percent={account.windows.monthly.percent}
          resetText={windowResetText(account.windows.monthly.reset_at)}
        />
      </div>
    </div>
  )
}
