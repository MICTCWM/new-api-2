import { describe, expect, test } from 'vitest'

import { formatConnectionDuration } from '../connection-log-display'

describe('formatConnectionDuration', () => {
  test.each([
    [0, '0.0s'],
    [4_000, '4.0s'],
    [59_949, '59.9s'],
    [59_950, '1m 0s'],
    [59_960, '1m 0s'],
    [60_000, '1m 0s'],
    [125_000, '2m 5s'],
  ])('formats %ims as %s', (milliseconds, expected) => {
    expect(formatConnectionDuration(milliseconds)).toBe(expected)
  })
})
