// jsdom has no layout engine, so these tests can only lock structural
// contracts (classes that make a row shrink, truncate or wrap). Real pixel
// widths at 320px/375px need a browser or manual device check.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { getConnectionLogs } from '../../api'
import type { ConnectionLogItem, ConnectionLogSnapshot } from '../../types'
import { ConnectionLogsPanel } from '../connection-logs-panel'
import { UsageLogsProvider } from '../usage-logs-provider'

vi.mock('../../api', () => ({ getConnectionLogs: vi.fn() }))

const mockGetConnectionLogs = vi.mocked(getConnectionLogs)
const originalMatchMedia = window.matchMedia

/**
 * Deep copy of the English bundle before a test injects its fixtures.
 * `getResourceBundle` hands back the live resource object that
 * `addResourceBundle` mutates in place, so a shallow reference would already
 * contain the fixture and the restore would just re-install it.
 */
let originalTranslationBundle: Record<string, string> | undefined

const MOBILE_QUERY = '(max-width: 640px)'
const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)'

type ChangeListener = (event: MediaQueryListEvent) => void

const mediaQueries = new Map<
  string,
  { matches: boolean; listeners: Set<ChangeListener> }
>()

function getMediaEntry(query: string) {
  let entry = mediaQueries.get(query)
  if (!entry) {
    entry = { matches: false, listeners: new Set() }
    mediaQueries.set(query, entry)
  }
  return entry
}

/**
 * Records `change` listeners so a test can flip a breakpoint and notify the
 * `useSyncExternalStore` subscription behind `useMediaQuery`.
 * `(prefers-reduced-motion: reduce)` stays false so motion keeps rendering.
 */
function installMobileMatchMedia() {
  mediaQueries.clear()
  getMediaEntry(MOBILE_QUERY).matches = true
  getMediaEntry(REDUCED_MOTION_QUERY).matches = false
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (query: string): MediaQueryList => {
      const entry = getMediaEntry(query)
      return {
        get matches() {
          return entry.matches
        },
        media: query,
        onchange: null,
        addListener: (listener: ChangeListener) => {
          entry.listeners.add(listener)
        },
        removeListener: (listener: ChangeListener) => {
          entry.listeners.delete(listener)
        },
        addEventListener: (
          type: string,
          listener: EventListenerOrEventListenerObject
        ) => {
          if (type === 'change' && typeof listener === 'function') {
            entry.listeners.add(listener as ChangeListener)
          }
        },
        removeEventListener: (
          type: string,
          listener: EventListenerOrEventListenerObject
        ) => {
          if (type === 'change' && typeof listener === 'function') {
            entry.listeners.delete(listener as ChangeListener)
          }
        },
        dispatchEvent: () => false,
      }
    },
  })
}

