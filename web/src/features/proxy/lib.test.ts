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
  formatNodeAddress,
  isHttpUrl,
  makeProxyRulePayload,
} from './lib'
import type { ProxyNode } from './types'

const makeNode = (overrides: Partial<ProxyNode> = {}): ProxyNode => ({
  name: 'node-1',
  type: 'vmess',
  server: '1.2.3.4',
  port: 443,
  bindable: true,
  ...overrides,
})

describe('isHttpUrl', () => {
  test('accepts http and https URLs', () => {
    expect(isHttpUrl('https://example.com/clash/sub')).toBe(true)
    expect(isHttpUrl('http://example.com/sub')).toBe(true)
  })

  test('rejects missing, empty, scheme-less and invalid values', () => {
    expect(isHttpUrl(undefined)).toBe(false)
    expect(isHttpUrl(null)).toBe(false)
    expect(isHttpUrl('')).toBe(false)
    expect(isHttpUrl('example.com/sub')).toBe(false)
    expect(isHttpUrl('ftp://example.com')).toBe(false)
    expect(isHttpUrl('https://')).toBe(false)
  })

  test('trims surrounding whitespace before matching', () => {
    expect(isHttpUrl('  https://example.com/sub  ')).toBe(true)
  })
})

describe('formatNodeAddress', () => {
  test('formats server:port', () => {
    expect(formatNodeAddress(makeNode())).toBe('1.2.3.4:443')
  })

  test('falls back to proxy_url when server/port are missing', () => {
    expect(
      formatNodeAddress(
        makeNode({ server: '', port: 0, proxy_url: 'scheme://fallback' })
      )
    ).toBe('scheme://fallback')
  })

  test('falls back to the name when no address is available', () => {
    expect(
      formatNodeAddress(makeNode({ server: '', port: 0, proxy_url: '' }))
    ).toBe('node-1')
  })
})

describe('makeProxyRulePayload', () => {
  test('resolves the selected node by index and copies form fields', () => {
    const nodes = [makeNode({ name: 'a' }), makeNode({ name: 'b' })]
    const payload = makeProxyRulePayload(
      { name: 'rule', channel_ids: [1, 2], nodeIndex: 1 },
      nodes
    )
    expect(payload).toEqual({
      name: 'rule',
      channel_ids: [1, 2],
      node: nodes[1],
    })
  })

  test('passes the out-of-range node through as-is (schema guards the UI)', () => {
    const payload = makeProxyRulePayload(
      { name: 'rule', channel_ids: [1], nodeIndex: 9 },
      [makeNode()]
    )
    expect(payload.node).toBeUndefined()
  })
})