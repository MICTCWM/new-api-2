import { useTranslation } from 'react-i18next'

import { dotColorMap } from '@/components/status-badge'
import { formatCompactNumber, formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  CONNECTION_EMPTY_VALUE,
  formatConnectionDuration,
  getConnectionDisplayName,
  getConnectionPresentation,
} from '../lib/connection-log-display'
import type { ConnectionLogItem } from '../types'
import { ModelBadge } from './model-badge'
import { useUsageLogsContext } from './usage-logs-provider'

/**
 * Densest list row: one line per connection. The state survives as a tone dot
 * (pulsing while the request is open) and an accessible-only label, which keeps
 * a dozen rows visible without repeating 正在连接 on every line.
 */
export function ConnectionLogsCompactRow(props: {
  item: ConnectionLogItem
  now: number
}) {
  const { t } = useTranslation()
  const { sensitiveVisible, setSelectedUserId, setUserInfoDialogOpen } =
    useUsageLogsContext()
  const presentation = getConnectionPresentation(props.item, props.now, t)
  const name = getConnectionDisplayName(props.item, sensitiveVisible)
  const fullInput = formatNumber(props.item.input_tokens)
  const fullOutput =
    props.item.output_tokens == null
      ? CONNECTION_EMPTY_VALUE
      : formatNumber(props.item.output_tokens)

  return (
    <div
      role='listitem'
      data-slot='connection-compact-row'
      data-connection-request-id={props.item.request_id}
      title={`${t('Input')} ${fullInput} · ${t('Output')} ${fullOutput}`}
      className='animate-in fade-in-0 slide-in-from-right-4 border-border/40 flex min-w-0 items-center gap-1 border-b px-2 py-[3px] text-[11px] leading-tight duration-300 last:border-b-0 motion-reduce:animate-none'
    >
      <span
        className={cn(
          'size-1.5 shrink-0 rounded-full',
          dotColorMap[presentation.meta.variant],
          props.item.active && 'animate-pulse'
        )}
        aria-hidden='true'
      />
      <button
        type='button'
        aria-label={`${t('User')} ${name}`}
        className='max-w-[3.5rem] shrink-0 truncate text-left font-medium hover:underline'
        onClick={(event) => {
          event.stopPropagation()
          setSelectedUserId(props.item.user_id)
          setUserInfoDialogOpen(true)
        }}
      >
        {name}
      </button>
      <ModelBadge
        compact
        wrap
        modelName={props.item.model}
        className='max-w-[6rem] min-w-0 shrink'
      />
      <span className='ml-auto shrink-0 font-mono tabular-nums'>
        <span className='text-muted-foreground'>↑</span>
        {formatCompactNumber(props.item.input_tokens)}
        <span className='text-muted-foreground'> ↓</span>
        {props.item.output_tokens == null
          ? CONNECTION_EMPTY_VALUE
          : formatCompactNumber(props.item.output_tokens)}
      </span>
      <span className='text-muted-foreground w-9 shrink-0 text-right font-mono text-[10px] tabular-nums'>
        {formatConnectionDuration(presentation.elapsed)}
      </span>
      <span className='sr-only'>{t(presentation.meta.mainKey)}</span>
    </div>
  )
}
