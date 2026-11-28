import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { getConnectionLogs } from '../../api'
import type { ConnectionLogItem, ConnectionLogSnapshot } from '../../types'
import { ConnectionLogsPanel } from '../connection-logs-panel'
import { UsageLogsProvider } from '../usage-logs-provider'
import {
  failConnectionLogStream,
  pushConnectionLogSnapshot,
  resetConnectionLogStream,
} from './connection-log-stream-mock'

vi.mock('../../api', () => ({ getConnectionLogs: vi.fn() }))
// The panel is pushed over SSE; these tests inject snapshots as pushed frames.
vi.mock(
  '../../use-connection-log-stream',
  () => import('./connection-log-stream-mock')
)

const mockGetConnectionLogs = vi.mocked(getConnectionLogs)

/**
 * Deep copy of the English bundle before a test injects its fixtures.
 * `getResourceBundle` hands back the live resource object that
 * `addResourceBundle` mutates in place, so a shallow reference would already
 * contain the fixture and the restore would just re-install it.
 */
let originalTranslationBundle: Record<string, string> | undefined

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
  retry_count: 0,
  retry_wait_ms: 0,
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
    resetConnectionLogStream()
    originalTranslationBundle = structuredClone(
      i18next.getResourceBundle('en', 'translation') ?? {}
    )
    i18next.addResourceBundle(
      'en',
      'translation',
      {
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
        Live: 'Live',
        'Reconnecting to live stream': 'Reconnecting to live stream',
        'Auto refresh: {{seconds}}s': 'Auto refresh: {{seconds}}s',
        'Live connections on this node': 'Live connections on this node',
        'Retry ×{{count}}': 'Retry ×{{count}}',
        'Retrying in {{seconds}}s': 'Retrying in {{seconds}}s',
      },
      true,
      true
    )
  })

  afterEach(() => {
    vi.useRealTimers()
    // Drop the injected fixture keys, then re-install the deep copy taken
    // before the fixture was merged in, so the injected keys do not leak.
    i18next.removeResourceBundle('en', 'translation')
    if (originalTranslationBundle) {
      i18next.addResourceBundle(
        'en',
        'translation',
        originalTranslationBundle,
        true,
        true
      )
    }
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

    const { container } = renderPanel()

    expect(await screen.findByText('Connecting')).toBeInTheDocument()
    expect(screen.getByText('Waiting for first token')).toBeInTheDocument()
    expect(
      container.querySelector('[data-slot="connection-beat-meter"]')
    ).not.toBeNull()
    expect(
      container.querySelector('[data-slot="connection-drop-meter"]')
    ).toBeNull()
  })

  test('uses the falling-bar meter for a non-stream connection', async () => {
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

    const { container } = renderPanel()

    expect(await screen.findByText('Connecting')).toBeInTheDocument()
    expect(
      container.querySelector('[data-slot="connection-drop-meter"]')
    ).not.toBeNull()
    expect(
      container.querySelector('[data-slot="connection-beat-meter"]')
    ).toBeNull()
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

  test('drops completed connections after the 60s retention window', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          request_id: 'recent',
          username: 'recent-user',
          state: 'completed',
          active: false,
          started_at: BASE_ITEM.started_at - 30_000,
          finished_at: BASE_ITEM.started_at + 4_000,
          duration_ms: 34_000,
          output_tokens: 512,
        },
        {
          ...BASE_ITEM,
          request_id: 'expired',
          username: 'expired-user',
          state: 'completed',
          active: false,
          started_at: BASE_ITEM.started_at - 90_000,
          finished_at: BASE_ITEM.started_at - 61_000,
          duration_ms: 29_000,
          output_tokens: 512,
        },
      ]),
    })

    renderPanel()

    expect(await screen.findByText('recent-user')).toBeInTheDocument()
    expect(screen.queryByText('expired-user')).not.toBeInTheDocument()
  })

  test('shows the retry count and countdown while a connection is retried', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          state: 'streaming',
          first_token_at: BASE_ITEM.started_at + 900,
          ttft_ms: 900,
          retry_count: 2,
          retry_wait_ms: 1500,
        },
      ]),
    })

    renderPanel()

    expect(await screen.findByText('Retry ×2')).toBeInTheDocument()
    expect(screen.getByText('Retrying in 2s')).toBeInTheDocument()
  })

  test('keeps the status cell identical for a finished connection', async () => {
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

    const { container } = renderPanel()

    expect(await screen.findByText('Connection completed')).toBeInTheDocument()
    // The live meter is replaced by a green check, and the fixed column layout
    // keeps that swap from resizing the table.
    expect(
      container.querySelector('[data-slot="connection-beat-meter"]')
    ).toBeNull()
    expect(
      container.querySelector('[data-slot="connection-done-check"]')
    ).not.toBeNull()
    expect(container.querySelector('table')?.className).toContain('table-fixed')
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

  test('labels the panel as live instead of showing a polling hint', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()

    expect(await screen.findByText('alice')).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()
    expect(screen.queryByText('Auto refresh: 5s')).not.toBeInTheDocument()
  })

  test('shows a new request as soon as its frame arrives', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()
    expect(await screen.findByText('alice')).toBeInTheDocument()

    // No polling involved: the pushed frame alone must add the row.
    act(() => {
      pushConnectionLogSnapshot(
        snapshot([
          BASE_ITEM,
          {
            ...BASE_ITEM,
            request_id: 'req-fresh',
            username: 'dave',
            started_at: BASE_ITEM.started_at + 2000,
          },
        ])
      )
    })

    expect(await screen.findByText('dave')).toBeInTheDocument()
    expect(screen.getByText('alice')).toBeInTheDocument()
    expect(screen.getByTestId('connection-concurrent')).toHaveTextContent('2')
  })

  test('polls the snapshot endpoint when the live stream gave up', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()
    expect(await screen.findByText('alice')).toBeInTheDocument()

    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([{ ...BASE_ITEM, username: 'polled-user' }]),
    })
    act(() => {
      failConnectionLogStream()
    })

    // The degraded panel keeps updating from the rate-limit-exempt snapshot
    // endpoint instead of leaving the admin with a frozen list.
    expect(await screen.findByText('polled-user')).toBeInTheDocument()
    expect(screen.getByText('Auto refresh: 10s')).toBeInTheDocument()
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
