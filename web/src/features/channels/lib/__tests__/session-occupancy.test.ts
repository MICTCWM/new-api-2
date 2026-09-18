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
  buildSessionOccupancySpec,
  clampSessionOccupancy,
  sessionOccupancyColor,
} from '../session-occupancy'

describe('session occupancy spec', () => {
  test('builds active and available slices for a partial occupancy', () => {
    const spec = buildSessionOccupancySpec({
      activeSessions: 2,
      capacity: 3,
      activeLabel: 'Active sessions',
      availableLabel: 'Available',
    })

    expect(spec).not.toBeNull()
    expect(spec?.data[0].values).toEqual([
      { type: 'Active sessions', value: 2 },
      { type: 'Available', value: 1 },
    ])
    expect(spec?.color.specified['Active sessions']).toBe(
      sessionOccupancyColor(2 / 3)
    )
  })

  test('fills the ring when active sessions equal capacity', () => {
    const spec = buildSessionOccupancySpec({
      activeSessions: 3,
      capacity: 3,
      activeLabel: 'Active sessions',
      availableLabel: 'Available',
    })

    expect(spec?.data[0].values).toEqual([
      { type: 'Active sessions', value: 3 },
      { type: 'Available', value: 0 },
    ])
    expect(spec?.color.specified['Active sessions']).toBe('#ef4444')
  })

  test('returns null when capacity is missing', () => {
    expect(
      buildSessionOccupancySpec({
        activeSessions: 1,
        capacity: 0,
        activeLabel: 'Active sessions',
        availableLabel: 'Available',
      })
    ).toBeNull()
  })

  test('clamps oversized and negative occupancy before it reaches the chart', () => {
    expect(clampSessionOccupancy(99, 3)).toEqual({
      active: 3,
      available: 0,
      capacity: 3,
      ratio: 1,
    })
    expect(clampSessionOccupancy(-4, 3)).toEqual({
      active: 0,
      available: 3,
      capacity: 3,
      ratio: 0,
    })
  })
})
