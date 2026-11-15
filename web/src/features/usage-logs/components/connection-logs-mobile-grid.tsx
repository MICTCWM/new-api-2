import { Check } from 'lucide-react'
import type { RefObject } from 'react'
import { useTranslation } from 'react-i18next'

import { dotColorMap } from '@/components/status-badge'
import { formatCompactNumber, formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  CONNECTION_EMPTY_VALUE,
  getConnectionDisplayName,
  getConnectionPresentation,
} from '../lib/connection-log-display'
import type { ConnectionLogItem } from '../types'
import { ConnectionEmptyState } from './connection-log-parts'
import { useUsageLogsContext } from './usage-logs-provider'

/**
 * One matrix cell: user on top, input/output totals below, state carried by the
 * tone dot (pulsing while open) plus the completion check. The whole cell is the
 * user button, so a tap opens the same profile dialog as the list layouts.
 */
function ConnectionLogsGridCell(props: {
  item: ConnectionLogItem
  now: number
}) {
  const { t } = useTranslation()
  const { sensitiveVisible, setSelectedUserId, setUserInfoDialogOpen } =
    useUsageLogsContext()
  const presentation = getConnectionPresentation(props.item, props.now, t)
  const name = getConnectionDisplayName(props.item, sensitiveVisible)
  const inputTokens = formatNumber(props.item.input_tokens)
  const outputTokens =
    props.item.output_tokens == null
      ? CONNECTION_EMPTY_VALUE
      : formatNumber(props.item.output_tokens)
  // Tiles are ~80px wide: full totals overflow two columns, so the visible line
  // is compact (12.8K) while the title/aria label keep the exact numbers.
  const compactInput = formatCompactNumber(props.item.input_tokens)
  const compactOutput =
    props.item.output_tokens == null
      ? CONNECTION_EMPTY_VALUE
      : formatCompactNumber(props.item.output_tokens)
  const statusLabel = t(presentation.meta.mainKey)

  return (
    <button
      type='button'
      data-slot='connection-grid-cell'
      data-connection-request-id={props.item.request_id}
      aria-label={`${t('User')} ${name} ${statusLabel} ${t('Input')} ${inputTokens} ${t('Output')} ${outputTokens}`}
      className='bg-card animate-in fade-in-0 flex min-h-11 min-w-0 flex-col justify-between gap-0.5 px-1.5 py-1 text-left duration-300 motion-reduce:animate-none'
      onClick={() => {
        setSelectedUserId(props.item.user_id)
        setUserInfoDialogOpen(true)
      }}
    >
      <span className='flex min-w-0 items-center gap-1'>
        <span
          className={cn(
            'size-1.5 shrink-0 rounded-full',
            dotColorMap[presentation.meta.variant],
            props.item.active && 'animate-pulse'
          )}
          aria-hidden='true'
        />
        <span className='min-w-0 flex-1 truncate text-[10px] leading-none font-medium'>
          {name}
        </span>
        {props.item.active ? null : (
          <Check className='text-success size-3 shrink-0' aria-hidden='true' />
        )}
      </span>
      <span className='flex min-w-0 items-baseline gap-1 font-mono text-[10px] leading-none tabular-nums'>
        <span>{compactInput}</span>
        <span className='text-muted-foreground'>/</span>
        <span className='text-muted-foreground min-w-0 truncate'>
          {compactOutput}
        </span>
      </span>
    </button>
  )
}

/**
 * Mobile matrix layout: every request on the current snapshot becomes one tile
 * of an auto-filling grid, so a hundred idle connections still fit on screen.
 */
export function ConnectionLogsMobileGrid(props: {
  rows: ConnectionLogItem[]
  now: number
  scrollAreaRef: RefObject<HTMLDivElement | null>
}) {
  return (
    <div className='flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border'>
      <div
        ref={props.scrollAreaRef}
        className='min-h-0 flex-1 overflow-y-auto overscroll-contain'
      >
        {props.rows.length === 0 ? (
          <div
            data-slot='connection-grid-empty'
            className='flex h-full items-center justify-center'
          >
            <ConnectionEmptyState />
          </div>
        ) : (
          <div
            data-slot='connection-grid'
            className='bg-border grid auto-rows-min grid-cols-[repeat(auto-fill,minmax(5rem,1fr))] gap-px'
          >
            {props.rows.map((item) => (
              <ConnectionLogsGridCell
                key={item.request_id}
                item={item}
                now={props.now}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
