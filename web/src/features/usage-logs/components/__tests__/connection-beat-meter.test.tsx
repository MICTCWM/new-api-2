import { act, render } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import type { StatusVariant } from '@/components/status-badge'

import type { ConnectionState } from '../../types'
import { ConnectionBeatMeter } from '../connection-beat-meter'

const METER_SELECTOR = '[data-slot="connection-beat-meter"]'
const TICK_MS = 50
const BAR_COUNT = 12
const LAST_BAR = BAR_COUNT - 1

function bars(container: HTMLElement): HTMLSpanElement[] {
  return [
    ...container.querySelectorAll<HTMLSpanElement>('.connection-beat-bar'),
  ]
}

function meterProps(
  overrides: Partial<{
    state: ConnectionState
    active: boolean
    tokens: number | null
    tone: StatusVariant
  }> = {}
) {
  return {
    state: (overrides.state ?? 'streaming') as ConnectionState,
    active: overrides.active ?? true,
    tokens: overrides.tokens === undefined ? 0 : overrides.tokens,
    tone: (overrides.tone ?? 'info') as StatusVariant,
  }
}

describe('connection beat meter', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('sweeps the bars while the stream has not produced tokens yet', () => {
    const { container } = render(
      <ConnectionBeatMeter
        {...meterProps({ state: 'waiting_first_token', tokens: null })}
      />
    )

    const meter = container.querySelector(METER_SELECTOR)
    expect(meter).not.toBeNull()
    expect(meter?.getAttribute('data-scan')).toBe('1')
    expect(meter?.getAttribute('aria-hidden')).toBe('true')
    expect(bars(container)).toHaveLength(BAR_COUNT)
  })

  test('reads live cadence instead of sweeping once the stream is producing', () => {
    const { container } = render(
      <ConnectionBeatMeter {...meterProps({ tokens: 24 })} />
    )

    expect(
      container.querySelector(METER_SELECTOR)?.getAttribute('data-scan')
    ).toBe('0')
  })

  test('scrolls a bar in on every tick while tokens arrive', () => {
    vi.useFakeTimers()
    const { container, rerender } = render(
      <ConnectionBeatMeter {...meterProps({ tokens: 0 })} />
    )

    rerender(<ConnectionBeatMeter {...meterProps({ tokens: 20 })} />)
    act(() => {
      vi.advanceTimersByTime(TICK_MS)
    })

    // 20 tokens over a 500ms poll interval = 2 tokens per 50ms bar.
    expect(bars(container)[LAST_BAR].style.height).toBe('12px')
    expect(bars(container)[LAST_BAR - 1].style.height).toBe('2px')

    act(() => {
      vi.advanceTimersByTime(TICK_MS)
    })

    expect(bars(container)[LAST_BAR - 1].style.height).toBe('12px')
    expect(bars(container)[LAST_BAR - 2].style.height).toBe('2px')
  })

  test('spreads one poll over the whole interval instead of replaying it as a spike', () => {
    vi.useFakeTimers()
    const { container, rerender } = render(
      <ConnectionBeatMeter {...meterProps({ tokens: 0 })} />
    )

    rerender(<ConnectionBeatMeter {...meterProps({ tokens: 20 })} />)
    act(() => {
      vi.advanceTimersByTime(TICK_MS * 4)
    })

    // Four ticks in, only four bars have received tokens.
    const heights = bars(container).map((bar) => bar.style.height)
    expect(heights.slice(0, BAR_COUNT - 4)).toEqual(
      Array(BAR_COUNT - 4).fill('2px')
    )
    expect(heights.slice(BAR_COUNT - 4)).toEqual(Array(4).fill('12px'))

    act(() => {
      vi.advanceTimersByTime(TICK_MS * 6)
    })

    // By the end of the poll interval the burst has filled ten bars, i.e. the
    // 500ms it is spread over — not the instantaneous spike one poll would be.
    const lit = bars(container).filter((bar) => bar.style.height === '12px')
    expect(lit).toHaveLength(10)
  })

  test('drains the bars while upstream stays silent', () => {
    vi.useFakeTimers()
    const { container, rerender } = render(
      <ConnectionBeatMeter {...meterProps({ tokens: 0 })} />
    )

    rerender(<ConnectionBeatMeter {...meterProps({ tokens: 20 })} />)
    act(() => {
      vi.advanceTimersByTime(500)
    })
    expect(bars(container)[LAST_BAR].style.height).toBe('12px')

    act(() => {
      vi.advanceTimersByTime(TICK_MS * BAR_COUNT)
    })

    for (const bar of bars(container)) {
      expect(bar.style.height).toBe('2px')
    }
  })

  test('keeps a slow stream readable above the idle height', () => {
    vi.useFakeTimers()
    const { container, rerender } = render(
      <ConnectionBeatMeter {...meterProps({ tokens: 0 })} />
    )

    rerender(<ConnectionBeatMeter {...meterProps({ tokens: 6 })} />)
    act(() => {
      vi.advanceTimersByTime(TICK_MS)
    })

    // 0.6 tokens per bar is scaled against the 0.75-token floor, not itself.
    expect(bars(container)[LAST_BAR].style.height).toBe('10px')
  })

  test('ignores the estimate published at the first token as a cadence spike', () => {
    vi.useFakeTimers()
    const { container, rerender } = render(
      <ConnectionBeatMeter
        {...meterProps({ state: 'waiting_first_token', tokens: null })}
      />
    )

    rerender(<ConnectionBeatMeter {...meterProps({ tokens: 180 })} />)
    act(() => {
      vi.advanceTimersByTime(TICK_MS)
    })

    expect(bars(container)[LAST_BAR].style.height).toBe('2px')
  })

  test('hands the cell back once the connection closes', () => {
    const { container } = render(
      <ConnectionBeatMeter
        {...meterProps({ state: 'completed', active: false, tokens: 512 })}
      />
    )

    expect(container.querySelector(METER_SELECTOR)).toBeNull()
  })
})
