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
import { afterEach, describe, expect, test, vi } from 'vitest'

const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { ChannelsProvider } = await import('../../channels-provider')
const { ChannelMutateDrawer } = await import('../channel-mutate-drawer')

type ApiMethod = (url: string, config?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; post: ApiMethod; put: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post

afterEach(() => {
  apiClient.get = originalGet
  apiClient.post = originalPost
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
    if (url === '/api/channel/63') {
      return {
        data: {
          success: true,
          data: {
            id: 63,
            name: 'Command Go pool channel',
            type: 63,
            status: 1,
            created_time: 0,
            test_time: 0,
            response_time: 0,
            base_url: '',
            key: '',
            models: 'deepseek/deepseek-v4.1-flash',
            group: 'default',
            model_mapping: '',
            setting: '',
            settings: '{}',
            channel_info: {
              is_multi_key: false,
              multi_key_size: 0,
              multi_key_polling_index: 0,
              multi_key_mode: 'random',
            },
          },
        },
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
  test('renders the switch off by default inside Advanced Settings and toggles on click', async () => {
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
  }, 30_000)
})

describe('ChannelMutateDrawer Command Go channel type', () => {
  test('does not render a Base URL input when editing a type 63 channel', async () => {
    installApiFixtures()
    const { container } = render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <ChannelsProvider>
          <ChannelMutateDrawer
            open
            onOpenChange={() => undefined}
            currentRow={
              { id: 63, type: 63, name: 'Command Go pool channel' } as never
            }
          />
        </ChannelsProvider>
      </QueryClientProvider>
    )

    await waitFor(
      () => {
        expect(
          screen.getByDisplayValue('Command Go pool channel')
        ).toBeInTheDocument()
      },
      { timeout: 5000 }
    )

    expect(screen.queryByText('Base URL')).not.toBeInTheDocument()
    // Covers every Base URL variant (general input plus provider-specific
    // labels) by checking the bound form control itself.
    expect(container.querySelector('[name="base_url"]')).toBeNull()
  })
})

describe('ChannelMutateDrawer create submit validation', () => {
  test('submits a type 63 channel with an empty API key', async () => {
    installApiFixtures()
    const postSpy = vi.fn(async (_url: string, _payload?: unknown) => ({
      data: { success: true },
    }))
    apiClient.post = postSpy
    renderCreateDrawer()

    const typeInput = await screen.findByPlaceholderText(
      'Search channel type...'
    )
    fireEvent.change(typeInput, { target: { value: '63' } })
    fireEvent.change(screen.getByLabelText('Name *'), {
      target: { value: 'Command Go pool channel' },
    })

    expect(await screen.findByLabelText('API Key')).toBeInTheDocument()
    expect(screen.queryByLabelText('API Key *')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))

    await waitFor(
      () => {
        expect(postSpy).toHaveBeenCalledTimes(1)
      },
      { timeout: 5000 }
    )
    expect(postSpy.mock.calls[0][0]).toBe('/api/channel')
    expect(
      (postSpy.mock.calls[0][1] as { channel: { type: number } }).channel.type
    ).toBe(63)
  }, 30_000)

  test('blocks creating a type 62 channel with an empty API key', async () => {
    installApiFixtures()
    const postSpy = vi.fn(async () => ({ data: { success: true } }))
    apiClient.post = postSpy
    renderCreateDrawer()

    const typeInput = await screen.findByPlaceholderText(
      'Search channel type...'
    )
    fireEvent.change(typeInput, { target: { value: '62' } })
    fireEvent.change(screen.getByLabelText('Name *'), {
      target: { value: 'Command channel' },
    })

    expect(await screen.findByLabelText('API Key *')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))

    expect(await screen.findByText('API key is required')).toBeInTheDocument()
    expect(postSpy).not.toHaveBeenCalled()
  }, 30_000)
})
