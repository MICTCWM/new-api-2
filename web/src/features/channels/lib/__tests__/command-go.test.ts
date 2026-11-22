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
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  commandGoAccountsQueryKey,
  extractCommandGoKeys,
  formatResetCountdown,
  getCommandGoStatusConfig,
  normalizeUnixTimestamp,
} from '../command-go'

describe('command-go helpers', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('normalizes millisecond and second timestamps to seconds', () => {
    expect(normalizeUnixTimestamp(1_700_000_000)).toBe(1_700_000_000)
    expect(normalizeUnixTimestamp(1_700_000_000_000)).toBe(1_700_000_000)
    expect(normalizeUnixTimestamp(0)).toBe(0)
  })

  it('maps every account status to a display variant', () => {
    expect(getCommandGoStatusConfig('active')).toEqual({
      labelKey: 'Active',
      variant: 'success',
    })
    expect(getCommandGoStatusConfig('error').variant).toBe('danger')
    expect(getCommandGoStatusConfig('no_quota')).toEqual({
      labelKey: 'No Quota',
      variant: 'neutral',
    })
    expect(getCommandGoStatusConfig('mystery')).toEqual({
      labelKey: 'Unknown',
      variant: 'neutral',
    })
  })

  it('extracts valid user_* keys with backend-compatible dedupe and invalid counts', () => {
    expect(
      extractCommandGoKeys(
        'user_aaaaaaaa user_bbbbbbbb user_aaaaaaaa user_short user_ okay'
      )
    ).toEqual({
      keys: ['user_aaaaaaaa', 'user_bbbbbbbb'],
      invalid: 2,
    })
    expect(
      extractCommandGoKeys('user_aaaaaaaa,user_bbbbbbbb,user_cccccccc')
    ).toEqual({
      keys: ['user_aaaaaaaa', 'user_bbbbbbbb', 'user_cccccccc'],
      invalid: 0,
    })
  })

  it('formats the reset countdown for a future reset in seconds or milliseconds', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
    const nowSeconds = Date.now() / 1000

    expect(formatResetCountdown(nowSeconds + 2 * 3600)).toBe('2h')
    expect(formatResetCountdown((nowSeconds + 2 * 3600) * 1000)).toBe('2h')
    expect(formatResetCountdown(nowSeconds + 2 * 3600 + 30 * 60)).toBe('2h 30m')
    expect(formatResetCountdown(nowSeconds + 86_400)).toBe('1d')
    expect(formatResetCountdown(nowSeconds + 86_400 + 2 * 3600)).toBe('1d 2h')
    expect(formatResetCountdown(nowSeconds - 60)).toBe('')
  })

  it('builds a stable per-channel query key', () => {
    expect(commandGoAccountsQueryKey(63)).toEqual(['command-go-accounts', 63])
  })
})
