import {
  AlertTriangle,
  Check,
  ChevronDown,
  ChevronUp,
  Copy,
  RefreshCw,
} from 'lucide-react'
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
import { type ReactNode, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { StatusBadge, type StatusBadgeProps } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from '@/components/ui/empty'
import { Progress } from '@/components/ui/progress'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import dayjs from '@/lib/dayjs'
import { formatDateTimeStr } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { CommandcodeQuotaResponse, CommandcodeWindow } from '../../api'

type CommandcodeQuotaDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelName?: string
  channelId?: number
  channelDisplayName?: string
  channelDisplayId?: string
  response: CommandcodeQuotaResponse | null
  onRefresh?: () => void | Promise<void>
  isRefreshing?: boolean
}

function clampPercent(value: number): number {
  if (!Number.isFinite(value)) {
    return 0
  }
  return Math.max(0, Math.min(100, value))
}

function formatCredits(value?: number | null): string {
  const n = Number(value)
  if (value == null || !Number.isFinite(n)) {
    return '-'
  }
  return n.toLocaleString(undefined, { maximumFractionDigits: 4 })
}

function formatEpochMs(ms?: number | null): string {
  const v = Number(ms)
  if (!ms || !Number.isFinite(v) || v <= 0) {
    return '-'
  }
  return formatDateTimeStr(dayjs(v).toDate())
}

function formatTimeLeftMs(
  ms?: number | null,
  t: (key: string) => string = (k) => k
): string {
  const v = Number(ms)
  if (!ms || !Number.isFinite(v) || v <= 0) {
    return '-'
  }
  const secondsLeft = dayjs(v).diff(dayjs(), 'second')
  if (secondsLeft <= 0) {
    return t('Expired')
  }
  const days = Math.floor(secondsLeft / (24 * 60 * 60))
  if (days > 0) {
    const hours = Math.floor((secondsLeft % (24 * 60 * 60)) / 3600)
    return `${days} ${t('days')} ${hours}${t('h')}`
  }
  const hours = Math.floor(secondsLeft / 3600)
  if (hours > 0) {
    return `${hours}${t('h')} ${Math.floor((secondsLeft % 3600) / 60)}${t('m')}`
  }
  const minutes = Math.floor(secondsLeft / 60)
  if (minutes > 0) {
    return `${minutes}${t('m')} ${secondsLeft % 60}${t('s')}`
  }
  return `${secondsLeft}${t('s')}`
}

const percentTextClassName: Partial<
  Record<NonNullable<StatusBadgeProps['variant']>, string>
> = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-destructive',
  info: 'text-info',
  neutral: 'text-muted-foreground',
}

function windowVariant(
  percent: number,
  exceeded: boolean
): NonNullable<StatusBadgeProps['variant']> {
  if (exceeded || percent >= 95) {
    return 'danger'
  }
  if (percent >= 80) {
    return 'warning'
  }
  return 'info'
}

