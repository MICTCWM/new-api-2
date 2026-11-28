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
import { useEffect, useState } from 'react'

import { useAuthStore } from '@/stores/auth-store'

import type { ConnectionLogSnapshot } from './types'

const STREAM_ENDPOINT = '/api/connection/logs/stream'
const INITIAL_RETRY_DELAY_MS = 1000
const MAX_RETRY_DELAY_MS = 30_000
/** Reconnects before the panel gives up on the stream and polls instead. */
const FALLBACK_FAILURE_THRESHOLD = 3

export interface ConnectionLogStreamState {
  snapshot: ConnectionLogSnapshot | null
  /** Client clock when the newest snapshot arrived, for the server offset. */
  receivedAt: number | null
  connected: boolean
  /** True once the stream keeps failing; the panel then polls as a backstop. */
  fallback: boolean
}

interface SSEFrameSplit {
  frames: string[]
  rest: string
}

/** Splits complete `\n\n`-terminated frames out of a partially received buffer. */
function splitSSEFrames(buffer: string): SSEFrameSplit {
  const frames: string[] = []
  let rest = buffer
  let boundary = rest.indexOf('\n\n')
  while (boundary >= 0) {
    frames.push(rest.slice(0, boundary))
    rest = rest.slice(boundary + 2)
    boundary = rest.indexOf('\n\n')
  }
  return { frames, rest }
}

/** Returns the frame payload, or null for heartbeat comments (`: ping`). */
function readSSEData(frame: string): string | null {
  const data: string[] = []
  for (const line of frame.split('\n')) {
    if (!line.startsWith('data:')) continue
    const value = line.slice('data:'.length)
    data.push(value.startsWith(' ') ? value.slice(1) : value)
  }
  if (data.length === 0) return null
  return data.join('\n')
}

/**
 * Streams the live connection snapshot from the server. A native EventSource
 * cannot send the dashboard's Authorization header, so the stream is read with
 * fetch and parsed by hand.
 */
export function useConnectionLogStream(): ConnectionLogStreamState {
  const [snapshot, setSnapshot] = useState<ConnectionLogSnapshot | null>(null)
  const [receivedAt, setReceivedAt] = useState<number | null>(null)
  const [connected, setConnected] = useState(false)
  const [fallback, setFallback] = useState(false)

  useEffect(() => {
    let disposed = false
    let failures = 0
    let retryDelay = INITIAL_RETRY_DELAY_MS
    let controller: AbortController | null = null
    let retryTimer: ReturnType<typeof setTimeout> | null = null

    const scheduleReconnect = (countAsFailure: boolean) => {
      if (disposed) return
      const delay = countAsFailure ? retryDelay : INITIAL_RETRY_DELAY_MS
      if (countAsFailure) {
        failures += 1
        if (failures >= FALLBACK_FAILURE_THRESHOLD) setFallback(true)
        retryDelay = Math.min(retryDelay * 2, MAX_RETRY_DELAY_MS)
      }
      retryTimer = setTimeout(() => {
        retryTimer = null
        void connect()
      }, delay)
    }

    /** Applies one complete frame; reports whether it carried a snapshot. */
    const applyFrame = (frame: string): boolean => {
      const payload = readSSEData(frame)
      if (payload == null) return false
      let next: ConnectionLogSnapshot
      try {
        next = JSON.parse(payload) as ConnectionLogSnapshot
      } catch {
        return false
      }
      if (disposed) return false
      setSnapshot(next)
      setReceivedAt(Date.now())
      failures = 0
      retryDelay = INITIAL_RETRY_DELAY_MS
      setFallback(false)
      return true
    }

    const connect = async () => {
      if (disposed) return
      const accessToken = useAuthStore.getState().auth.accessToken
      const headers: Record<string, string> = { Accept: 'text/event-stream' }
      if (accessToken) headers.Authorization = `Bearer ${accessToken}`

      controller = new AbortController()
      let buffer = ''
      let delivered = false
      try {
        const response = await fetch(STREAM_ENDPOINT, {
          headers,
          credentials: 'include',
          cache: 'no-store',
          signal: controller.signal,
        })
        if (!response.ok) throw new Error(`stream failed: ${response.status}`)
        if (!response.body) throw new Error('stream has no body')

        setConnected(true)

        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        while (!disposed) {
          const { done, value } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          const { frames, rest } = splitSSEFrames(buffer)
          buffer = rest
          for (const frame of frames) {
            if (applyFrame(frame)) delivered = true
          }
        }
      } catch {
        // Aborted or network failure: the reconnect below handles both.
      }

      if (disposed) return
      setConnected(false)
      // A stream that already delivered frames was healthy, so its end is a
      // plain reconnect; only a stream that never delivered counts as failure.
      scheduleReconnect(!delivered)
    }

    void connect()

    return () => {
      disposed = true
      controller?.abort()
      if (retryTimer != null) clearTimeout(retryTimer)
    }
  }, [])

  return { snapshot, receivedAt, connected, fallback }
}
