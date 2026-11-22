import { useQueryClient } from '@tanstack/react-query'
import { Loader2, RefreshCw, DollarSign } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  CodeBlock,
  CodeBlockCopyButton,
} from '@/components/ai-elements/code-block'
import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { IconBadge } from '@/components/ui/icon-badge'
import { formatCurrencyFromUSD } from '@/lib/currency'
import { formatTimestampToDate } from '@/lib/format'

import {
  getCommandcodeQuota,
  getCodexUsage,
  updateChannelBalance,
  type CommandcodeQuotaResponse,
} from '../../api'
import { CHANNEL_TYPE_COMMAND } from '../../constants'
import { channelsQueryKeys } from '../../lib'
import type { OpencodeUsageSnapshot } from '../../types'
import { useChannels } from '../channels-provider'
import {
  CodexUsageDialog,
  type CodexUsageDialogData,
} from './codex-usage-dialog'
import { CommandcodeQuotaDialog } from './commandcode-quota-dialog'

type BalanceQueryDialogProps = {
  initialRawResponse?: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function BalanceQueryDialog(props: BalanceQueryDialogProps) {
  const { t } = useTranslation()
  const { currentRow, setCurrentRow } = useChannels()
  const queryClient = useQueryClient()
  const [isQuerying, setIsQuerying] = useState(false)
  const [balance, setBalance] = useState<number | null>(null)
  const [balanceUpdatedTime, setBalanceUpdatedTime] = useState<number | null>(
    null
  )
  const [rawResponse, setRawResponse] = useState<string | null>(
    props.initialRawResponse ?? null
  )
  const [opencodeUsage, setOpencodeUsage] =
    useState<OpencodeUsageSnapshot | null>(null)
  const [codexUsageResponse, setCodexUsageResponse] =
    useState<CodexUsageDialogData | null>(null)
  const [commandcodeQuotaResponse, setCommandcodeQuotaResponse] =
    useState<CommandcodeQuotaResponse | null>(null)

  const isCodex = currentRow?.type === 57
  // Only type 62 exposes /commandcode/quota; type 63 uses the account pool.
  const isCommand = currentRow?.type === CHANNEL_TYPE_COMMAND

  const handleQueryCodexUsage = async () => {
    const row = currentRow
    if (!row) return
    setIsQuerying(true)
    try {
      const res = await getCodexUsage(row.id)
      if (!res.success) {
        throw new Error(res.message || t('Failed to fetch usage'))
      }
      setCodexUsageResponse(res)
    } catch (error: unknown) {
      toast.error(
        error instanceof Error ? error.message : t('Failed to fetch usage')
      )
    } finally {
      setIsQuerying(false)
    }
  }

  const handleQueryCommandcodeQuota = async () => {
    const row = currentRow
    if (!row) return
    setIsQuerying(true)
    try {
      const res = await getCommandcodeQuota(row.id)
      if (!res.success) {
        throw new Error(res.message || t('Failed to fetch usage'))
      }
      setCommandcodeQuotaResponse(res)
    } catch (error: unknown) {
      toast.error(
        error instanceof Error ? error.message : t('Failed to fetch usage')
      )
    } finally {
      setIsQuerying(false)
    }
  }

  useEffect(() => {
    if (!isCodex) return
    if (!props.open) return
    handleQueryCodexUsage()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.open, isCodex])

  useEffect(() => {
    if (!isCommand) return
    if (!props.open) return
    handleQueryCommandcodeQuota()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.open, isCommand])

  if (!currentRow) return null

  const handleQueryBalance = async () => {
    setIsQuerying(true)
    try {
      const response = await updateChannelBalance(currentRow.id)
      if (response.success && response.usage) {
        setOpencodeUsage(response.usage)
        setBalanceUpdatedTime(
          response.usage.calibration.last_success_at ||
            Math.floor(Date.now() / 1000)
        )
        toast.success(t('Balance updated successfully'))
        await queryClient.invalidateQueries({
          queryKey: channelsQueryKeys.lists(),
        })
        await queryClient.invalidateQueries({
          queryKey: ['code-provider-usage', currentRow.id],
        })
        setRawResponse(null)
      } else if (response.success && response.balance !== undefined) {
        const newBalance = response.balance
        const now = Math.floor(Date.now() / 1000)

        setBalance(newBalance)
        setBalanceUpdatedTime(now)
        toast.success(t('Balance updated successfully'))

        // Update currentRow immediately with new balance and timestamp
        setCurrentRow({
          ...currentRow,
          balance: newBalance,
          balance_updated_time: now,
        })

        // Invalidate queries to refresh the table
        await queryClient.invalidateQueries({
          queryKey: channelsQueryKeys.lists(),
        })
        setRawResponse(null)
      } else if (response.success && response.raw_response !== undefined) {
        setRawResponse(response.raw_response)
      } else {
        toast.error(response.message || t('Failed to query balance'))
      }
    } catch (error: unknown) {
      toast.error(
        error instanceof Error ? error.message : t('Failed to query balance')
      )
    } finally {
      setIsQuerying(false)
    }
  }

  const handleClose = () => {
    setBalance(null)
    setBalanceUpdatedTime(null)
    setRawResponse(null)
    setOpencodeUsage(null)
    setCodexUsageResponse(null)
    setCommandcodeQuotaResponse(null)
    props.onOpenChange(false)
  }

  const formatBalance = (bal: number) =>
    formatCurrencyFromUSD(bal, {
      digitsLarge: 2,
      digitsSmall: 4,
      abbreviate: false,
    })

  const formatDate = (timestamp: number) => {
    if (!timestamp) return 'Never'
    return formatTimestampToDate(timestamp)
  }

  if (isCodex) {
    return (
      <CodexUsageDialog
        open={props.open}
        onOpenChange={(v) => {
          if (!v) handleClose()
        }}
        channelName={currentRow.name}
        channelId={currentRow.id}
        response={codexUsageResponse}
        onRefresh={handleQueryCodexUsage}
        isRefreshing={isQuerying}
      />
    )
  }

  if (isCommand) {
    return (
      <CommandcodeQuotaDialog
        open={props.open}
        onOpenChange={(v) => {
          if (!v) handleClose()
        }}
        channelName={currentRow.name}
        channelId={currentRow.id}
        response={commandcodeQuotaResponse}
        onRefresh={handleQueryCommandcodeQuota}
        isRefreshing={isQuerying}
      />
    )
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={handleClose}
      title={t('Query Balance')}
      description={
        <>
          {t('Update balance for:')}
          <strong>{currentRow.name}</strong>
        </>
      }
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <Button variant='outline' onClick={handleClose} disabled={isQuerying}>
          {t('Close')}
        </Button>
      }
    >
      <div className='space-y-4 py-4'>
        {rawResponse !== null && (
          <>
            <Alert>
              <AlertTitle>{t('Balance response not recognized')}</AlertTitle>
              <AlertDescription>
                {t(
                  'The upstream response is valid JSON, but it does not match the OpenAI credit_summary format. The channel balance was not updated.'
                )}
              </AlertDescription>
            </Alert>
            <CodeBlock
              code={rawResponse}
              language='json'
              maxExpandedLines={24}
              showLineNumbers
              title={t('Upstream JSON response')}
            >
              <CodeBlockCopyButton />
            </CodeBlock>
          </>
        )}
        {rawResponse === null && opencodeUsage && (
          <div className='space-y-3'>
            {(
              [
                [t('5 hours'), opencodeUsage.five_hour],
                [t('Weekly'), opencodeUsage.week],
                [t('Monthly'), opencodeUsage.month],
              ] as const
            ).map(([label, window]) => {
              const percent = Math.min(100, Math.max(0, window.percent))
              return (
                <div className='space-y-2 rounded-lg border p-4' key={label}>
                  <div className='flex items-center justify-between gap-3 text-sm'>
                    <span className='font-medium'>{label}</span>
                    <span className='text-muted-foreground tabular-nums'>
                      {percent.toFixed(2)}%
                    </span>
                  </div>
                  <div className='bg-muted h-2 overflow-hidden rounded-full'>
                    <div
                      className='bg-primary h-full transition-all'
                      style={{ width: `${percent}%` }}
                    />
                  </div>
                  <div className='text-muted-foreground flex flex-wrap justify-between gap-x-4 gap-y-1 text-xs'>
                    <span>
                      {t('Used:')} {formatBalance(window.used_usd)} /{' '}
                      {formatBalance(window.limit_usd)}
                    </span>
                    <span>
                      {t('Remaining:')} {formatBalance(window.remaining_usd)}
                    </span>
                  </div>
                  <div className='text-muted-foreground text-xs'>
                    {t('Reset at:')} {formatDate(window.reset_at)}
                  </div>
                </div>
              )
            })}
            <div className='text-muted-foreground text-xs'>
              {t('Last updated:')}{' '}
              {formatDate(
                opencodeUsage.calibration.last_success_at ||
                  balanceUpdatedTime ||
                  currentRow.balance_updated_time
              )}
            </div>
          </div>
        )}
        {rawResponse === null && !opencodeUsage && (
          <>
            {/* Current Balance Display */}
            <div className='bg-muted/50 rounded-lg border p-4'>
              <div className='text-muted-foreground mb-2 flex items-center gap-2 text-sm'>
                <IconBadge tone='success' size='xs'>
                  <DollarSign />
                </IconBadge>
                <span>{t('Current Balance')}</span>
              </div>
              <div className='text-2xl font-bold'>
                {balance !== null
                  ? formatBalance(balance)
                  : formatBalance(currentRow.balance)}
              </div>
              <div className='text-muted-foreground mt-2 text-xs'>
                {t('Last updated:')}{' '}
                {formatDate(
                  balanceUpdatedTime ?? currentRow.balance_updated_time
                )}
              </div>
            </div>
          </>
        )}

        {/* Balance Update Button */}
        <Button
          className='w-full'
          onClick={handleQueryBalance}
          disabled={isQuerying}
        >
          {isQuerying && <Loader2 className='mr-2 h-4 w-4 animate-spin' />}
          {!isQuerying && <RefreshCw className='mr-2 h-4 w-4' />}
          {isQuerying ? t('Querying...') : t('Update Balance')}
        </Button>
      </div>
    </Dialog>
  )
}
