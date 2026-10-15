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
import { Activity, CircleDashed, RefreshCw, Waves } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardHeader } from '@/components/ui/card'
import { IconBadge } from '@/components/ui/icon-badge'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getConnectionLogs } from '../api'
import type { ConnectionLogItem } from '../types'
import { ConnectionLogsTable } from './connection-logs-table'

// Token counts must keep moving smoothly, so progress polls well below the
// five-second log refresh.
const REFRESH_INTERVAL_MS = 5000
const LIVE_PROGRESS_INTERVAL_MS = 500
const EMPTY_ITEMS: ConnectionLogItem[] = []

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
        className='mt-1 font-mono text-lg leading-none font-semibold tabular-nums'
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
      className='text-muted-foreground flex min-h-32 items-center justify-center rounded-lg border text-sm'
    >
      {props.children}
    </div>
  )
}

export function ConnectionLogsPanel() {
  const { t } = useTranslation()
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

  return (
    <Card className='h-full min-h-0 gap-3'>
      <CardHeader>
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
        <div className='text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-xs'>
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

      <CardContent className='flex min-h-0 flex-1 flex-col gap-3'>
        <div className='bg-border grid grid-cols-2 gap-px'>
          <ConnectionStat
            label={t('Active connections')}
            testId='connection-concurrent'
            value={formatNumber(
              progress?.concurrent ?? snapshot?.concurrent ?? null
            )}
            icon={<Activity />}
            tone='info'
          />
          <ConnectionStat
            label={t('Total input tokens')}
            testId='connection-total-input'
            value={formatNumber(
              progress?.total_input_tokens ??
                snapshot?.total_input_tokens ??
                null
            )}
            icon={<Waves />}
            tone='primary'
          />
        </div>
        {detail}
      </CardContent>
    </Card>
  )
}
