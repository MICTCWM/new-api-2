import { useQuery } from '@tanstack/react-query'
import { Activity, CircleDashed, RefreshCw, Waves } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardHeader } from '@/components/ui/card'
import { IconBadge } from '@/components/ui/icon-badge'
import { useMediaQuery } from '@/hooks'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getConnectionLogs } from '../api'
import type { ConnectionLogItem } from '../types'
import {
  ConnectionLogsTable,
  type ConnectionViewMode,
} from './connection-logs-table'
import { ConnectionLogsViewToggle } from './connection-logs-view-toggle'

// Token counts and the real cadence strip need frequent, small refreshes, so
// progress polls well below the five-second log refresh. 150ms is fast enough
// that a burst shows up almost as it happens; at 250ms the strip was always a
// quarter second behind the tokens it was supposed to be reporting.
const REFRESH_INTERVAL_MS = 5000
const LIVE_PROGRESS_INTERVAL_MS = 150
const EMPTY_ITEMS: ConnectionLogItem[] = []
const VIEW_MODE_STORAGE_KEY = 'connection_logs_view_mode'

interface ConnectionStatProps {
  label: string
  value: string
  /** Stable hook for tests and for reading the stat out of the card. */
  testId: string
  icon: ReactNode
  tone: 'info' | 'primary'
}

function ConnectionStat(props: ConnectionStatProps) {
  return (
    <div className='bg-card min-w-0 flex-1 px-3 py-2.5 sm:px-4'>
      <div className='text-muted-foreground flex items-center gap-1.5 text-[11px] font-medium'>
        <IconBadge tone={props.tone} size='xs' decorative>
          {props.icon}
        </IconBadge>
        <span className='truncate'>{props.label}</span>
      </div>
      <div
        data-testid={props.testId}
        className='mt-1 truncate font-mono text-base leading-none font-semibold tabular-nums sm:text-lg'
      >
        {props.value}
      </div>
    </div>
  )
}

/** Keeps loading and error states inside the same frame as the log table. */
function ConnectionLogsFrame(props: { role?: 'alert'; children: ReactNode }) {
  return (
    <div
      role={props.role}
      className='text-muted-foreground flex min-h-32 flex-1 items-center justify-center rounded-lg border text-sm'
    >
      {props.children}
    </div>
  )
}

/**
 * Mobile header: one 24px line replaces the desktop title block, the wrapping
 * node/refresh copy and the two tall stat cards, so the list starts roughly
 * eighty pixels higher on a 320px viewport.
 */
function ConnectionLogsMobileHeader(props: {
  nodeName?: string
  refreshing: boolean
  onRefresh: () => void
  viewMode: ConnectionViewMode
  onViewModeChange: (mode: ConnectionViewMode) => void
}) {
  const { t } = useTranslation()
  const refreshSeconds = REFRESH_INTERVAL_MS / 1000

  return (
    <CardHeader
      data-slot='connection-mobile-header'
      className='flex flex-row items-center gap-1.5 border-b px-2 py-1'
    >
      <CircleDashed
        className='text-muted-foreground size-3 shrink-0'
        aria-hidden='true'
      />
      <span className='text-[11px] font-medium whitespace-nowrap'>
        {t('Live connections')}
      </span>
      {props.nodeName ? (
        <span className='text-muted-foreground min-w-0 truncate font-mono text-[10px]'>
          {props.nodeName}
        </span>
      ) : null}
      <span
        className='text-muted-foreground ml-auto shrink-0 font-mono text-[10px] tabular-nums'
        title={t('Auto refresh: {{seconds}}s', { seconds: refreshSeconds })}
      >
        {refreshSeconds}s
      </span>
      <ConnectionLogsViewToggle
        value={props.viewMode}
        onChange={props.onViewModeChange}
      />
      <Button
        variant='ghost'
        size='icon-xs'
        className='size-6'
        aria-label={t('Refresh')}
        onClick={props.onRefresh}
        disabled={props.refreshing}
      >
        <RefreshCw
          className={cn('size-3', props.refreshing && 'animate-spin')}
          aria-hidden='true'
        />
      </Button>
    </CardHeader>
  )
}

/** Single-row totals: the same two numbers the desktop stat cards show. */
function ConnectionLogsMobileStats(props: {
  concurrent: string
  inputTokens: string
}) {
  const { t } = useTranslation()

  return (
    <div
      data-slot='connection-mobile-stats'
      className='text-muted-foreground flex min-w-0 items-center gap-x-3 px-2 text-[10px]'
    >
      <span className='flex min-w-0 items-baseline gap-1'>
        <Activity
          className='text-info size-3 shrink-0 self-center'
          aria-hidden='true'
        />
        <span className='truncate'>{t('Active connections')}</span>
        <span
          data-testid='connection-concurrent'
          className='text-foreground shrink-0 font-mono text-[11px] font-semibold tabular-nums'
        >
          {props.concurrent}
        </span>
      </span>
      <span className='flex min-w-0 items-baseline gap-1'>
        <Waves
          className='text-chart-1 size-3 shrink-0 self-center'
          aria-hidden='true'
        />
        <span className='truncate'>{t('Total input tokens')}</span>
        <span
          data-testid='connection-total-input'
          className='text-foreground shrink-0 font-mono text-[11px] font-semibold tabular-nums'
        >
          {props.inputTokens}
        </span>
      </span>
    </div>
  )
}

