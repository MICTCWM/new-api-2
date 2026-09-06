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
import { describe, expect, test } from 'vitest'

import {
  extractDeploymentCount,
  formatChannelIDs,
  formatInstanceIDs,
  parseChannelIDs,
  parseInstanceIDs,
} from '../auto-scaling-lib'

describe('parseChannelIDs', () => {
  test('returns an empty array for an empty or missing value', () => {
    expect(parseChannelIDs('')).toEqual([])
    expect(parseChannelIDs(undefined)).toEqual([])
    expect(parseChannelIDs(null)).toEqual([])
  })

  test('parses a single id', () => {
    expect(parseChannelIDs('7')).toEqual([7])
  })

  test('parses multiple ids', () => {
    expect(parseChannelIDs('1,3,5')).toEqual([1, 3, 5])
  })

  test('skips empty segments and surrounding whitespace', () => {
    expect(parseChannelIDs(' 1 , , 3 , 5 ')).toEqual([1, 3, 5])
  })

  test('skips non-numeric and invalid segments', () => {
    expect(parseChannelIDs('1,abc,2.5,3,-4,0')).toEqual([1, 3])
  })

  test('deduplicates repeated ids preserving first-occurrence order', () => {
    expect(parseChannelIDs('1,1')).toEqual([1])
    expect(parseChannelIDs('3,1,3,2,1')).toEqual([3, 1, 2])
  })

  test('rejects scientific notation and hex literals', () => {
    expect(parseChannelIDs('1e2')).toEqual([])
    expect(parseChannelIDs('0x10')).toEqual([])
  })

  test('rejects pure negative ids', () => {
    expect(parseChannelIDs('-1')).toEqual([])
    expect(parseChannelIDs('-1,3')).toEqual([3])
  })

  test('skips invalid segments while accepting whitespace-padded ids', () => {
    expect(parseChannelIDs('1,abc,3')).toEqual([1, 3])
    expect(parseChannelIDs(' 1 , 3 ')).toEqual([1, 3])
  })
})

describe('formatChannelIDs', () => {
  test('joins ids into a comma-separated string', () => {
    expect(formatChannelIDs([1, 3, 5])).toBe('1,3,5')
  })

  test('formats a single id', () => {
    expect(formatChannelIDs([7])).toBe('7')
  })

  test('returns an empty string for an empty selection', () => {
    expect(formatChannelIDs([])).toBe('')
  })
})

describe('extractDeploymentCount', () => {
  test('returns the numeric count field from data', () => {
    expect(
      extractDeploymentCount({
        success: true,
        data: { count: 3, deployments: [{}, {}, {}] },
      })
    ).toBe(3)
  })

  test('returns null when data is missing or carries no count', () => {
    expect(extractDeploymentCount(undefined)).toBeNull()
    expect(extractDeploymentCount({ success: true })).toBeNull()
    expect(
      extractDeploymentCount({ success: true, data: { ok: true } })
    ).toBeNull()
    expect(
      extractDeploymentCount({ success: true, data: { count: '5' } })
    ).toBeNull()
  })
})

describe('parseInstanceIDs', () => {
  test('returns an empty array for an empty or missing value', () => {
    expect(parseInstanceIDs('')).toEqual([])
    expect(parseInstanceIDs(undefined)).toEqual([])
    expect(parseInstanceIDs(null)).toEqual([])
  })

  test('parses a single uuid', () => {
    expect(parseInstanceIDs('abc-123')).toEqual(['abc-123'])
  })

  test('parses multiple uuids', () => {
    expect(parseInstanceIDs('uuid-a,uuid-b,uuid-c')).toEqual([
      'uuid-a',
      'uuid-b',
      'uuid-c',
    ])
  })

  test('skips empty segments and trims surrounding whitespace', () => {
    expect(parseInstanceIDs(' uuid-a , , uuid-b , ')).toEqual([
      'uuid-a',
      'uuid-b',
    ])
  })

  test('deduplicates repeated uuids preserving first-occurrence order', () => {
    expect(parseInstanceIDs('uuid-a,uuid-a')).toEqual(['uuid-a'])
    expect(parseInstanceIDs('uuid-b,uuid-a,uuid-b')).toEqual([
      'uuid-b',
      'uuid-a',
    ])
  })

  test('keeps arbitrary non-empty tokens without uuid-format validation', () => {
    expect(parseInstanceIDs('not-a-uuid, another')).toEqual([
      'not-a-uuid',
      'another',
    ])
  })
})

describe('formatInstanceIDs', () => {
  test('joins uuids into a comma-separated string', () => {
    expect(formatInstanceIDs(['uuid-a', 'uuid-b'])).toBe('uuid-a,uuid-b')
  })

  test('formats a single uuid', () => {
    expect(formatInstanceIDs(['uuid-a'])).toBe('uuid-a')
  })

  test('returns an empty string for an empty selection', () => {
    expect(formatInstanceIDs([])).toBe('')
  })
})
