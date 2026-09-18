import { describe, expect, test } from 'vitest'

import { LOG_TYPE_ENUM, LOG_TYPE_FILTERS, RELAY_CONTEXT_LOG_TYPES } from '../../constants'
import { renderAuditContent } from '../format'

function interpolate(key: string, options?: Record<string, unknown>): string {
  if (!options) return key
  return key.replace(/\{\{(\w+)\}\}/g, (_match, name: string) =>
    options[name] == null ? '' : String(options[name])
  )
}

describe('opencode session usage log rendering', () => {
  test('localizes a create event with its session id', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'opencode.session.create',
          params: { session_id: 'abc123', channel_id: 11 },
        },
      },
      interpolate
    )

    expect(rendered).toBe('Created session abc123')
  })

  test('localizes a hit event with its session id', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'opencode.session.hit',
          params: { session_id: 'hit-session', channel_id: 12 },
        },
      },
      interpolate
    )

    expect(rendered).toBe('Reused session hit-session')
  })

  test('localizes a move event with from and to channel ids', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'opencode.session.move',
          params: {
            session_id: 'moved-session',
            from_channel_id: 3,
            to_channel_id: 8,
          },
        },
      },
      interpolate
    )

    expect(rendered).toBe('Moved session from channel 3 to 8')
  })

  test('localizes a queue event with timeout_hard_fallback', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'opencode.session.queue',
          params: {
            result: 'timeout_hard_fallback',
            wait_seconds: 8,
          },
        },
      },
      interpolate
    )

    expect(rendered).toBe('Queued session (timeout_hard_fallback)')
  })

  test('localizes an expire event', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'opencode.session.expire',
          params: { session_id: 'expired-session', channel_id: 4 },
        },
      },
      interpolate
    )

    expect(rendered).toBe('Session expired')
  })

  test('exposes Session in common log type filters and relay context types', () => {
    expect(LOG_TYPE_FILTERS.map((type) => type.value)).toContain(
      String(LOG_TYPE_ENUM.SESSION)
    )
    expect(RELAY_CONTEXT_LOG_TYPES).toContain(LOG_TYPE_ENUM.SESSION)
  })
})
