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
import { api } from '@/lib/api'

import type {
  CreateProxyRuleParams,
  ProxyCreateRuleResult,
  ProxyFetchResult,
  ProxySimpleResult,
  ProxyStatusResult,
  SaveSubscriptionParams,
  UpdateProxyRuleParams,
} from './types'

const businessErrorConfig = { skipBusinessError: true }

/**
 * Fetch the current proxy status: the configured subscription URL, when it was
 * last fetched, the parsed nodes and the routing rules. The endpoint returns
 * `success: false` when no subscription has been configured yet, so the shared
 * success interceptor must not toast that in the page — the caller decides.
 */
export async function getProxyStatus(): Promise<ProxyStatusResult> {
  const res = await api.get('/api/proxy/status', businessErrorConfig)
  return res.data
}

/**
 * Save a proxy subscription URL and immediately pull its nodes.
 */
export async function saveProxySubscription(
  params: SaveSubscriptionParams
): Promise<ProxyFetchResult> {
  const res = await api.post('/api/proxy/subscription', params, businessErrorConfig)
  return res.data
}

/**
 * Re-fetch nodes from the already-configured subscription URL.
 */
export async function refreshProxySubscription(): Promise<ProxyFetchResult> {
  const res = await api.post('/api/proxy/refresh', {}, businessErrorConfig)
  return res.data
}

/**
 * Clear the configured proxy subscription, its nodes and routing rules.
 */
export async function clearProxySubscription(): Promise<ProxySimpleResult> {
  const res = await api.delete('/api/proxy/subscription', businessErrorConfig)
  return res.data
}

/**
 * Create a new routing rule binding a set of channels to a proxy node.
 */
export async function createProxyRule(
  params: CreateProxyRuleParams
): Promise<ProxyCreateRuleResult> {
  const res = await api.post('/api/proxy/rules', params, businessErrorConfig)
  return res.data
}

/**
 * Update an existing routing rule: rename it, change its bound channels or
 * switch the selected proxy node.
 */
export async function updateProxyRule(
  id: string,
  params: UpdateProxyRuleParams
): Promise<ProxyCreateRuleResult> {
  const res = await api.put(`/api/proxy/rules/${id}`, params, businessErrorConfig)
  return res.data
}

/**
 * Delete a routing rule by id.
 */
export async function deleteProxyRule(id: string): Promise<ProxySimpleResult> {
  const res = await api.delete(`/api/proxy/rules/${id}`, businessErrorConfig)
  return res.data
}