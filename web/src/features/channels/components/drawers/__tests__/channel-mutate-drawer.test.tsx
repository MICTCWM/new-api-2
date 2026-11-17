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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

const { QueryClient, QueryClientProvider } = await import(
  '@tanstack/react-query'
)
const { api } = await import('@/lib/api')
const { ChannelsProvider } = await import('../../channels-provider')
const { ChannelMutateDrawer } = await import('../channel-mutate-drawer')

type ApiMethod = (url: string, config?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; post: ApiMethod; put: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get

afterEach(() => {
  apiClient.get = originalGet
})

function installApiFixtures() {
  apiClient.get = async (url: string) => {
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    if (url === '/api/channel/models') {
      return { data: { success: true, data: [] } }
    }
    if (url.startsWith('/api/prefill_group')) {
      return { data: { success: true, data: [] } }
    }
    if (url === '/api/channel') {
      return {
        data: { success: true, data: { items: [], fallback_channels: [] } },
      }
    }
    if (url === '/api/user/2fa/status' || url === '/api/user/passkey') {
      return { data: { success: true, data: { enabled: false } } }
    }
    throw new Error(`Unexpected GET ${url}`)
  }
}

function renderCreateDrawer() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  return render(
    <QueryClientProvider client={queryClient}>
      <ChannelsProvider>
        <ChannelMutateDrawer open onOpenChange={() => undefined} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
}

describe('ChannelMutateDrawer simulated cache creation switch', () => {
  test(
    'renders the switch off by default inside Advanced Settings and toggles on click',
    async () => {
      installApiFixtures()
      renderCreateDrawer()

      // Expand the Advanced Settings accordion; the switch lives in its
      // Extra Settings subsection, next to the station cache toggle.
      const advancedTrigger = await screen.findByRole(
        'button',
        { name: /Request overrides/ },
        { timeout: 3000 }
      )
      fireEvent.click(advancedTrigger)

      const label = await screen.findByText(
        'Simulate cache creation',
        undefined,
        { timeout: 3000 }
      )
      const formItem = label.closest('[data-slot="form-item"]')
      const control = formItem?.querySelector('[role="switch"]')

      expect(control).toBeInTheDocument()
      expect(control).toHaveAttribute('aria-checked', 'false')

      fireEvent.click(control as HTMLElement)
      await waitFor(
        () => {
          expect(control).toHaveAttribute('aria-checked', 'true')
        },
        { timeout: 3000 }
      )

      fireEvent.click(control as HTMLElement)
      await waitFor(
        () => {
          expect(control).toHaveAttribute('aria-checked', 'false')
        },
        { timeout: 3000 }
      )
    },
    30_000
  )
})
