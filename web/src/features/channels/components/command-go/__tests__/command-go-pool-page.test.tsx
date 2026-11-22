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
import { toast } from 'sonner'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { CommandGoPoolPage } from '../command-go-pool-page'

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouter: () => ({
    history: { canGoBack: () => false, back: vi.fn() },
  }),
}))

type ApiMethod = (url: string, config?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; post: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post

beforeEach(() => {
  apiClient.get = originalGet
  apiClient.post = originalPost
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'admin',
    role: ROLE.SUPER_ADMIN,
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

function installApiFixtures(options: { withAccount?: boolean } = {}) {
  const accounts = options.withAccount
    ? [
        {
          id: 1,
          name: 'user_test',
          key_mask: 'user_te••••',
          status: 'error',
          last_error: 'upstream rejected',
          consecutive_failures: 1,
          last_used_at: 0,
          last_refreshed_at: 0,
          plan_id: '',
          windows: {
            five_hour: { used: 0, cap: 100, percent: 0, reset_at: 0 },
            weekly: { used: 0, cap: 100, percent: 0, reset_at: 0 },
            monthly: { used: 0, cap: 100, percent: 0, reset_at: 0 },
          },
        },
      ]
    : []
  apiClient.get = async (url: string) => {
    if (url === '/api/channel/63') {
      return {
        data: {
          success: true,
          data: { id: 63, name: 'Command Go pool', type: 63 },
        },
      }
    }
    if (url === '/api/channel/63/commandgo/accounts') {
      return {
        data: {
          success: true,
          data: {
            accounts,
            summary: {
              total: accounts.length,
              active: 0,
              error: Math.max(accounts.length, 1),
              no_quota: 0,
            },
          },
        },
      }
    }
    throw new Error(`Unexpected GET ${url}`)
  }
}

function renderPoolPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <CommandGoPoolPage channelId={63} />
    </QueryClientProvider>
  )
}

describe('CommandGoPoolPage dialogs', () => {
  test('opens the import dialog when Import Accounts is clicked', async () => {
    installApiFixtures()
    renderPoolPage()

    const importButton = await screen.findByRole('button', {
      name: 'Import Accounts',
    })
    fireEvent.click(importButton)

    expect(await screen.findByText('Import Command Go Accounts')).toBeVisible()
  })

  test('opens the delete confirmation dialog for status deletion', async () => {
    installApiFixtures()
    renderPoolPage()

    const deleteButton = await screen.findByRole('button', {
      name: 'Delete All Error Accounts',
    })
    await waitFor(() => expect(deleteButton).toBeEnabled())
    fireEvent.click(deleteButton)

    expect(await screen.findByText('Delete all error accounts?')).toBeVisible()
  })

  test('disables account mutations without channel OPERATE permission', async () => {
    installApiFixtures({ withAccount: true })
    useAuthStore.getState().auth.setUser({
      id: 2,
      username: 'read-only-admin',
      role: ROLE.ADMIN,
      permissions: {
        admin_permissions: {
          channel: { read: true, operate: false },
        },
      },
    })
    renderPoolPage()

    expect(
      await screen.findByRole('button', { name: 'Import Accounts' })
    ).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Update All Balances' })
    ).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Delete All Error Accounts' })
    ).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Delete All No-Quota Accounts' })
    ).toBeDisabled()

    fireEvent.click(await screen.findByLabelText('Select user_test'))
    expect(
      screen.getByRole('button', { name: 'Recover Selected' })
    ).toBeDisabled()
    expect(
      screen.getByRole('button', { name: 'Delete Selected' })
    ).toBeDisabled()
  })

  test('warns when a batch refresh partially fails', async () => {
    installApiFixtures({ withAccount: true })
    apiClient.post = vi.fn(async () => ({
      data: {
        success: true,
        data: {
          refreshed: 2,
          failed: [{ id: 9, name: 'user_bad', error: 'boom' }],
        },
      },
    }))
    const warningSpy = vi.spyOn(toast, 'warning')
    renderPoolPage()

    const refreshButton = await screen.findByRole('button', {
      name: 'Update All Balances',
    })
    await waitFor(() => expect(refreshButton).toBeEnabled())
    fireEvent.click(refreshButton)

    await waitFor(() => {
      expect(warningSpy).toHaveBeenCalledWith(
        '2 account(s) refreshed, 1 failed. First: user_bad: boom'
      )
    })
  })

  test('shows an error toast when every account fails to refresh', async () => {
    installApiFixtures({ withAccount: true })
    apiClient.post = vi.fn(async () => ({
      data: {
        success: true,
        data: {
          refreshed: 0,
          failed: [{ id: 9, name: 'user_bad', error: 'boom' }],
        },
      },
    }))
    const errorSpy = vi.spyOn(toast, 'error')
    renderPoolPage()

    const refreshButton = await screen.findByRole('button', {
      name: 'Update All Balances',
    })
    await waitFor(() => expect(refreshButton).toBeEnabled())
    fireEvent.click(refreshButton)

    await waitFor(() => {
      expect(errorSpy).toHaveBeenCalledWith('Refresh failed: user_bad: boom')
    })
  })
})
