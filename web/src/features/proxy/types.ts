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
 * A single node parsed from a proxy subscription. `bindable` is true when the
 * node's protocol is supported for direct connection through the gateway.
 */
export type ProxyNode = {
  name: string
  type: string
  server: string
  port: number
  bindable: boolean
  proxy_url?: string
  share_url?: string
  relay?: boolean
}

/**
 * A routing rule that binds a set of channels to a single proxy node.
 * `id` is a backend-generated string timestamp id.
 */
export type ProxyRule = {
  id: string
  name: string
  channel_ids: number[]
  node: ProxyNode
  proxy_url?: string
  relay_port?: number
  created_at: number
}

/**
 * Contract of the backend `GET /api/proxy/status` endpoint.
 */
export type ProxyStatusData = {
  subscription_url: string
  subscription_fetched_at: number
  nodes: ProxyNode[]
  rules: ProxyRule[]
}

export type ProxyStatusResult = {
  success: boolean
  message?: string
  data?: ProxyStatusData
}

/**
 * Shape returned by `POST /api/proxy/subscription` and `POST /api/proxy/refresh`.
 */
export type ProxyFetchData = {
  node_count: number
  bindable_count: number
  nodes: ProxyNode[]
}

export type ProxyFetchResult = {
  success: boolean
  message?: string
  data?: ProxyFetchData
}

export type ProxyCreateRuleResult = {
  success: boolean
  message?: string
  data?: {
    rule: ProxyRule
  }
}

export type ProxySimpleResult = {
  success: boolean
  message?: string
}

/**
 * Payload for `POST /api/proxy/subscription`.
 */
export type SaveSubscriptionParams = {
  url: string
}

/**
 * Payload for `POST /api/proxy/rules`.
 */
export type CreateProxyRuleParams = {
  name: string
  channel_ids: number[]
  node: ProxyNode
}

/**
 * Payload for `PUT /api/proxy/rules/:id`.
 */
export type UpdateProxyRuleParams = CreateProxyRuleParams
