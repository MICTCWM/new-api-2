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
// Test double for the SSE hook. The panel receives pushed snapshots, so tests
// emit them directly instead of driving a real stream (fetch, timing, auth).
import { useEffect, useReducer } from 'react'

import type { ConnectionLogSnapshot } from '../../types'
import type { ConnectionLogStreamState } from '../../use-connection-log-stream'

const streamState: ConnectionLogStreamState = {
  snapshot: null,
  receivedAt: null,
  connected: true,
  fallback: false,
}

const listeners = new Set<() => void>()

export function useConnectionLogStream(): ConnectionLogStreamState {
  const [, rerender] = useReducer((tick: number) => tick + 1, 0)
  useEffect(() => {
    listeners.add(rerender)
    return () => {
      listeners.delete(rerender)
    }
  }, [rerender])
  return streamState
}

/** Pushes a snapshot frame, as the stream would on a connection change. */
export function pushConnectionLogSnapshot(
  snapshot: ConnectionLogSnapshot,
  receivedAt = Date.now() + 1000
): void {
  streamState.snapshot = snapshot
  streamState.receivedAt = receivedAt
  streamState.connected = true
  streamState.fallback = false
  for (const listener of listeners) listener()
}

/** Marks the stream as given up, so the panel falls back to polling. */
export function failConnectionLogStream(): void {
  streamState.snapshot = null
  streamState.receivedAt = null
  streamState.connected = false
  streamState.fallback = true
  for (const listener of listeners) listener()
}

export function resetConnectionLogStream(): void {
  streamState.snapshot = null
  streamState.receivedAt = null
  streamState.connected = true
  streamState.fallback = false
}
