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
    cadence: number[] | null
    tone: StatusVariant
  }> = {}
) {
  return {
    state: (overrides.state ?? 'streaming') as ConnectionState,
    active: overrides.active ?? true,
    tokens: overrides.tokens === undefined ? 0 : overrides.tokens,
    cadence: overrides.cadence,
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
    expect(bars(container)[LAST_BAR].style.height).toBe('14px')
    expect(bars(container)[LAST_BAR - 1].style.height).toBe('2px')

    act(() => {
      vi.advanceTimersByTime(TICK_MS)
    })

    expect(bars(container)[LAST_BAR - 1].style.height).toBe('14px')
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
    expect(heights.slice(BAR_COUNT - 4)).toEqual(Array(4).fill('14px'))

    act(() => {
      vi.advanceTimersByTime(TICK_MS * 6)
    })

    // By the end of the poll interval the burst has filled ten bars, i.e. the
    // 500ms it is spread over — not the instantaneous spike one poll would be.
    const lit = bars(container).filter((bar) => bar.style.height === '14px')
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
    expect(bars(container)[LAST_BAR].style.height).toBe('14px')

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
    expect(bars(container)[LAST_BAR].style.height).toBe('11px')
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

  test('draws real 100ms cadence buckets instead of a synthetic even rate', () => {
    const cadence = [0, 0, 1, 3, 0, 2, 0, 4, 0, 1, 0, 2]
    const { container } = render(
      <ConnectionBeatMeter {...meterProps({ tokens: 100, cadence })} />
    )

    expect(bars(container).map((bar) => bar.style.height)).toEqual([
      '2px',
      '2px',
      '4px',
      '6px',
      '2px',
      '5px',
      '2px',
      '7px',
      '2px',
      '4px',
      '2px',
      '5px',
    ])
    expect(
      container.querySelector(METER_SELECTOR)?.getAttribute('data-scan')
    ).toBe('0')
  })

  test('keeps the bars idle when real cadence reports a pause', () => {
    const { container } = render(
      <ConnectionBeatMeter
        {...meterProps({ tokens: 100, cadence: Array(BAR_COUNT).fill(0) })}
      />
    )

    for (const bar of bars(container)) {
      expect(bar.style.height).toBe('2px')
    }
  })

  test('keeps intermediate cadence rates between idle and full height', () => {
    const cadence = [0, 6, 0, 12, 0, 24, 0, 48, 0, 0, 0, 0]
    const { container } = render(
      <ConnectionBeatMeter {...meterProps({ tokens: 100, cadence })} />
    )

    expect(bars(container).map((bar) => bar.style.height)).toEqual([
      '2px',
      '8px',
      '2px',
      '10px',
      '2px',
      '12px',
      '2px',
      '14px',
      '2px',
      '2px',
      '2px',
      '2px',
    ])
  })

  test('eases a new cadence window in 50ms steps', () => {
    vi.useFakeTimers()
    const idle = Array(BAR_COUNT).fill(0)
    const { container, rerender } = render(
      <ConnectionBeatMeter {...meterProps({ tokens: 100, cadence: idle })} />
    )

    expect(bars(container)[LAST_BAR].style.height).toBe('2px')

    rerender(
      <ConnectionBeatMeter
        {...meterProps({
          tokens: 102,
          cadence: [...idle.slice(1), 2],
        })}
      />
    )

    // The new target is set immediately, but the visual step waits for the
    // 50ms tick and only covers one fifth of the distance.
    expect(bars(container)[LAST_BAR].style.height).toBe('2px')
    act(() => {
      vi.advanceTimersByTime(TICK_MS)
    })
    expect(bars(container)[LAST_BAR].style.height).toBe('2.6px')

    act(() => {
      vi.advanceTimersByTime(TICK_MS * 4)
    })
    expect(bars(container)[LAST_BAR].style.height).toBe('5px')
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
