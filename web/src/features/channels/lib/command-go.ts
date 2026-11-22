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
import type { CommandGoAccountStatus } from '../types'
import { formatRelativeTime } from './channel-utils'

/** Mirrors the backend `commandGoImportLimit`. */
export const COMMAND_GO_IMPORT_LIMIT = 50

/** Matches every user_* fragment, mirroring the backend candidate regex. */
const COMMAND_GO_CANDIDATE_PATTERN = /user_[a-zA-Z0-9_-]*/g

/** Mirrors the backend `commandGoKeyPattern` (8+ chars after `user_`). */
const COMMAND_GO_KEY_PATTERN = /^user_[a-zA-Z0-9_-]{8,}$/

/** Backend timestamps arrive either in seconds or milliseconds. */
export function normalizeUnixTimestamp(value: number): number {
  if (!value || !Number.isFinite(value)) return 0
  return value > 1e12 ? value / 1000 : value
}

/** Query key for one Command Go account pool. */
export function commandGoAccountsQueryKey(channelId: number) {
  return ['command-go-accounts', channelId] as const
}

/**
 * Extract every valid user_* key in order, de-duplicating repeats and counting
 * invalid look-alike candidates, using the same rules as the backend importer.
 */
export function extractCommandGoKeys(text: string): {
  keys: string[]
  invalid: number
} {
  const seen = new Set<string>()
  const keys: string[] = []
  let invalid = 0
  for (const candidate of text.match(COMMAND_GO_CANDIDATE_PATTERN) ?? []) {
    if (!COMMAND_GO_KEY_PATTERN.test(candidate)) {
      invalid += 1
      continue
    }
    if (seen.has(candidate)) continue
    seen.add(candidate)
    keys.push(candidate)
  }
  return { keys, invalid }
}

export const COMMAND_GO_ACCOUNT_STATUS_CONFIG: Record<
  CommandGoAccountStatus,
  { labelKey: string; variant: 'success' | 'danger' | 'neutral' }
> = {
  active: { labelKey: 'Active', variant: 'success' },
  error: { labelKey: 'Error', variant: 'danger' },
  no_quota: { labelKey: 'No Quota', variant: 'neutral' },
}

const COMMAND_GO_UNKNOWN_STATUS = {
  labelKey: 'Unknown',
  variant: 'neutral',
} as const

export function getCommandGoStatusConfig(status: string) {
  return (
    COMMAND_GO_ACCOUNT_STATUS_CONFIG[status as CommandGoAccountStatus] ||
    COMMAND_GO_UNKNOWN_STATUS
  )
}

export function formatLastActivity(
  timestamp: number,
  locale?: Intl.LocalesArgument
): string {
  return formatRelativeTime(normalizeUnixTimestamp(timestamp), locale)
}

export type ResetCountdownUnitLabels = {
  second: string
  minute: string
  hour: string
  day: string
}

const DEFAULT_RESET_COUNTDOWN_UNITS: ResetCountdownUnitLabels = {
  second: 's',
  minute: 'm',
  hour: 'h',
  day: 'd',
}

export function formatResetCountdown(
  resetAt: number,
  nowMs: number = Date.now(),
  units: ResetCountdownUnitLabels = DEFAULT_RESET_COUNTDOWN_UNITS
): string {
  const resetSeconds = normalizeUnixTimestamp(resetAt)
  if (!resetSeconds) return ''
  const remaining = Math.floor(resetSeconds - nowMs / 1000)
  if (remaining <= 0) return ''
  if (remaining < 60) return `${remaining}${units.second}`
  if (remaining < 3_600) {
    return `${Math.floor(remaining / 60)}${units.minute}`
  }
  if (remaining < 86_400) {
    const hours = Math.floor(remaining / 3_600)
    const minutes = Math.floor((remaining % 3_600) / 60)
    return minutes > 0
      ? `${hours}${units.hour} ${minutes}${units.minute}`
      : `${hours}${units.hour}`
  }
  const days = Math.floor(remaining / 86_400)
  const hours = Math.floor((remaining % 86_400) / 3_600)
  return hours > 0
    ? `${days}${units.day} ${hours}${units.hour}`
    : `${days}${units.day}`
}