function QuotaWindowCard(props: {
  title: string
  quotaWindow: CommandcodeWindow | null
  fallbackResetMs?: number
  resetLabel?: string
}) {
  const { t } = useTranslation()
  const quotaWindow = props.quotaWindow
  const hasData = !!quotaWindow && Number(quotaWindow.cap) > 0
  const used = quotaWindow ? Number(quotaWindow.used) || 0 : 0
  const cap = quotaWindow ? Number(quotaWindow.cap) || 0 : 0
  const percent = hasData ? clampPercent((used / cap) * 100) : 0
  const variant = windowVariant(percent, Boolean(quotaWindow?.exceeded))
  const resetAt = quotaWindow?.reset_at || props.fallbackResetMs || 0

  return (
    <Card size='sm' className='gap-0 py-0'>
      <CardHeader className='p-3 pb-2'>
        <div className='flex items-start justify-between gap-3'>
          <div className='min-w-0'>
            <CardTitle className='text-sm font-semibold'>
              {props.title}
            </CardTitle>
            <CardDescription className='mt-1 text-xs'>
              {hasData ? `${formatCredits(used)} / ${formatCredits(cap)}` : '-'}
            </CardDescription>
          </div>
          <div className='shrink-0 text-right'>
            <div
              className={cn(
                'text-xl leading-none font-semibold tabular-nums',
                percentTextClassName[variant] ?? 'text-muted-foreground'
              )}
            >
              {hasData ? `${Math.round(percent)}%` : '-'}
            </div>
            <div className='text-muted-foreground mt-1 text-[11px]'>
              {t('Used')}
            </div>
          </div>
        </div>
      </CardHeader>
      <CardContent className='p-3 pt-0'>
        {hasData ? (
          <Progress
            value={percent}
            aria-label={`${props.title} usage: ${Math.round(percent)}%`}
            className='mt-1'
          />
        ) : (
          <div className='text-muted-foreground mt-1 text-sm'>-</div>
        )}
        {quotaWindow?.exceeded ? (
          <div className='text-destructive mt-2 text-xs font-medium'>
            {t('Window exceeded')}
          </div>
        ) : null}
        <div className='mt-3 grid grid-cols-1 gap-2 text-xs sm:grid-cols-2'>
          <div className='min-w-0'>
            <div className='text-muted-foreground text-[11px]'>
              {t('Reset at:')}
            </div>
            <div className='break-all tabular-nums'>
              {formatEpochMs(resetAt)}
            </div>
          </div>
          <div className='min-w-0 sm:text-right'>
            <div className='text-muted-foreground text-[11px]'>
              {props.resetLabel ?? t('Resets in:')}
            </div>
            <div className='tabular-nums'>{formatTimeLeftMs(resetAt, t)}</div>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}

function SectionHeading(props: {
  title: string
  description?: string
  children?: ReactNode
}) {
  return (
    <div className='flex flex-wrap items-start justify-between gap-3'>
      <div className='min-w-0'>
        <div className='text-sm font-semibold'>{props.title}</div>
        {props.description ? (
          <div className='text-muted-foreground mt-1 text-xs leading-5'>
            {props.description}
          </div>
        ) : null}
      </div>
      {props.children ? (
        <div className='flex shrink-0 flex-wrap items-center gap-2'>
          {props.children}
        </div>
      ) : null}
    </div>
  )
}

function InfoField(props: {
  label: string
  value?: string | null
  mono?: boolean
  copyable?: boolean
  className?: string
}) {
  const { t } = useTranslation()
  const { copyToClipboard, copiedText } = useCopyToClipboard({ notify: false })
  const text = props.value?.trim() || ''
  const hasCopied = copiedText === text

  return (
    <div
      className={cn(
        'bg-background ring-border/60 min-w-0 rounded-lg p-3 ring-1',
        props.className
      )}
    >
      <div className='text-muted-foreground text-[11px] font-medium'>
        {props.label}
      </div>
      <div className='mt-1 flex min-w-0 items-start justify-between gap-2'>
        <span
          className={cn(
            'min-w-0 flex-1 text-xs leading-5 break-all',
            props.mono && 'font-mono tabular-nums'
          )}
        >
          {text || '-'}
        </span>
        {props.copyable !== false && text ? (
          <Button
            type='button'
            variant='ghost'
            size='icon-xs'
            aria-label={t('Copy')}
            onClick={() => copyToClipboard(text)}
          >
            {hasCopied ? <Check className='text-success' /> : <Copy />}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

function UsageSummaryItem(props: { label: string; value: string }) {
  return (
    <div className='bg-background ring-border/60 min-w-0 rounded-lg p-3 ring-1'>
      <div className='text-muted-foreground text-[11px] font-medium'>
        {props.label}
      </div>
      <div className='mt-1 text-sm font-semibold tabular-nums'>
        {props.value}
      </div>
    </div>
  )
}

export function CommandcodeQuotaDialog({
  open,
  onOpenChange,
  channelName,
  channelId,
  channelDisplayName,
  channelDisplayId,
  response,
  onRefresh,
  isRefreshing,
}: CommandcodeQuotaDialogProps) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })
  const [showRawJson, setShowRawJson] = useState(false)

  const report = useMemo(() => response?.data ?? null, [response?.data])
  const credits = report?.credits ?? null
  const plan = report?.plan ?? null
  const usage = report?.usage ?? null
  const account = report?.account ?? null

  const channelLabelName = channelDisplayName ?? channelName ?? '-'
  let channelLabelId = ''
  if (channelDisplayId != null) {
    channelLabelId = ` (#${channelDisplayId})`
  } else if (channelId) {
    channelLabelId = ` (#${channelId})`
  }
  const channelLabel = `${channelLabelName}${channelLabelId}`

  const errorMessage =
    response?.success === false
      ? response?.message?.trim() || t('Failed to fetch usage')
      : ''

  const hasLimitState = Boolean(credits?.limited || credits?.exceeded)
  const monthlyWindow = useMemo<CommandcodeWindow | null>(() => {
    if (!plan || plan.monthly_credits == null || !credits) {
      return null
    }
    const cap = Number(plan.monthly_credits)
    const remaining = Number(credits.monthly_credits)
    if (!Number.isFinite(cap) || cap <= 0 || !Number.isFinite(remaining)) {
      return null
    }
    const used = Math.max(0, cap - remaining)
    return {
      used,
      cap,
      exceeded: remaining <= 0,
      reset_at: plan.current_period_end || 0,
    }
  }, [plan, credits])

  const rawJsonText = useMemo(() => {
    if (!response) {
      return ''
    }
    try {
      return JSON.stringify(response, null, 2)
    } catch {
      return String(response?.data ?? '')
    }
  }, [response])

  const handleDialogOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      setShowRawJson(false)
    }
    onOpenChange(nextOpen)
  }

  const hasAnySection = Boolean(
    credits || plan || usage || account || (report?.failures?.length ?? 0) > 0
  )

  return (
    <Dialog
      open={open}
      onOpenChange={handleDialogOpenChange}
      title={t('Command Quota & Usage')}
      contentClassName='sm:max-w-[900px]'
      titleClassName='flex items-center gap-2'
      contentHeight='auto'
      bodyClassName='flex flex-col gap-4'
      footer={
        <Button
          type='button'
          variant='outline'
          onClick={() => handleDialogOpenChange(false)}
        >
          {t('Close')}
        </Button>
      }
    >
      <div className='flex flex-col gap-4'>
        {errorMessage && (
          <div className='rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-950/30 dark:text-red-400'>
            {errorMessage}
          </div>
        )}

        {!hasAnySection && !errorMessage ? (
          <Empty className='min-h-32 border'>
            <EmptyHeader>
              <EmptyTitle>{t('No quota data')}</EmptyTitle>
              <EmptyDescription>
                {t('Upstream did not return quota data.')}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : null}

        <Card size='sm' className='bg-muted/30 gap-0 py-0'>
          <CardHeader className='p-4 pb-2'>
            <CardTitle className='text-muted-foreground text-xs font-medium'>
              {t('Account Status')}
            </CardTitle>
            {onRefresh ? (
              <CardAction>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={onRefresh}
                  disabled={Boolean(isRefreshing)}
                >
                  <RefreshCw data-icon='inline-start' />
                  {t('Refresh')}
                </Button>
              </CardAction>
            ) : null}
          </CardHeader>
          <CardContent className='p-4 pt-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <StatusBadge
                label={plan?.name || t('Unknown')}
                variant={plan?.name ? 'blue' : 'neutral'}
                copyable={false}
              />
              {credits ? (
                hasLimitState ? (
                  <StatusBadge
                    label={t('Limited')}
                    variant='danger'
                    copyable={false}
                  />
                ) : (
                  <StatusBadge
                    label={t('Available')}
                    variant='success'
                    copyable={false}
                  />
                )
              ) : null}
              {credits?.below_threshold ? (
                <StatusBadge
                  label={t('Below threshold')}
                  variant='warning'
                  copyable={false}
                />
              ) : null}
              {(report?.failures?.length ?? 0) > 0 ? (
                <StatusBadge
                  label={`${t('Partial failures')} (${report?.failures?.length})`}
                  variant='warning'
                  copyable={false}
                />
              ) : null}
            </div>
            <div className='mt-4 grid grid-cols-1 gap-3 md:grid-cols-2'>
              <InfoField
                label={t('Account')}
                value={account ? account.name || account.user_name : null}
                copyable={false}
              />
              <InfoField
                label={t('Channel')}
                value={channelLabel}
                copyable={false}
              />
              <InfoField label='User ID' value={account?.id} mono />
              <InfoField
                label={t('Username')}
                value={account?.user_name}
                mono
              />
            </div>
          </CardContent>
        </Card>

        {credits ? (
          <div className='flex flex-col gap-3'>
            <SectionHeading
              title={t('Usage Windows')}
              description={t('Rolling usage windows reported by upstream.')}
            />
            <div className='grid grid-cols-1 gap-3 md:grid-cols-2'>
              <QuotaWindowCard
                title={t('5-Hour Window')}
                quotaWindow={credits.five_hour ?? null}
              />
              <QuotaWindowCard
                title={t('Weekly Window')}
                quotaWindow={credits.weekly ?? null}
              />
            </div>
            {monthlyWindow ? (
              <QuotaWindowCard
                title={t('Monthly Window')}
                quotaWindow={monthlyWindow}
              />
            ) : null}
          </div>
        ) : null}

        {credits ? (
          <div className='flex flex-col gap-3'>
            <SectionHeading
              title={t('Credits Balance')}
              description={t('Remaining credits on this account.')}
            />
            <div className='grid grid-cols-1 gap-3 sm:grid-cols-3'>
              <UsageSummaryItem
                label={t('Monthly Credits')}
                value={formatCredits(credits.monthly_credits)}
              />
              <UsageSummaryItem
                label={t('Purchased Credits')}
                value={formatCredits(credits.purchased_credits)}
              />
              <UsageSummaryItem
                label={t('Free Credits')}
                value={formatCredits(credits.free_credits)}
              />
            </div>
          </div>
        ) : null}

        {plan ? (
          <div className='flex flex-col gap-3'>
            <SectionHeading
              title={t('Plan & Period')}
              description={plan.plan_id || undefined}
            >
              {plan.status ? (
                <StatusBadge
                  label={plan.status}
                  variant={plan.status === 'active' ? 'success' : 'neutral'}
                  copyable={false}
                />
              ) : null}
            </SectionHeading>
            {plan.cancel_at_period_end ? (
              <Alert>
                <AlertTriangle />
                <AlertTitle>{t('Cancels at period end')}</AlertTitle>
                <AlertDescription>
                  {t(
                    'This subscription will not renew after the current period ends.'
                  )}
                </AlertDescription>
              </Alert>
            ) : null}
            <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
              <InfoField
                label={t('Period start')}
                value={formatEpochMs(plan.current_period_start)}
                copyable={false}
              />
              <InfoField
                label={t('Period end')}
                value={formatEpochMs(plan.current_period_end)}
                copyable={false}
              />
              <InfoField
                label={t('Monthly limit')}
                value={
                  plan.monthly_credits != null
                    ? `${formatCredits(plan.monthly_credits)} ${t('credits')}`
                    : t('Unknown plan')
                }
                copyable={false}
              />
              <InfoField
                label={t('Resets in:')}
                value={formatTimeLeftMs(plan.current_period_end, t)}
                copyable={false}
              />
            </div>
          </div>
        ) : null}

        {usage ? (
          <div className='flex flex-col gap-3'>
            <SectionHeading
              title={t('Usage Summary')}
              description={
                usage.period_basis
                  ? `${t('Period')}: ${usage.period_basis}`
                  : undefined
              }
            />
            <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
              <UsageSummaryItem
                label={t('Total requests')}
                value={usage.total_count.toLocaleString()}
              />
              <UsageSummaryItem
                label={t('Completed')}
                value={usage.completed_count.toLocaleString()}
              />
              <UsageSummaryItem
                label={t('Failed')}
                value={usage.failed_count.toLocaleString()}
              />
              <UsageSummaryItem
                label={t('Success rate')}
                value={`${usage.success_rate}%`}
              />
              <UsageSummaryItem
                label={t('Tokens in')}
                value={usage.total_tokens_in.toLocaleString()}
              />
              <UsageSummaryItem
                label={t('Tokens out')}
                value={usage.total_tokens_out.toLocaleString()}
              />
              <UsageSummaryItem
                label={t('Total credits')}
                value={formatCredits(usage.total_credits)}
              />
              <UsageSummaryItem
                label={t('Avg cost')}
                value={formatCredits(usage.average_cost)}
              />
            </div>
          </div>
        ) : null}

        {(report?.failures?.length ?? 0) > 0 ? (
          <Alert>
            <AlertTriangle />
            <AlertTitle>{t('Some upstream endpoints failed')}</AlertTitle>
            <AlertDescription>
              <ul className='mt-1 list-disc space-y-1 pl-4'>
                {report?.failures?.map((failure) => (
                  <li key={failure} className='break-all'>
                    {failure}
                  </li>
                ))}
              </ul>
            </AlertDescription>
          </Alert>
        ) : null}

        <Collapsible
          open={showRawJson}
          onOpenChange={setShowRawJson}
          className='rounded-lg border'
        >
          <CollapsibleTrigger
            render={
              <button
                type='button'
                className='hover:bg-muted/40 flex w-full items-center justify-between gap-2 p-3 transition-colors'
                aria-expanded={showRawJson}
              />
            }
          >
            <div className='text-sm font-medium'>{t('Raw JSON')}</div>
            {showRawJson ? (
              <ChevronUp className='text-muted-foreground h-4 w-4' />
            ) : (
              <ChevronDown className='text-muted-foreground h-4 w-4' />
            )}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <>
              <div className='flex justify-end border-t px-3 py-2'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={() => copyToClipboard(rawJsonText)}
                  disabled={!rawJsonText}
                >
                  {copiedText === rawJsonText ? (
                    <Check data-icon='inline-start' className='text-success' />
                  ) : (
                    <Copy data-icon='inline-start' />
                  )}
                  {t('Copy')}
                </Button>
              </div>
              <ScrollArea className='max-h-[50vh]'>
                <pre className='bg-muted/30 m-0 p-3 text-xs break-words whitespace-pre-wrap'>
                  {rawJsonText || '-'}
                </pre>
              </ScrollArea>
            </>
          </CollapsibleContent>
        </Collapsible>
      </div>
    </Dialog>
  )
}
