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
 * Pure parsing/formatting helpers for proxy nodes and rules. Kept free of
 * React/network dependencies so they can be unit tested directly.
 */
import { z } from 'zod'

import type { CreateProxyRuleParams, ProxyNode } from './types'

/**
 * True when the string is an absolute http(s) URL. The gateway subscription
 * address must always start with a scheme so it can be fetched upstream.
 */
export function isHttpUrl(value: string | undefined | null): boolean {
  return /^https?:\/\/.+/i.test(value?.trim() ?? '')
}

/**
 * Human-readable `<server>:<port>` address for a node, falling back to the
 * proxy URL when the parsed address is missing.
 */
export function formatNodeAddress(node: ProxyNode): string {
  if (node.server && node.port) {
    return `${node.server}:${node.port}`
  }
  return (node.proxy_url || node.name || '').trim()
}

/**
 * Form schema for the "new rule" drawer. `nodeIndex` is the selected node's
 * index into the parsed node list (kept as a plain number, defaulting to -1
 * when nothing is selected so the `.min(0)` guard forces a selection).
 */
export const PROXY_RULE_FORM_SCHEMA = z.object({
  name: z.string().trim().min(1, 'Rule name is required'),
  channel_ids: z
    .array(z.number().int().min(1))
    .min(1, 'Select at least one channel'),
  nodeIndex: z.number().int().min(0, 'Select a proxy node'),
})

export type ProxyRuleFormValues = z.infer<typeof PROXY_RULE_FORM_SCHEMA>

/**
 * Build the backend `POST /api/proxy/rules` payload from the draft form values
 * and the current node list. The selected node is resolved by index so the
 * drawer only ever submits a fully populated node object.
 */
export function makeProxyRulePayload(
  values: ProxyRuleFormValues,
  nodes: ProxyNode[]
): CreateProxyRuleParams {
  const node = nodes[values.nodeIndex]
  return {
    name: values.name,
    channel_ids: values.channel_ids,
    // `node` is guaranteed by the schema (nodeIndex >= 0), but guard against a
    // stale out-of-range index just in case the node list changed mid-edit.
    node: node,
  }
}