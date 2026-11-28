/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import type { ConnectionLogSnapshot } from '../types'
import { useConnectionLogStream } from '../use-connection-log-stream'

const SNAPSHOT: ConnectionLogSnapshot = {
  concurrent: 1,
  total_input_tokens: 1200,
  server_time: 1_700_000_005_000,
  node_name: 'node-a',
  items: [],
}

/** A response whose body is written by the test, one chunk at a time. */
function createOpenStream() {
  let push: (chunk: string) => void = () => undefined
  let stop: () => void = () => undefined
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      push = (chunk) => controller.enqueue(new TextEncoder().encode(chunk))
      stop = () => controller.close()
    },
  })
  const response = new Response(body, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  })
  return { response, push, stop }
}

describe('useConnectionLogStream', () => {
  beforeEach(() => {
    vi.useRealTimers()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  test('assembles a snapshot frame split across two chunks', async () => {
    const stream = createOpenStream()
    const fetchMock = vi.fn().mockResolvedValue(stream.response)
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useConnectionLogStream())
    await waitFor(() => expect(result.current.connected).toBe(true))

    const frame = JSON.stringify(SNAPSHOT)
    const splitAt = Math.floor(frame.length / 2)
    await act(async () => {
      stream.push(`data: ${frame.slice(0, splitAt)}`)
      stream.push(`${frame.slice(splitAt)}\n\n`)
    })

    // A half frame must not be parsed early, and the joined frame must arrive
    // intact even though the network split it.
    await waitFor(() => expect(result.current.snapshot).not.toBeNull())
    expect(result.current.snapshot?.node_name).toBe('node-a')
    expect(result.current.receivedAt).toBeGreaterThan(0)
  })

  test('ignores heartbeat comment frames and keeps streaming', async () => {
    const stream = createOpenStream()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(stream.response))

    const { result } = renderHook(() => useConnectionLogStream())
    await waitFor(() => expect(result.current.connected).toBe(true))

    await act(async () => {
      stream.push(': ping\n\n')
    })
    expect(result.current.snapshot).toBeNull()
    expect(result.current.connected).toBe(true)

    await act(async () => {
      stream.push(`data: ${JSON.stringify(SNAPSHOT)}\n\n`)
    })
    await waitFor(() => expect(result.current.snapshot).not.toBeNull())
  })

  test('falls back to polling after three failed connection attempts', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')))

    const { result } = renderHook(() => useConnectionLogStream())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.connected).toBe(false)
    expect(result.current.fallback).toBe(false)

    // Backoff is 1s, 2s, 4s...: the third failure is what gives up.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(result.current.fallback).toBe(false)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(result.current.fallback).toBe(true)
  })
})
