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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { getConnectionLogs } from '../../api'
import type { ConnectionLogItem, ConnectionLogSnapshot } from '../../types'
import { ConnectionLogsPanel } from '../connection-logs-panel'
import { UsageLogsProvider } from '../usage-logs-provider'

vi.mock('../../api', () => ({ getConnectionLogs: vi.fn() }))

const mockGetConnectionLogs = vi.mocked(getConnectionLogs)

const BASE_ITEM: ConnectionLogItem = {
  request_id: 'req-a',
  username: 'alice',
  user_id: 3,
  group: 'default',
  model: 'gpt-5.4',
  stream: true,
  channel_id: 12,
  state: 'waiting_first_token',
  started_at: 1_700_000_000_000,
  first_token_at: null,
  finished_at: null,
  ttft_ms: null,
  duration_ms: 0,
  input_tokens: 1200,
  output_tokens: null,
  stream_chunks: 0,
  active: true,
}

function snapshot(
  items: ConnectionLogItem[],
  overrides: Partial<ConnectionLogSnapshot> = {}
): ConnectionLogSnapshot {
  return {
    concurrent: items.filter((item) => item.active).length,
    total_input_tokens: items
      .filter((item) => item.active)
      .reduce((sum, item) => sum + item.input_tokens, 0),
    server_time: 1_700_000_005_000,
    node_name: 'node-a',
    items,
    ...overrides,
  }
}

function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={client}>
      <UsageLogsProvider>
        <ConnectionLogsPanel />
      </UsageLogsProvider>
    </QueryClientProvider>
  )
}

