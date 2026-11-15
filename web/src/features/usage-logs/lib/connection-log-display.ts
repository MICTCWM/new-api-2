import type { TFunction } from 'i18next'

import type { StatusVariant } from '@/components/status-badge'

import type { ConnectionLogItem, ConnectionState } from '../types'

/** Placeholder shown wherever a live value has not arrived yet. */
export const CONNECTION_EMPTY_VALUE = '--'

export interface ConnectionStateMeta {
  /** Top-level state: every open connection reads 正在连接. */
  mainKey: string
  /** Sub-state shown only for streaming requests while they are open. */
  subKey?: string
  variant: StatusVariant
}

// The beat meter is the only motion in this cell; the old breathing badge is
// gone so every connection reads the same way.
const CONNECTION_STATE_META: Record<ConnectionState, ConnectionStateMeta> = {
  waiting_first_token: {
    mainKey: 'Connecting',
    subKey: 'Waiting for first token',
    variant: 'warning',
  },
  streaming: {
    mainKey: 'Connecting',
    subKey: 'Streaming output',
    variant: 'info',
  },
  // Non-stream requests only show the top-level 正在连接 state, in purple:
  // they have no cadence, so they must not read like a stream.
  waiting_response: {
    mainKey: 'Connecting',
    variant: 'purple',
  },
  completed: {
    mainKey: 'Connection completed',
    variant: 'success',
  },
}

export function formatConnectionDuration(milliseconds: number): string {
  const seconds = Math.max(0, milliseconds) / 1000
  // Round to whole seconds before choosing the unit: 59_960ms must read
  // "1m 0s", not "60.0s".
  if (seconds < 59.95) return `${seconds.toFixed(1)}s`
  const roundedSeconds = Math.round(seconds)
  return `${Math.floor(roundedSeconds / 60)}m ${roundedSeconds % 60}s`
}

/** `group · #channel`, dropping the channel segment when it is unknown. */
export function getConnectionRoute(item: ConnectionLogItem): string {
  return [item.group, item.channel_id > 0 ? `#${item.channel_id}` : '']
    .filter((segment) => segment !== '')
    .join(' · ')
}

/**
 * Text label for the dense mobile layouts, which have no avatar slot to hide
 * behind: an anonymous account keeps its numeric id, a masked view reads ••••.
 */
export function getConnectionDisplayName(
  item: ConnectionLogItem,
  sensitiveVisible: boolean
): string {
  if (!item.username) return `#${item.user_id}`
  return sensitiveVisible ? item.username : '••••'
}

/** Live elapsed time for an open connection, final duration once it ends. */
function getConnectionElapsed(item: ConnectionLogItem, now: number): number {
  return item.active ? Math.max(0, now - item.started_at) : item.duration_ms
}

interface ConnectionPresentation {
  meta: ConnectionStateMeta
  elapsed: number
  /**
   * Sub-state line under the desktop badge. A retry backoff pauses the stream
   * without ending it, so the countdown replaces the normal sub-state.
   */
  subText: string
  retrying: boolean
  /** Whole seconds left in the retry backoff; zero outside a retry wait. */
  retrySeconds: number
}

/**
 * Everything the desktop row and the mobile card derive from one item. The
 * route is not included: only the desktop table renders it, and the mobile
 * card re-renders every 50ms without using it.
 */
export function getConnectionPresentation(
  item: ConnectionLogItem,
  now: number,
  t: TFunction
): ConnectionPresentation {
  const meta = CONNECTION_STATE_META[item.state]
  const retrySeconds =
    item.retry_wait_ms > 0
      ? Math.max(1, Math.ceil(item.retry_wait_ms / 1000))
      : 0

  let subText = ''
  if (retrySeconds > 0) {
    subText = t('Retrying in {{seconds}}s', { seconds: retrySeconds })
  } else if (meta.subKey) {
    subText = t(meta.subKey)
  }

  return {
    meta,
    elapsed: getConnectionElapsed(item, now),
    subText,
    retrying: retrySeconds > 0,
    retrySeconds,
  }
}
