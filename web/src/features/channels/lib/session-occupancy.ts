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
export const CODE_PROVIDER_SESSION_USAGE_QUERY_KEY = [
  'code-provider-session-usage',
] as const

export interface SessionOccupancySlice {
  type: string
  value: number
}

export interface SessionOccupancySpec {
  type: 'pie'
  data: [{ id: string; values: SessionOccupancySlice[] }]
  valueField: 'value'
  categoryField: 'type'
  innerRadius: number
  outerRadius: number
  padAngle: number
  legends: { visible: false }
  label: { visible: false }
  tooltip: { visible: boolean }
  color: { specified: Record<string, string> }
}

export function clampSessionOccupancy(
  activeSessions: number,
  capacity: number
): { active: number; available: number; capacity: number; ratio: number } {
  const safeCapacity = Number.isFinite(capacity) ? Math.max(0, capacity) : 0
  const safeActive = Number.isFinite(activeSessions)
    ? Math.min(safeCapacity, Math.max(0, activeSessions))
    : 0
  const available = Math.max(0, safeCapacity - safeActive)
  const ratio = safeCapacity > 0 ? safeActive / safeCapacity : 0
  return {
    active: safeActive,
    available,
    capacity: safeCapacity,
    ratio,
  }
}

export function sessionOccupancyColor(ratio: number): string {
  if (ratio >= 1) return '#ef4444'
  if (ratio >= 0.5) return '#eab308'
  return '#22c55e'
}

export function buildSessionOccupancySpec(input: {
  activeSessions: number
  capacity: number
  activeLabel: string
  availableLabel: string
}): SessionOccupancySpec | null {
  const occupancy = clampSessionOccupancy(input.activeSessions, input.capacity)
  if (occupancy.capacity <= 0) return null

  return {
    type: 'pie',
    data: [
      {
        id: 'session-occupancy',
        values: [
          { type: input.activeLabel, value: occupancy.active },
          { type: input.availableLabel, value: occupancy.available },
        ],
      },
    ],
    valueField: 'value',
    categoryField: 'type',
    innerRadius: 0.72,
    outerRadius: 0.96,
    padAngle: 1.2,
    legends: { visible: false },
    label: { visible: false },
    tooltip: { visible: true },
    color: {
      specified: {
        [input.activeLabel]: sessionOccupancyColor(occupancy.ratio),
        [input.availableLabel]: '#94a3b8',
      },
    },
  }
}
