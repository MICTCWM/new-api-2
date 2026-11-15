import { act, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import type { ConnectionLogItem } from '../../types'
import { ConnectionOutputTokens } from '../connection-output-tokens'

const BASE_ITEM: ConnectionLogItem = {
  request_id: 'req-a',
  username: 'alice',
  user_id: 3,
  group: 'default',
  model: 'gpt-5.4',
  stream: true,
  channel_id: 12,
  state: 'streaming',
  started_at: 1_700_000_000_000,
  first_token_at: 1_700_000_000_900,
  finished_at: null,
  ttft_ms: 900,
  duration_ms: 0,
  input_tokens: 1200,
  output_tokens: 0,
  stream_chunks: 0,
  active: true,
  retry_count: 0,
  retry_wait_ms: 0,
}

const originalMatchMedia = window.matchMedia

function stubReducedMotion(reduce: boolean) {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (query: string): MediaQueryList => ({
      matches: reduce && query.includes('prefers-reduced-motion'),
      media: query,
      onchange: null,
      addListener: () => undefined,
      removeListener: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      dispatchEvent: () => false,
    }),
  })
}

/** A manually flushed requestAnimationFrame queue so frame scheduling is observable. */
function installFrameQueue() {
  const callbacks = new Map<number, FrameRequestCallback>()
  let nextHandle = 1
  let scheduled = 0

  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    const handle = nextHandle
    nextHandle += 1
    scheduled += 1
    callbacks.set(handle, callback)
    return handle
  })
  vi.stubGlobal('cancelAnimationFrame', (handle: number) => {
    callbacks.delete(handle)
  })

  return {
    get pending() {
      return callbacks.size
    },
    get scheduled() {
      return scheduled
    },
    flush(timestamp: number) {
      const pending = [...callbacks.values()]
      callbacks.clear()
      act(() => {
        for (const callback of pending) callback(timestamp)
      })
    },
  }
}

describe('connection output tokens', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: originalMatchMedia,
    })
  })

  test('stops requesting animation frames once the count reaches its target', () => {
    stubReducedMotion(false)
    const frames = installFrameQueue()
    const { rerender } = render(
      <ConnectionOutputTokens item={{ ...BASE_ITEM, output_tokens: 0 }} />
    )

    rerender(
      <ConnectionOutputTokens item={{ ...BASE_ITEM, output_tokens: 400 }} />
    )
    expect(frames.pending).toBeGreaterThan(0)

    let timestamp = performance.now()
    for (let frame = 0; frame < 100 && frames.pending > 0; frame += 1) {
      timestamp += 250
      frames.flush(timestamp)
    }

    expect(screen.getByText('400')).toBeInTheDocument()
    expect(frames.pending).toBe(0)

    const scheduledAtCatchUp = frames.scheduled
    frames.flush(timestamp + 250)
    expect(frames.scheduled).toBe(scheduledAtCatchUp)

    // A later poll raises the target again: the chase must restart.
    rerender(
      <ConnectionOutputTokens item={{ ...BASE_ITEM, output_tokens: 410 }} />
    )
    expect(frames.scheduled).toBeGreaterThan(scheduledAtCatchUp)
    expect(frames.pending).toBeGreaterThan(0)

    for (let frame = 0; frame < 100 && frames.pending > 0; frame += 1) {
      timestamp += 250
      frames.flush(timestamp)
    }

    expect(screen.getByText('410')).toBeInTheDocument()
    expect(frames.pending).toBe(0)

    const scheduledAtSecondCatchUp = frames.scheduled
    frames.flush(timestamp + 250)
    expect(frames.scheduled).toBe(scheduledAtSecondCatchUp)
  })

  test('cancels the pending frame when the component unmounts', () => {
    stubReducedMotion(false)
    const frames = installFrameQueue()
    const { rerender, unmount } = render(
      <ConnectionOutputTokens item={{ ...BASE_ITEM, output_tokens: 0 }} />
    )

    rerender(
      <ConnectionOutputTokens item={{ ...BASE_ITEM, output_tokens: 400 }} />
    )
    expect(frames.pending).toBeGreaterThan(0)

    unmount()

    expect(frames.pending).toBe(0)
  })

  test('shows the final value immediately without frames when motion is reduced', () => {
    stubReducedMotion(true)
    const frames = installFrameQueue()
    const { rerender } = render(
      <ConnectionOutputTokens item={{ ...BASE_ITEM, output_tokens: 100 }} />
    )

    rerender(
      <ConnectionOutputTokens item={{ ...BASE_ITEM, output_tokens: 512 }} />
    )

    expect(screen.getByText('512')).toBeInTheDocument()
    expect(frames.scheduled).toBe(0)
  })
})
