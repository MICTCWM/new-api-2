import { Activity, Check } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { textColorMap, type StatusVariant } from '@/components/status-badge'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import {
  Empty,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { getUserAvatarFallback, getUserAvatarStyle } from '@/lib/avatar'
import { cn } from '@/lib/utils'

import {
  CONNECTION_EMPTY_VALUE,
  formatConnectionDuration,
} from '../lib/connection-log-display'
import { getFirstResponseTimeColor } from '../lib/format'
import type { ConnectionLogItem } from '../types'
import { ConnectionBeatMeter } from './connection-beat-meter'
import { ConnectionDropMeter } from './connection-drop-meter'
import { useUsageLogsContext } from './usage-logs-provider'

/** Shared empty state for the desktop table and the mobile card list. */
export function ConnectionEmptyState() {
  const { t } = useTranslation()

  return (
    <Empty>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <Activity className='size-6' aria-hidden='true' />
        </EmptyMedia>
        <EmptyTitle>{t('No connections right now')}</EmptyTitle>
      </EmptyHeader>
    </Empty>
  )
}

export function ConnectionRetryBadge(props: { count: number }) {
  const { t } = useTranslation()

  if (props.count <= 0) return null

  return (
    <span
      data-slot='connection-retry-count'
      className='text-warning bg-warning/10 rounded-full px-1.5 text-[10px] leading-4 font-medium tracking-normal tabular-nums'
    >
      {t('Retry ×{{count}}', { count: props.count })}
    </span>
  )
}

/** Check for finished requests, beat meter for streams, drop meter otherwise. */
export function ConnectionLiveIndicator(props: {
  item: ConnectionLogItem
  tone: StatusVariant
}) {
  if (!props.item.active) {
    return (
      <Check
        data-slot='connection-done-check'
        className='text-success size-3.5'
        aria-hidden='true'
      />
    )
  }

  if (props.item.stream) {
    return (
      <ConnectionBeatMeter
        state={props.item.state}
        active
        tokens={props.item.output_tokens}
        cadence={props.item.output_cadence}
        tone={props.tone}
      />
    )
  }

  return <ConnectionDropMeter tone={props.tone} />
}

/** TTFT belongs to streaming requests only, and reads -- until the first token. */
export function ConnectionTtftValue(props: { item: ConnectionLogItem }) {
  if (!props.item.stream) return null

  if (props.item.ttft_ms == null) {
    return (
      <span className='text-muted-foreground'>{CONNECTION_EMPTY_VALUE}</span>
    )
  }

  const ttftSeconds = props.item.ttft_ms / 1000
  return (
    <span className={textColorMap[getFirstResponseTimeColor(ttftSeconds)]}>
      {formatConnectionDuration(props.item.ttft_ms)}
    </span>
  )
}

/**
 * User button shared by the desktop cell and the mobile card. The avatar and
 * name styling differ per layout, so callers pass the class hooks.
 */
export function ConnectionUserButton(props: {
  item: ConnectionLogItem
  avatarClassName?: string
  nameClassName?: string
  /** Mobile layout: keep only the avatar; the name stays in the accessible label. */
  avatarOnly?: boolean
}) {
  const { t } = useTranslation()
  const { sensitiveVisible, setSelectedUserId, setUserInfoDialogOpen } =
    useUsageLogsContext()

  if (!props.item.username) {
    return (
      <span
        data-slot='connection-user-fallback'
        className={cn(
          'text-muted-foreground font-mono',
          // The card aligns its second line under the first column, so the
          // fallback label needs the same 20px footprint as the avatar. The
          // clip keeps a long numeric id from painting over the time column.
          props.avatarOnly &&
            'flex size-5 shrink-0 items-center justify-center overflow-hidden text-[10px]'
        )}
      >
        <span className={cn(props.avatarOnly && 'min-w-0 truncate')}>
          #{props.item.user_id}
        </span>
      </span>
    )
  }

  return (
    <button
      type='button'
      aria-label={
        sensitiveVisible
          ? `${t('User')} ${props.item.username}`
          : `${t('User')} ••••`
      }
      className={cn(
        'flex min-w-0 items-center gap-1.5 text-left',
        // Negative margin keeps the 20px layout slot while the padding grows
        // the touch target to 28px.
        props.avatarOnly && '-m-1 shrink-0 p-1'
      )}
      onClick={(event) => {
        event.stopPropagation()
        setSelectedUserId(props.item.user_id)
        setUserInfoDialogOpen(true)
      }}
    >
      <Avatar
        aria-hidden='true'
        className={cn(
          'ring-border/60 size-6 shrink-0 ring-1',
          props.avatarClassName
        )}
      >
        <AvatarFallback
          className={cn(
            'font-semibold',
            !sensitiveVisible && 'bg-muted text-muted-foreground'
          )}
          style={
            sensitiveVisible
              ? getUserAvatarStyle(props.item.username)
              : undefined
          }
        >
          {sensitiveVisible ? getUserAvatarFallback(props.item.username) : '•'}
        </AvatarFallback>
      </Avatar>
      {props.avatarOnly ? null : (
        <TooltipProvider delay={300}>
          <Tooltip>
            <TooltipTrigger
              render={
                <span
                  className={cn(
                    'text-muted-foreground max-w-[100px] truncate hover:underline',
                    props.nameClassName
                  )}
                />
              }
            >
              {sensitiveVisible ? props.item.username : '••••'}
            </TooltipTrigger>
            {sensitiveVisible && props.item.username.length > 12 && (
              <TooltipContent side='top'>{props.item.username}</TooltipContent>
            )}
          </Tooltip>
        </TooltipProvider>
      )}
    </button>
  )
}
