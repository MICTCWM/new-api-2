import { describe, expect, test } from 'vitest'

import { renderAuditContent } from '../format'

describe('speed protection usage log rendering', () => {
  test('localizes an entered event with its protected group', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'speed_protection.entered',
          params: { group: 'default' },
        },
      },
      (key, options) => `${key}::${String(options?.group)}`
    )

    expect(rendered).toBe(
      'Speed protection activated for group {{group}}::default'
    )
  })

  test('localizes a kicked event with its protected group', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'speed_protection.kicked',
          params: { group: 'premium' },
        },
      },
      (key, options) => `${key}::${String(options?.group)}`
    )

    expect(rendered).toBe(
      'Speed protection removed from group {{group}}::premium'
    )
  })
})
