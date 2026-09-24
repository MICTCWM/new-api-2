import { describe, expect, test } from 'vitest'

import { getUpstreamCostUSD } from '../format'

describe('upstream cost formatting data', () => {
  test('prefers the Command cost when both provider fields exist', () => {
    expect(
      getUpstreamCostUSD({
        command_upstream_cost_usd: 1.25,
        opencode_upstream_cost_usd: 0.75,
      })
    ).toBe(1.25)
  })

  test('falls back to OpenCode and rejects invalid values', () => {
    expect(getUpstreamCostUSD({ opencode_upstream_cost_usd: 0.75 })).toBe(0.75)
    expect(
      getUpstreamCostUSD({ command_upstream_cost_usd: -1 })
    ).toBeUndefined()
    expect(
      getUpstreamCostUSD({
        command_upstream_cost_usd: Number.POSITIVE_INFINITY,
      })
    ).toBeUndefined()
  })
})
