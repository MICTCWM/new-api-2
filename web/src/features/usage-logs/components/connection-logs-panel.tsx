import { useQuery } from '@tanstack/react-query'
import { Activity, CircleDashed, RefreshCw, Waves } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardHeader } from '@/components/ui/card'
import { IconBadge } from '@/components/ui/icon-badge'
import { useMediaQuery } from '@/hooks'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getConnectionLogs } from '../api'
import type { ConnectionLogItem, ConnectionLogSnapshot } from '../types'
import { useConnectionLogStream } from '../use-connection-log-stream'
import {
  ConnectionLogsTable,
  type ConnectionViewMode,
} from './connection-logs-table'
import { ConnectionLogsViewToggle } from './connection-logs-view-toggle'

// The panel is pushed over SSE, so a snapshot appears the moment a request
// starts. This interval is only the degraded path: when the stream keeps
// failing the panel polls the rate-limit-exempt snapshot endpoint instead.
const FALLBACK_POLL_INTERVAL_MS = 10_000
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
 * Picks the fresher of the pushed and the fetch-on-demand snapshot. The manual
 * refresh control and the fallback poll both land in the query, so the newest
 * arrival wins instead of a source always overriding the other.
 */
function pickLiveSnapshot(
  pushed: ConnectionLogSnapshot | null,
  pushedAt: number | null,
  fetched: ConnectionLogSnapshot | null | undefined,
  fetchedAt: number
): ConnectionLogSnapshot | null {
  if (!fetched) return pushed
  if (!pushed) return fetched
  return fetchedAt > (pushedAt ?? 0) ? fetched : pushed
}

/**
 * Mobile header: one 24px line replaces the desktop title block, the wrapping
 * node/refresh copy and the two tall stat cards, so the list starts roughly
 * eighty pixels higher on a 320px viewport.
 */
function ConnectionLogsMobileHeader(props: {
  nodeName?: string
  liveLabel: string
  refreshing: boolean
  onRefresh: () => void
  viewMode: ConnectionViewMode
  onViewModeChange: (mode: ConnectionViewMode) => void
}) {
  const { t } = useTranslation()

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
      <span className='text-muted-foreground ml-auto min-w-0 shrink-0 truncate font-mono text-[10px] tabular-nums'>
        {props.liveLabel}
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
  const stream = useConnectionLogStream()
  // One snapshot query with three jobs: the mount fetch covers the moment
  // before the stream handshake completes, the refresh control fetches on
  // demand, and when the stream keeps failing this becomes the slow polling
  // fallback. It never toasts on failure, so a degraded panel stays quiet.
  const snapshotQuery = useQuery({
    queryKey: ['usage-logs', 'connection-logs'],
    queryFn: () => getConnectionLogs(),
    refetchInterval: stream.fallback ? FALLBACK_POLL_INTERVAL_MS : false,
    staleTime: FALLBACK_POLL_INTERVAL_MS - 1000,
    retry: false,
  })
  const { refetch: refetchSnapshot } = snapshotQuery
  useEffect(() => {
    // Switching to the fallback must fetch at once, not after one interval;
    // keeping the same query key means the last snapshot stays on screen.
    if (stream.fallback) void refetchSnapshot()
  }, [stream.fallback, refetchSnapshot])
  const polledSnapshot = snapshotQuery.data?.data
  const snapshot = pickLiveSnapshot(
    stream.snapshot,
    stream.receivedAt,
    polledSnapshot,
    snapshotQuery.dataUpdatedAt
  )
  const fromStream = snapshot != null && snapshot === stream.snapshot
  const items = snapshot?.items ?? EMPTY_ITEMS
  const concurrentValue = formatNumber(snapshot?.concurrent ?? null)
  const totalInputValue = formatNumber(snapshot?.total_input_tokens ?? null)
  const nodeName = snapshot?.node_name
  const clockOffset =
    snapshot == null
      ? null
      : snapshot.server_time -
        (fromStream ? (stream.receivedAt ?? 0) : snapshotQuery.dataUpdatedAt)
  const refreshing = snapshotQuery.isFetching

  let liveLabel = t('Reconnecting to live stream')
  if (stream.fallback) {
    liveLabel = t('Auto refresh: {{seconds}}s', {
      seconds: FALLBACK_POLL_INTERVAL_MS / 1000,
    })
  } else if (stream.connected) {
    liveLabel = t('Live')
  }

  let detail: ReactNode
  if (snapshot == null) {
    if (snapshotQuery.isError) {
      detail = (
        <ConnectionLogsFrame role='alert'>
          {t('Failed to load logs')}
        </ConnectionLogsFrame>
      )
    } else if (!stream.connected && !stream.fallback) {
      detail = (
        <ConnectionLogsFrame>
          {t('Reconnecting to live stream')}
        </ConnectionLogsFrame>
      )
    } else {
      detail = <ConnectionLogsFrame>{t('Loading')}</ConnectionLogsFrame>
    }
  } else {
    detail = (
      <ConnectionLogsTable
        items={items}
        viewMode={viewMode}
        // Each frame carries the node clock, so the client keeps ticking from
        // the offset of the newest one.
        clockOffset={clockOffset}
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

  const handleRefresh = () => {
    void snapshotQuery.refetch()
  }

  if (isMobile) {
    return (
      <Card className='h-full min-h-0 gap-0 overflow-hidden py-0'>
        <ConnectionLogsMobileHeader
          nodeName={nodeName}
          liveLabel={liveLabel}
          refreshing={refreshing}
          onRefresh={handleRefresh}
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
            onClick={handleRefresh}
            disabled={refreshing}
          >
            <RefreshCw
              className={cn('size-3.5', refreshing && 'animate-spin')}
              aria-hidden='true'
            />
            {t('Refresh')}
          </Button>
        </CardAction>
        <div className='text-muted-foreground flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs'>
          <span>{t('Live connections on this node')}</span>
          {nodeName ? <span className='font-mono'>{nodeName}</span> : null}
          <span
            className='flex items-center gap-1'
            title={t('New connections enter on the right')}
          >
            <CircleDashed className='size-3.5' aria-hidden='true' />
            {liveLabel}
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