/** Flips a breakpoint and dispatches `change`, like a real viewport resize. */
function setQueryMatches(query: string, matches: boolean) {
  const entry = getMediaEntry(query)
  entry.matches = matches
  const event = { matches, media: query } as MediaQueryListEvent
  for (const listener of entry.listeners) listener(event)
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

describe('connection logs mobile cards', () => {
  beforeEach(() => {
    mockGetConnectionLogs.mockReset()
    installMobileMatchMedia()
    originalTranslationBundle = structuredClone(
      i18next.getResourceBundle('en', 'translation') ?? {}
    )
    i18next.addResourceBundle(
      'en',
      'translation',
      {
        Connecting: 'Connecting',
        'Connection completed': 'Connection completed',
        'Waiting for first token': 'Waiting for first token',
        'Streaming output': 'Streaming output',
        'Active connections': 'Active connections',
        'Total input tokens': 'Total input tokens',
        Input: 'Input',
        Output: 'Output',
        'Input tokens': 'Input tokens',
        'Output tokens': 'Output tokens',
        TTFT: 'TTFT',
        Duration: 'Duration',
        User: 'User',
        'No connections right now': 'No connections right now',
        Refresh: 'Refresh',
        'Auto refresh: {{seconds}}s': 'Auto refresh: {{seconds}}s',
        'Live connections on this node': 'Live connections on this node',
        'Retry ×{{count}}': 'Retry ×{{count}}',
        'Retrying in {{seconds}}s': 'Retrying in {{seconds}}s',
        'Total:': 'Total:',
        'Go to previous page': 'Go to previous page',
        'Go to next page': 'Go to next page',
        'Go to first page': 'Go to first page',
        'Go to last page': 'Go to last page',
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
    mediaQueries.clear()
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

  test('renders the card list instead of the horizontally scrolling table', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    const { container } = renderPanel()

    expect(
      await screen.findByRole('button', { name: 'User alice' })
    ).toBeInTheDocument()
    // The avatar is the only user affordance: the name is not rendered.
    expect(screen.queryByText('alice')).toBeNull()
    expect(container.querySelector('table')).toBeNull()
    expect(
      container.querySelector('[data-connection-request-id="req-a"]')
    ).not.toBeNull()
  })

  test('shows the model logo, arrow tokens, TTFT seconds and total duration', async () => {
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

    const card = await screen.findByRole('listitem')
    expect(within(card).getByRole('img', { name: 'gpt-5.4' })).toHaveAttribute(
      'data-slot',
      'model-icon'
    )
    expect(card.textContent).not.toMatch(/\d{4}-\d{2}-\d{2}/)
    // Mobile rows intentionally omit the start timestamp to leave room for the
    // model and state; only the elapsed duration remains.
    expect(
      card.querySelector('[data-slot="connection-mobile-time"]')
    ).toBeNull()
    const metrics = card.querySelector(
      '[data-slot="connection-mobile-metrics"]'
    )
    expect(metrics).toHaveTextContent('Input 1,200')
    expect(metrics).toHaveTextContent('Output 512')
    expect(metrics).toHaveTextContent('↑')
    expect(metrics).toHaveTextContent('↓')
    expect(metrics).toHaveTextContent('TTFT 0.9s')
    expect(metrics).toHaveTextContent('4.0s')
    const timeGroup = metrics?.querySelector(
      '[data-slot="connection-mobile-time-group"]'
    ) as HTMLElement
    expect(timeGroup).not.toBeNull()
    expect(
      timeGroup.querySelector('[data-slot="connection-mobile-time-divider"]')
    ).not.toBeNull()
    expect(within(timeGroup).getByText('TTFT')).toHaveClass('sr-only')
    expect(within(timeGroup).getByText('0.9s')).toBeInTheDocument()
    expect(within(timeGroup).getByText('4.0s')).toBeInTheDocument()
    // Route and channel are no longer part of the compact card.
    expect(card.textContent).not.toContain('default')
    expect(card.textContent).not.toContain('#12')
  })

  test('reads -- for output tokens and TTFT until the first token arrives', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        { ...BASE_ITEM, state: 'waiting_first_token', ttft_ms: null },
      ]),
    })

    renderPanel()

    const card = await screen.findByRole('listitem')
    const metrics = card.querySelector(
      '[data-slot="connection-mobile-metrics"]'
    )
    expect(metrics).toHaveTextContent('Input 1,200')
    expect(metrics).toHaveTextContent('Output --')
    expect(metrics).toHaveTextContent('TTFT --')
  })

  test('keeps the beat meter for an active streaming connection', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([{ ...BASE_ITEM, state: 'streaming', ttft_ms: 900 }]),
    })

    const { container } = renderPanel()

    expect(await screen.findByText('Connecting')).toBeInTheDocument()
    expect(
      container.querySelector('[data-slot="connection-beat-meter"]')
    ).not.toBeNull()
    expect(
      container.querySelector('[data-slot="connection-drop-meter"]')
    ).toBeNull()
  })

  test('keeps the drop meter for an active non-stream connection', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        { ...BASE_ITEM, stream: false, state: 'waiting_response' },
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

  test('shows the completion check and hides the live meter for a finished card', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          state: 'completed',
          duration_ms: 4000,
          output_tokens: 512,
          active: false,
        },
      ]),
    })

    const { container } = renderPanel()

    expect(await screen.findByText('Connection completed')).toBeInTheDocument()
    expect(
      container.querySelector('[data-slot="connection-done-check"]')
    ).not.toBeNull()
    expect(
      container.querySelector('[data-slot="connection-beat-meter"]')
    ).toBeNull()
  })

  test('shows the retry badge and countdown on the card', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          state: 'streaming',
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

  test('shows only the model logo and keeps the status cluster tight', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([BASE_ITEM]),
    })

    renderPanel()

    const card = await screen.findByRole('listitem')
    expect(card).toHaveClass('overflow-hidden')
    const firstLine = card.firstElementChild
    expect(firstLine).toHaveClass('min-w-0')
    const modelIcon = within(card).getByRole('img', { name: 'gpt-5.4' })
    expect(modelIcon).toHaveClass('size-5', 'shrink-0')
    expect(
      modelIcon.querySelector('[data-slot="model-provider-icon"]')
    ).not.toBeNull()
    // The name is an accessible label only, never visible row text.
    expect(card.textContent).not.toContain('gpt-5.4')
    // The status cluster absorbs the remaining width and truncates instead of
    // pushing the card past the viewport.
    const statusCluster = card.querySelector(
      '[data-slot="connection-mobile-status"]'
    )
    expect(statusCluster).toHaveClass('ml-auto', 'min-w-0', 'shrink')
    expect(
      card.querySelector('[data-slot="connection-beat-meter"]')?.parentElement
    ).toHaveClass('shrink-0')
    const metrics = card.querySelector(
      '[data-slot="connection-mobile-metrics"]'
    )
    expect(metrics).toHaveClass('flex-wrap', 'min-w-0')
  })

  test('uses a generic model icon when the provider is unknown', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([{ ...BASE_ITEM, model: 'custom-model' }]),
    })

    renderPanel()

    const card = await screen.findByRole('listitem')
    const modelIcon = within(card).getByRole('img', { name: 'custom-model' })
    expect(
      modelIcon.querySelector('[data-slot="model-provider-icon"]')
    ).toBeNull()
    expect(
      modelIcon.querySelector('[data-slot="model-fallback-icon"]')
    ).not.toBeNull()
  })

  test('drops the desktop sub-state line from the card', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([{ ...BASE_ITEM, state: 'waiting_first_token' }]),
    })

    renderPanel()

    await screen.findByRole('listitem')
    expect(screen.queryByText('Waiting for first token')).toBeNull()
    expect(screen.queryByText('Streaming output')).toBeNull()
  })

  test('renders the numeric user id when the username is empty', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([{ ...BASE_ITEM, username: '', user_id: 42 }]),
    })

    renderPanel()

    const card = await screen.findByRole('listitem')
    expect(within(card).getByText('#42')).toBeInTheDocument()
    expect(
      card.querySelector('[data-slot="connection-mobile-metrics"]')
    ).toHaveTextContent('Input 1,200')
  })

  test('clips a long numeric user id inside the avatar slot', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([{ ...BASE_ITEM, username: '', user_id: 123456 }]),
    })

    renderPanel()

    const card = await screen.findByRole('listitem')
    const fallback = card.querySelector(
      '[data-slot="connection-user-fallback"]'
    )
    expect(fallback).toHaveTextContent('#123456')
    // The slot is fixed at 20px; clipping and truncating keep the digits from
    // overlapping the model badge next to it.
    expect(fallback).toHaveClass('size-5', 'overflow-hidden')
    expect(within(fallback as HTMLElement).getByText('#123456')).toHaveClass(
      'truncate',
      // `min-w-0` is what lets `truncate` actually clip inside the flex slot.
      'min-w-0'
    )
  })

  test('wraps the retry info onto the metrics line while the model keeps a readable width', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        {
          ...BASE_ITEM,
          state: 'streaming',
          ttft_ms: 900,
          retry_count: 2,
          retry_wait_ms: 1500,
        },
      ]),
    })

    renderPanel()

    const retryBadge = await screen.findByText('Retry ×2')
    expect(screen.getByText('Retrying in 2s')).toBeInTheDocument()
    const card = screen.getByRole('listitem')
    expect(card).toHaveClass('overflow-hidden')
    const metrics = card.querySelector(
      '[data-slot="connection-mobile-metrics"]'
    )
    expect(metrics).toHaveClass('flex-wrap', 'min-w-0')
    // Retry info shares the wrapped metrics line instead of shrinking the row.
    expect(metrics).toContainElement(retryBadge)
    // The model logo stays fixed while the metrics wrap.
    expect(screen.getByRole('img', { name: 'gpt-5.4' })).toHaveClass('shrink-0')
  })

  test('renders the shared empty state when nothing is connected', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([], { concurrent: 0, total_input_tokens: 0 }),
    })

    const { container } = renderPanel()

    expect(
      await screen.findByText('No connections right now')
    ).toBeInTheDocument()
    const emptySlot = container.querySelector(
      '[data-slot="connection-mobile-empty"]'
    )
    expect(emptySlot).not.toBeNull()
    expect(emptySlot).toContainElement(
      screen.getByText('No connections right now')
    )
    expect(emptySlot).toHaveClass('h-full', 'items-center', 'justify-center')
    expect(screen.queryByRole('list')).toBeNull()
  })

  test('exposes one list item per connection inside the card list', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        BASE_ITEM,
        {
          ...BASE_ITEM,
          request_id: 'req-b',
          username: 'bob',
          started_at: BASE_ITEM.started_at + 500,
        },
      ]),
    })

    renderPanel()

    expect(
      await screen.findByRole('button', { name: 'User bob' })
    ).toBeInTheDocument()
    const list = screen.getByRole('list')
    expect(within(list).getAllByRole('listitem')).toHaveLength(2)
  })

  test('keeps the pagination controls below the card list', async () => {
    const items = Array.from({ length: 21 }, (_, index) => ({
      ...BASE_ITEM,
      request_id: `req-${index}`,
      username: `user-${index}`,
    }))
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot(items),
    })

    renderPanel()

    expect(
      await screen.findByRole('button', { name: 'User user-20' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Go to next page' })
    ).toBeInTheDocument()
  })

  test('keeps the current page when switching between desktop and mobile', async () => {
    const items = Array.from({ length: 21 }, (_, index) => ({
      ...BASE_ITEM,
      request_id: `req-${index}`,
      username: `user-${index}`,
    }))
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot(items),
    })

    // Desktop first: the breakpoint starts unmatched.
    act(() => setQueryMatches(MOBILE_QUERY, false))
    const { container } = renderPanel()

    expect(await screen.findByText('user-20')).toBeInTheDocument()
    expect(container.querySelector('table')).not.toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Go to next page' }))
    // Page 2 holds only the oldest request once the newest-first list is paged.
    expect(await screen.findByText('user-0')).toBeInTheDocument()
    expect(screen.queryByText('user-20')).toBeNull()

    act(() => setQueryMatches(MOBILE_QUERY, true))

    expect(container.querySelector('table')).toBeNull()
    expect(screen.getByRole('list')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'User user-0' })
    ).toBeInTheDocument()
    expect(screen.queryByText('user-20')).toBeNull()

    act(() => setQueryMatches(MOBILE_QUERY, false))

    expect(container.querySelector('table')).not.toBeNull()
    expect(screen.getByText('user-0')).toBeInTheDocument()
    expect(screen.queryByText('user-20')).toBeNull()
  })

  test('drops TTFT for a non-stream request', async () => {
    mockGetConnectionLogs.mockResolvedValue({
      success: true,
      data: snapshot([
        { ...BASE_ITEM, stream: false, state: 'waiting_response' },
      ]),
    })

    renderPanel()

    expect(await screen.findByText('Connecting')).toBeInTheDocument()
    const card = screen.getByRole('listitem')
    const metrics = card.querySelector(
      '[data-slot="connection-mobile-metrics"]'
    )
    expect(metrics).not.toBeNull()
    expect(metrics?.textContent).not.toContain('TTFT')
  })

  test('keeps ticking the elapsed duration of an open card', async () => {
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
})
