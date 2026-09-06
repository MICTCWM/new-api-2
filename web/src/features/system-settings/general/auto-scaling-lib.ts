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
/**
 * Parse a comma-separated channel id list (the backend storage format for the
 * auto-scaling target channels, e.g. "1,3,5", empty string means none) into an
 * array of numeric ids. Only plain positive decimal segments (`/^\d+$/`,
 * matching the backend `strconv.Atoi` parsing) are accepted; empty, invalid or
 * non-positive segments are skipped, and duplicates are dropped while keeping
 * first-occurrence order.
 */
export function parseChannelIDs(value: string | undefined | null): number[] {
  if (!value) return []
  const ids: number[] = []
  for (const segment of value.split(',')) {
    const trimmed = segment.trim()
    if (!/^\d+$/.test(trimmed)) continue
    const id = Number(trimmed)
    if (id > 0 && !ids.includes(id)) {
      ids.push(id)
    }
  }
  return ids
}

/**
 * Serialize an array of channel ids back into the comma-separated string the
 * backend stores. An empty selection serializes to an empty string.
 */
export function formatChannelIDs(ids: number[]): string {
  return ids.join(',')
}

/**
 * Contract of the backend `GET /api/autoscaling/test` endpoint.
 */
export type AutoscalingTestResult = {
  success: boolean
  message?: string
  data?: Record<string, unknown> | unknown[]
}

/**
 * A single AutoDL instance returned by the backend.
 */
export type AutoscalingInstance = {
  instance_uuid: string
  name: string
  status: string
}

/**
 * Contract of the backend `GET /api/autoscaling/instances` endpoint. The
 * endpoint returns `success: false` (with a message such as "未配置 AutoDL
 * Token") as a business error when no AutoDL token is configured, without an
 * HTTP error status.
 */
export type AutoscalingInstancesResult = {
  success: boolean
  message?: string
  data?: {
    count: number
    instances: AutoscalingInstance[]
  }
}

/**
 * Parse a comma-separated instance_uuid list (the backend storage format for
 * the auto-scaling target instances, e.g. "uuid-a,uuid-b", empty string means
 * none) into an array of uuid strings. Empty segments are skipped, surrounding
 * whitespace is trimmed and duplicates are dropped while keeping first
 * occurrence order. Uuid strings are not format-validated, matching the backend
 * `SetAutoScalingInstanceIDs` behavior (which also only trims and deduplicates).
 */
export function parseInstanceIDs(value: string | undefined | null): string[] {
  if (!value) return []
  const ids: string[] = []
  for (const segment of value.split(',')) {
    const trimmed = segment.trim()
    if (!trimmed) continue
    if (!ids.includes(trimmed)) {
      ids.push(trimmed)
    }
  }
  return ids
}

/**
 * Serialize an array of instance uuids back into the comma-separated string the
 * backend stores. An empty selection serializes to an empty string.
 */
export function formatInstanceIDs(ids: string[]): string {
  return ids.join(',')
}

/**
 * Extract the number of AutoDL deployments reported by a successful test so the
 * UI can show "N deployments" instead of a bare OK.
 *
 * Returns `null` when the result carries no deployment information (the caller
 * falls back to a generic success message).
 */
export function extractDeploymentCount(
  result: AutoscalingTestResult | undefined
): number | null {
  const data = result?.data
  if (!data || typeof data !== 'object' || Array.isArray(data)) return null

  const count = data.count
  if (typeof count === 'number' && Number.isFinite(count)) {
    return count
  }

  return null
}