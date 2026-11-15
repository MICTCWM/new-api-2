import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import type { ConnectionLogItem } from '../../types'
import { ConnectionUserButton } from '../connection-log-parts'
import { UsageLogsProvider, useUsageLogsContext } from '../usage-logs-provider'

const ITEM: ConnectionLogItem = {
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

function SensitiveToggle() {
  const { sensitiveVisible, setSensitiveVisible } = useUsageLogsContext()
  return (
    <button
      type='button'
      onClick={() => setSensitiveVisible(!sensitiveVisible)}
    >
      toggle sensitivity
    </button>
  )
}

function renderUserButton() {
  return render(
    <UsageLogsProvider>
      <SensitiveToggle />
      <ConnectionUserButton item={ITEM} />
    </UsageLogsProvider>
  )
}

describe('connection user button', () => {
  test('names the user button and hides the decorative avatar from assistive tech', () => {
    const { container } = renderUserButton()

    expect(
      screen.getByRole('button', { name: 'User alice' })
    ).toBeInTheDocument()
    expect(
      container.querySelector('[data-slot="avatar"]')
    ).toHaveAttribute('aria-hidden', 'true')
  })

  test('masks the username but keeps an accessible name when sensitive data is hidden', () => {
    renderUserButton()

    fireEvent.click(screen.getByRole('button', { name: 'toggle sensitivity' }))

    expect(screen.getByText('••••')).toBeInTheDocument()
    expect(screen.queryByText('alice')).toBeNull()
    expect(
      screen.getByRole('button', { name: 'User ••••' })
    ).toBeInTheDocument()
  })
})
