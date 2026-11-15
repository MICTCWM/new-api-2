// jsdom has no layout engine, so these tests lock the structural contracts of
// the three mobile densities (cards, single-line rows, matrix) rather than
// pixel widths. Real 320px/375px rendering needs a browser check.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, within } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { getConnectionLogs } from '../../api'
import type { ConnectionLogItem, ConnectionLogSnapshot } from '../../types'
import { ConnectionLogsPanel } from '../connection-logs-panel'
import { UsageLogsProvider } from '../usage-logs-provider'

vi.mock('../../api', () => ({ getConnectionLogs: vi.fn() }))

const mockGetConnectionLogs = vi.mocked(getConnectionLogs)
const originalMatchMedia = window.matchMedia
const MOBILE_QUERY = '(max-width: 640px)'
const VIEW_MODE_STORAGE_KEY = 'connection_logs_view_mode'

let originalTranslationBundle: Record<string, string> | undefined

function installMobileMatchMedia() {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (query: string): MediaQueryList => ({
      matches: query === MOBILE_QUERY,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  })
}

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

const COMPLETED_ITEM: ConnectionLogItem = {
  ...BASE_ITEM,
  request_id: 'req-b',
  username: 'bob',
  user_id: 4,
  state: 'completed',
  first_token_at: BASE_ITEM.started_at + 900,
  ttft_ms: 900,
  finished_at: BASE_ITEM.started_at + 4000,
  duration_ms: 4000,
  output_tokens: 512,
  active: false,
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

describe('connection logs mobile densities', () => {
  beforeEach(() => {
    mockGetConnectionLogs.mockReset()
    installMobileMatchMedia()
    window.localStorage.clear()
    originalTranslationBundle = structuredClone(
      i18next.getResourceBundle('en', 'translation') ?? {}
    )
    i18next.addResourceBundle(
      'en',
      'translation',
      {
        'Live connections': 'Live connections',
        'View mode': 'View mode',
        'List view': 'List view',
        Compact: 'Compact',
        'Grid view': 'Grid view',
        Connecting: 'Connecting',
        'Connection completed': 'Connection completed',
        'Waiting for first token': 'Waiting for first token',
        'Streaming output': 'Streaming output',
        'Active connections': 'Active connections',
        'Total input tokens': 'Total input tokens',
        Input: 'Input',
        Output: 'Output',
        User: 'User',
        Refresh: 'Refresh',
        'No connections right now': 'No connections right now',
        'Failed to load logs': 'Failed to load logs',
        'Auto refresh: {{seconds}}s': 'Auto refresh: {{seconds}}s',
        'Live connections on this node': 'Live connections on this node',
        'Retry ×{{count}}': 'Retry ×{{count}}',
        'Retrying in {{seconds}}s': 'Retrying in {{seconds}}s',
        'Go to next page': 'Go to next page',
        'Go to previous page': 'Go to previous page',
      },
      true,
      true
    )
  })

  afterEach(() => {
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: originalMatchMedia,
    })
    window.localStorage.clear()
    vi.useRealTimers()
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

  test('keeps the compressed header and totals strip above the list', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()

    expect(
      await screen.findByRole('button', { name: 'User alice' })
    ).toBeInTheDocument()
    const header = document.querySelector(
      '[data-slot="connection-mobile-header"]'
    )
    expect(header).toHaveClass('flex-row')
    expect(
      within(header as HTMLElement).getByRole('group', {
        name: 'View mode',
      })
    ).toBeInTheDocument()

    const stats = document.querySelector(
      '[data-slot="connection-mobile-stats"]'
    )
    expect(stats).not.toBeNull()
    expect(
      within(stats as HTMLElement).getByTestId('connection-concurrent')
    ).toHaveTextContent('1')
    expect(
      within(stats as HTMLElement).getByTestId('connection-total-input')
    ).toHaveTextContent('1,200')
  })

  test('drops the open-state wording but keeps the completion badge', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM, COMPLETED_ITEM]),
    })

    const { container } = renderPanel()

    await screen.findByRole('button', { name: 'User alice' })
    const activeCard = container.querySelector(
      '[data-connection-request-id="req-a"]'
    ) as HTMLElement
    const completedCard = container.querySelector(
      '[data-connection-request-id="req-b"]'
    ) as HTMLElement

    // The live meter carries the state; only screen readers get the wording.
    expect(
      activeCard.querySelector(
        '[data-slot="connection-mobile-status"] [data-slot="status-badge"]'
      )
    ).toBeNull()
    expect(within(activeCard).getByText('Connecting')).toHaveClass('sr-only')

    expect(
      completedCard.querySelector(
        '[data-slot="connection-mobile-status"] [data-slot="status-badge"]'
      )
    ).not.toBeNull()
    expect(
      within(completedCard).getByText('Connection completed')
    ).not.toHaveClass('sr-only')
  })

  test('switches to single-line compact rows', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM, COMPLETED_ITEM]),
    })

    const { container } = renderPanel()

    fireEvent.click(await screen.findByRole('button', { name: 'Compact' }))

    expect(
      container.querySelectorAll('[data-slot="connection-mobile-card"]')
    ).toHaveLength(0)
    const rows = container.querySelectorAll(
      '[data-slot="connection-compact-row"]'
    )
    expect(rows).toHaveLength(2)
    const activeRow = container.querySelector(
      '[data-connection-request-id="req-a"]'
    ) as HTMLElement
    expect(activeRow).toHaveClass('flex', 'items-center')
    expect(activeRow).toHaveAttribute('title', 'Input 1,200 · Output --')
    expect(activeRow.textContent).toContain('1200')
    expect(activeRow.textContent).toContain('↑')
    expect(activeRow.textContent).toContain('↓')
    expect(activeRow.textContent).toContain('gpt-5.4')
    const modelBadge = activeRow.querySelector('[data-slot="model-badge"]')
    expect(modelBadge).toHaveTextContent('gpt-5.4')
    expect(modelBadge).toHaveClass('max-w-[5rem]')
    expect(within(activeRow).getByText('Connecting')).toHaveClass('sr-only')
    expect(screen.getByRole('button', { name: 'Compact' })).toHaveAttribute(
      'aria-pressed',
      'true'
    )
  })

  test('switches to the matrix and fills each cell with user, state and totals', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM, COMPLETED_ITEM]),
    })

    const { container } = renderPanel()

    fireEvent.click(await screen.findByRole('button', { name: 'Grid view' }))

    const grid = container.querySelector('[data-slot="connection-grid"]')
    expect(grid).not.toBeNull()
    expect(grid).toHaveClass(
      'grid',
      'grid-cols-[repeat(auto-fill,minmax(5rem,1fr))]'
    )
    const cells = container.querySelectorAll(
      '[data-slot="connection-grid-cell"]'
    )
    expect(cells).toHaveLength(2)
    expect(cells[0].textContent).toContain('↑')
    expect(cells[0].textContent).toContain('↓')
    expect(
      screen.getByRole('button', {
        name: 'User alice Connecting Input 1,200 Output --',
      })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', {
        name: 'User bob Connection completed Input 1,200 Output 512',
      })
    ).toBeInTheDocument()
    // The matrix replaces the paged list, so its pager is gone.
    expect(screen.queryByRole('button', { name: 'Go to next page' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Grid view' })).toHaveAttribute(
      'aria-pressed',
      'true'
    )
  })

  test('renders every request in the matrix instead of one page', async () => {
    const items = Array.from({ length: 21 }, (_, index) => ({
      ...BASE_ITEM,
      request_id: `req-${index}`,
      username: `user-${index}`,
    }))
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot(items),
    })

    const { container } = renderPanel()

    fireEvent.click(await screen.findByRole('button', { name: 'Grid view' }))

    expect(
      container.querySelectorAll('[data-slot="connection-grid-cell"]')
    ).toHaveLength(21)
    // 21 rows would need a second page in the card list; the matrix shows all.
    expect(screen.queryByRole('button', { name: 'Go to next page' })).toBeNull()
  })

  test('restores the stored view mode and keeps the toggle in sync', async () => {
    window.localStorage.setItem(VIEW_MODE_STORAGE_KEY, 'grid')
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    const { container } = renderPanel()

    expect(
      await screen.findByRole('button', {
        name: 'User alice Connecting Input 1,200 Output --',
      })
    ).toBeInTheDocument()
    expect(
      container.querySelector('[data-slot="connection-grid"]')
    ).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Grid view' })).toHaveAttribute(
      'aria-pressed',
      'true'
    )
  })

  test('writes the chosen view mode back to storage', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()
    fireEvent.click(await screen.findByRole('button', { name: 'Compact' }))

    expect(window.localStorage.getItem(VIEW_MODE_STORAGE_KEY)).toBe('compact')
  })
})
