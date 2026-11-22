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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { CHANNEL_TYPE_COMMAND_GO } from '../../constants'
import type { Channel } from '../../types'
import { BalanceCell, CodeProviderUsageCell } from '../channels-columns'
import { ChannelsProvider } from '../channels-provider'

type ApiMethod = (url: string, config?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; post: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post

const commandGoChannel = {
  id: 63,
  type: CHANNEL_TYPE_COMMAND_GO,
  name: 'Command Go pool channel',
  status: 1,
  balance: 0,
  used_quota: 0,
} as unknown as Channel

afterEach(() => {
  apiClient.get = originalGet
  apiClient.post = originalPost
})

function renderWithQueryClient(node: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>{node}</QueryClientProvider>
  )
}

describe('Command Go channel cells', () => {
  test('does not fetch command usage for type 63 channels', async () => {
    const getSpy = vi.fn(async () => ({ data: { success: true } }))
    apiClient.get = getSpy

    renderWithQueryClient(<CodeProviderUsageCell channel={commandGoChannel} />)

    expect(
      await screen.findByLabelText('Manage accounts in Pool Management')
    ).toBeVisible()
    expect(getSpy).not.toHaveBeenCalled()
  })

  test('refreshes the account pool instead of the generic balance endpoint for type 63 channels', async () => {
    const getSpy = vi.fn(async (url: string) => {
      throw new Error(`Unexpected GET ${url}`)
    })
    const postSpy = vi.fn(async (url: string) => {
      if (url === '/api/channel/63/commandgo/accounts/refresh') {
        return { data: { success: true, data: { refreshed: 1, failed: [] } } }
      }
      throw new Error(`Unexpected POST ${url}`)
    })
    apiClient.get = getSpy
    apiClient.post = postSpy

    const { container } = renderWithQueryClient(
      <ChannelsProvider>
        <BalanceCell channel={commandGoChannel} />
      </ChannelsProvider>
    )

    const remainingBadge = container.querySelector('.cursor-pointer')
    expect(remainingBadge).not.toBeNull()
    fireEvent.click(remainingBadge as Element)

    await waitFor(() => {
      expect(postSpy).toHaveBeenCalledWith(
        '/api/channel/63/commandgo/accounts/refresh',
        {},
        expect.anything()
      )
    })
    // The generic balance update path must not be used for type 63.
    expect(getSpy).not.toHaveBeenCalled()
  })
})
