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
import { describe, expect, test, vi } from 'vitest'

import { SettingsPageProvider } from '@/features/system-settings/components/settings-page-context'

import { ResponseRewriteSection } from '../index'
import {
  serializeResponseRewriteRules,
  type ResponseRewriteRule,
  type ResponseRewriteSettings,
} from '../types'

const { updateSystemOptionMock, toastErrorMock, getGroupsMock } = vi.hoisted(
  () => ({
    updateSystemOptionMock: vi.fn(async () => ({ success: true })),
    toastErrorMock: vi.fn(),
    getGroupsMock: vi.fn(async () => ({
      success: true,
      message: '',
      data: ['default', 'vip'],
    })),
  })
)

vi.mock('sonner', () => ({
  toast: {
    error: toastErrorMock,
    info: vi.fn(),
    success: vi.fn(),
  },
}))

vi.mock('@/features/users/api', () => ({
  getGroups: getGroupsMock,
}))

vi.mock('@/features/system-settings/api', () => ({
  updateSystemOption: updateSystemOptionMock,
}))

function makeRule(
  patch: Partial<ResponseRewriteRule> = {}
): ResponseRewriteRule {
  return {
    id: 'rule-1',
    name: '',
    groups: ['default'],
    find: 'gpt-4o',
    replace: 'gpt-4',
    ...patch,
  }
}

function makeSettings(
  overrides: Partial<ResponseRewriteSettings> = {}
): ResponseRewriteSettings {
  return {
    'response_rewrite_setting.model_id_enabled': false,
    'response_rewrite_setting.model_id_groups': '[]',
    'response_rewrite_setting.text_enabled': false,
    'response_rewrite_setting.text_rules': '[]',
    ...overrides,
  }
}

function renderSection(settings: ResponseRewriteSettings) {
  const actionsContainer = document.createElement('div')
  document.body.appendChild(actionsContainer)

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })

  render(
    <QueryClientProvider client={queryClient}>
      <SettingsPageProvider actionsContainer={actionsContainer}>
        <ResponseRewriteSection defaultValues={settings} />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

describe('response rewrite section save validation', () => {
  test('blocks saving invalid rules even while text rewrite is disabled', () => {
    renderSection(
      makeSettings({
        'response_rewrite_setting.text_rules': serializeResponseRewriteRules([
          makeRule({ id: 'rule-1', find: 'gpt-4o' }),
          makeRule({ id: 'rule-2', find: 'gpt-4o' }),
        ]),
      })
    )

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(toastErrorMock).toHaveBeenCalledWith(
      'Duplicate find fragment in the same group'
    )
    expect(updateSystemOptionMock).not.toHaveBeenCalled()
  })

  test('saves valid rules while text rewrite is disabled', async () => {
    renderSection(
      makeSettings({
        'response_rewrite_setting.text_rules': serializeResponseRewriteRules([
          makeRule(),
        ]),
      })
    )

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      expect(updateSystemOptionMock).toHaveBeenCalledWith({
        key: 'response_rewrite_setting.text_rules',
        value: '[]',
      })
    })
    expect(toastErrorMock).not.toHaveBeenCalled()
  })
})
