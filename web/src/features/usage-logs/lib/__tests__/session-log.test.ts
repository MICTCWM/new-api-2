import { describe, expect, test } from 'vitest'

import { LOG_TYPE_ENUM, LOG_TYPE_FILTERS, RELAY_CONTEXT_LOG_TYPES } from '../../constants'
import { buildSearchParams } from '../filter'
import { renderAuditContent, getSessionEventRowClass } from '../format'
import { sessionEventMapper } from '../mappers'

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

  test('keeps Session logs on the dedicated page instead of common type filters', () => {
    expect(LOG_TYPE_FILTERS.map((type) => type.value)).not.toContain(
      String(LOG_TYPE_ENUM.SESSION)
    )
    expect(RELAY_CONTEXT_LOG_TYPES).toContain(LOG_TYPE_ENUM.SESSION)
  })

  test('builds dedicated session log search params without a common type filter', () => {
    const start = new Date('2026-09-19T00:00:00.000Z')
    const params = buildSearchParams(
      {
        startTime: start,
        model: 'deepseek-v4-flash',
        group: 'default',
        username: 'alice',
        event: 'move',
        channel: '11',
      },
      'session'
    )

    expect(params).toEqual({
      startTime: start.getTime(),
      model: 'deepseek-v4-flash',
      group: 'default',
      username: 'alice',
      event: 'move',
      channel: '11',
    })
    expect(params).not.toHaveProperty('type')
  })

  test('maps session lifecycle events to distinct badge colors', () => {
    expect(sessionEventMapper.getVariant('create')).toBe('green')
    expect(sessionEventMapper.getVariant('hit')).toBe('blue')
    expect(sessionEventMapper.getVariant('move')).toBe('amber')
    expect(sessionEventMapper.getVariant('queue')).toBe('violet')
    expect(sessionEventMapper.getVariant('expire')).toBe('grey')
  })

  test('tints session rows with gradient wash and left stripe per event', () => {
    expect(
      getSessionEventRowClass({
        op: { action: 'opencode.session.create' },
      })
    ).toContain('bg-gradient-to-r')
    expect(
      getSessionEventRowClass({
        op: { action: 'opencode.session.create' },
      })
    ).toContain('border-l-emerald-600')
    expect(
      getSessionEventRowClass({
        op: { action: 'opencode.session.move' },
      })
    ).toContain('border-l-amber-600')
    expect(
      getSessionEventRowClass({
        op: { action: 'opencode.session.queue' },
      })
    ).toContain('border-l-violet-600')
    expect(
      getSessionEventRowClass({
        op: { action: 'opencode.session.expire' },
      })
    ).toContain('border-l-slate-500')
    expect(getSessionEventRowClass(null)).toBe('')
  })
})