describe('connection logs panel', () => {
  beforeEach(() => {
    mockGetConnectionLogs.mockReset()
    i18next.addResourceBundle('en', 'translation', {
      'Connection Logs': 'Connection Logs',
      Connecting: 'Connecting',
      'Connection completed': 'Connection completed',
      'Waiting for first token': 'Waiting for first token',
      'Streaming output': 'Streaming output',
      'Waiting for response': 'Waiting for response',
      'Active connections': 'Active connections',
      'Total input tokens': 'Total input tokens',
      'Input tokens': 'Input tokens',
      'Output tokens': 'Output tokens',
      TTFT: 'TTFT',
      Duration: 'Duration',
      'No connections right now': 'No connections right now',
      'Failed to load logs': 'Failed to load logs',
      Refresh: 'Refresh',
      'Auto refresh: {{seconds}}s': 'Auto refresh: {{seconds}}s',
      'Live connections on this node': 'Live connections on this node',
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  test('shows concurrency and total input tokens from the snapshot', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        BASE_ITEM,
        {
          ...BASE_ITEM,
          request_id: 'req-b',
          input_tokens: 800,
          started_at: BASE_ITEM.started_at + 500,
        },
      ]),
    })

    renderPanel()

    // The total waits for the loaded value; concurrency is read from the stat.
    expect(await screen.findByText('2,000')).toBeInTheDocument()
    expect(screen.getByTestId('connection-concurrent')).toHaveTextContent('2')
  })

  test('labels an open stream with the waiting sub-state', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()

    expect(await screen.findByText('Connecting')).toBeInTheDocument()
    expect(screen.getByText('Waiting for first token')).toBeInTheDocument()
  })

  test('keeps output tokens and TTFT as placeholders until the first token', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()

    expect(await screen.findByText('alice')).toBeInTheDocument()
    expect(screen.getByText('TTFT')).toBeInTheDocument()
    expect(screen.getAllByText('--')).toHaveLength(2)
  })

  test('reveals TTFT while output tokens stay hidden during a stream', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          state: 'streaming',
          first_token_at: BASE_ITEM.started_at + 900,
          ttft_ms: 900,
          stream_chunks: 7,
        },
      ]),
    })

    renderPanel()

    expect(await screen.findByText('Streaming output')).toBeInTheDocument()
    expect(screen.getByText('0.9s')).toBeInTheDocument()
    expect(screen.getAllByText('--')).toHaveLength(1)
  })

  test('reveals output tokens once the connection completes', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          state: 'completed',
          first_token_at: BASE_ITEM.started_at + 900,
          ttft_ms: 900,
          finished_at: BASE_ITEM.started_at + 4000,
          duration_ms: 4000,
          output_tokens: 512,
          active: false,
        },
      ]),
    })

    renderPanel()

    expect(await screen.findByText('Connection completed')).toBeInTheDocument()
    expect(await screen.findByText('512')).toBeInTheDocument()
    expect(screen.queryByText('--')).not.toBeInTheDocument()
  })

  test('shows the final duration for a finished request', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          state: 'completed',
          finished_at: BASE_ITEM.started_at + 4000,
          duration_ms: 4000,
          output_tokens: 512,
          active: false,
        },
      ]),
    })

    renderPanel()

    expect(await screen.findByText('4.0s')).toBeInTheDocument()
  })

  test('keeps ticking the elapsed duration of an open request', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(1_700_000_135_000))
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM], {
        server_time: BASE_ITEM.started_at + 125_000,
      }),
    })

    renderPanel()
    // Deliver the snapshot by advancing the clock instead of polling real
    // timers, which stay paused under fake timers.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100)
    })
    expect(screen.getByText('2m 5s')).toBeInTheDocument()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000)
    })

    expect(screen.getByText('2m 8s')).toBeInTheDocument()
  })

  test('omits the TTFT placeholder for non-stream requests', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          stream: false,
          state: 'waiting_response',
          ttft_ms: null,
        },
      ]),
    })

    renderPanel()

    expect(await screen.findByText('Connecting')).toBeInTheDocument()
    expect(screen.getAllByText('--')).toHaveLength(1)
  })

  test('shows non-stream connections with the main status only', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          stream: false,
          state: 'waiting_response',
        },
      ]),
    })

    renderPanel()

    expect(await screen.findByText('Connecting')).toBeInTheDocument()
    expect(screen.queryByText('Waiting for response')).not.toBeInTheDocument()
  })

  test('lists the newest request above older ones', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        BASE_ITEM,
        {
          ...BASE_ITEM,
          request_id: 'req-b',
          username: 'bob',
          started_at: BASE_ITEM.started_at + 1000,
        },
      ]),
    })

    renderPanel()

    const rows = await screen.findAllByRole('row')
    expect(rows).toHaveLength(3)
    expect(rows[1]).toHaveTextContent('bob')
    expect(rows[2]).toHaveTextContent('alice')
  })

  test('enters request rows from the right', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()

    const rows = await screen.findAllByRole('row')
    expect(rows[1]).toHaveClass('slide-in-from-right-4')
  })

  test('refetches the snapshot when the refresh control is clicked', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()
    expect(await screen.findByText('alice')).toBeInTheDocument()

    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([{ ...BASE_ITEM, request_id: 'req-b', username: 'bob' }]),
    })
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))

    expect(await screen.findByText('bob')).toBeInTheDocument()
    expect(screen.queryByText('alice')).not.toBeInTheDocument()
  })

  test('shows the five second auto refresh hint', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()

    expect(await screen.findByText('Auto refresh: 5s')).toBeInTheDocument()
  })

  test('renders the empty state when nothing is connected', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([], { concurrent: 0, total_input_tokens: 0 }),
    })

    renderPanel()

    expect(
      await screen.findByText('No connections right now')
    ).toBeInTheDocument()
  })

  test('reports a load failure instead of an empty connection list', async () => {
    mockGetConnectionLogs.mockRejectedValue(new Error('network down'))

    renderPanel()

    expect(await screen.findByText('Failed to load logs')).toBeInTheDocument()
    expect(
      screen.queryByText('No connections right now')
    ).not.toBeInTheDocument()
  })
})
