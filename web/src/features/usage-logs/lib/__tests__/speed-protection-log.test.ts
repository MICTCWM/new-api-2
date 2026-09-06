import { describe, expect, test } from 'vitest'

import { renderAuditContent } from '../format'

describe('speed protection usage log rendering', () => {
  test('localizes an entered event with its protected group', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'speed_protection.entered',
          params: { group: 'default', channel_name: 'Alpha' },
        },
      },
      (key, options) => `${key}::${String(options?.group)}`
    )

    expect(rendered).toBe(
      'Channel {{channel_name}} entered speed protection for group {{group}}::default'
    )
  })

  test('localizes a kicked event with its protected group', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'speed_protection.kicked',
          params: { group: 'premium', channel_name: 'Beta' },
        },
      },
      (key, options) => `${key}::${String(options?.group)}`
    )

    expect(rendered).toBe(
      'Channel {{channel_name}} kicked out of speed protection for group {{group}}::premium'
    )
  })
})