export function ConnectionLogsPanel() {
  const { t } = useTranslation()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const [viewMode, setViewMode] = useState<ConnectionViewMode>(() => {
    try {
      const stored = localStorage.getItem(VIEW_MODE_STORAGE_KEY)
      if (stored === 'list' || stored === 'compact' || stored === 'grid') {
        return stored
      }
    } catch {
      /* Private mode or disabled storage: keep the default. */
    }
    return 'list'
  })
  const logsQuery = useQuery({
    queryKey: ['usage-logs', 'connection-logs'],
    queryFn: () => getConnectionLogs(),
    refetchInterval: REFRESH_INTERVAL_MS,
    staleTime: REFRESH_INTERVAL_MS - 1000,
    retry: false,
  })
  const progressQuery = useQuery({
    queryKey: ['usage-logs', 'connection-log-progress'],
    queryFn: () => getConnectionLogs(true),
    refetchInterval: LIVE_PROGRESS_INTERVAL_MS,
    staleTime: 0,
    retry: false,
  })
  const snapshot = logsQuery.data?.data
  const progress = progressQuery.data?.data
  const progressByRequestID = new Map(
    (progress?.items ?? []).map((item) => [item.request_id, item])
  )
  const items = (snapshot?.items ?? EMPTY_ITEMS).map((item) => {
    const liveItem = progressByRequestID.get(item.request_id)
    return liveItem ? { ...item, ...liveItem } : item
  })
  const concurrentValue = formatNumber(
    progress?.concurrent ?? snapshot?.concurrent ?? null
  )
  const totalInputValue = formatNumber(
    progress?.total_input_tokens ?? snapshot?.total_input_tokens ?? null
  )

  let detail: ReactNode
  if (logsQuery.isLoading) {
    detail = <ConnectionLogsFrame>{t('Loading')}</ConnectionLogsFrame>
  } else if (logsQuery.isError) {
    detail = (
      <ConnectionLogsFrame role='alert'>
        {t('Failed to load logs')}
      </ConnectionLogsFrame>
    )
  } else {
    detail = (
      <ConnectionLogsTable
        items={items}
        viewMode={viewMode}
        // The five-second snapshot anchors the node clock; progress polls only
        // refresh values, so the client keeps ticking from this offset.
        clockOffset={
          snapshot == null
            ? null
            : snapshot.server_time - logsQuery.dataUpdatedAt
        }
      />
    )
  }

  const handleViewModeChange = (mode: ConnectionViewMode) => {
    setViewMode(mode)
    try {
      localStorage.setItem(VIEW_MODE_STORAGE_KEY, mode)
    } catch {
      /* The in-memory switch still works when storage is unavailable. */
    }
  }

  if (isMobile) {
    return (
      <Card className='h-full min-h-0 gap-0 overflow-hidden py-0'>
        <ConnectionLogsMobileHeader
          nodeName={snapshot?.node_name}
          refreshing={logsQuery.isFetching}
          onRefresh={() => void logsQuery.refetch()}
          viewMode={viewMode}
          onViewModeChange={handleViewModeChange}
        />
        {/* Same two-slot shape as the desktop content (stats, then the table),
            so changing the breakpoint re-renders the table instead of
            remounting it and losing the current page. */}
        <CardContent className='flex min-h-0 flex-1 flex-col gap-1 px-1.5 py-1'>
          <div className='-mx-1.5 shrink-0'>
            <ConnectionLogsMobileStats
              concurrent={concurrentValue}
              inputTokens={totalInputValue}
            />
          </div>
          {detail}
        </CardContent>
      </Card>
    )
  }

  return (
    <Card className='h-full min-h-0 gap-3 max-sm:gap-2'>
      <CardHeader className='max-sm:px-3'>
        <CardAction>
          <Button
            variant='ghost'
            size='sm'
            onClick={() => void logsQuery.refetch()}
            disabled={logsQuery.isFetching}
          >
            <RefreshCw
              className={cn('size-3.5', logsQuery.isFetching && 'animate-spin')}
              aria-hidden='true'
            />
            {t('Refresh')}
          </Button>
        </CardAction>
        <div className='text-muted-foreground flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs'>
          <span>{t('Live connections on this node')}</span>
          {snapshot?.node_name ? (
            <span className='font-mono'>{snapshot.node_name}</span>
          ) : null}
          <span
            className='flex items-center gap-1'
            title={t('New connections enter on the right')}
          >
            <CircleDashed className='size-3.5' aria-hidden='true' />
            {t('Auto refresh: {{seconds}}s', {
              seconds: REFRESH_INTERVAL_MS / 1000,
            })}
          </span>
        </div>
      </CardHeader>

      <CardContent className='flex min-h-0 flex-1 flex-col gap-3 max-sm:gap-2 max-sm:px-3'>
        <div className='bg-border grid grid-cols-2 gap-px'>
          <ConnectionStat
            label={t('Active connections')}
            testId='connection-concurrent'
            value={concurrentValue}
            icon={<Activity />}
            tone='info'
          />
          <ConnectionStat
            label={t('Total input tokens')}
            testId='connection-total-input'
            value={totalInputValue}
            icon={<Waves />}
            tone='primary'
          />
        </div>
        {detail}
      </CardContent>
    </Card>
  )
}
