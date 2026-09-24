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
  parseResponseRewriteGroups,
  parseResponseRewriteRules,
  serializeResponseRewriteGroups,
  serializeResponseRewriteRules,
  strictParseResponseRewriteRules,
  validateResponseRewriteRule,
  validateResponseRewriteRules,
  type ResponseRewriteRule,
} from '../types'

const validRule = {
  name: 'hide vendor',
  groups: ['default'],
  find: 'gpt-4o',
  replace: 'gpt-4',
}

function makeRule(
  patch: Partial<ResponseRewriteRule> = {}
): ResponseRewriteRule {
  return {
    id: 'rule-1',
    ...validRule,
    ...patch,
  }
}

describe('response rewrite rule validation', () => {
  test('accepts a rule with a non-empty find fragment', () => {
    expect(validateResponseRewriteRule({ find: 'gpt-4o' })).toBeNull()
  })

  test('rejects an empty or whitespace-only find fragment', () => {
    expect(validateResponseRewriteRule({ find: '' })).toBe(
      'Find fragment is required'
    )
    expect(validateResponseRewriteRule({ find: '   ' })).toBe(
      'Find fragment is required'
    )
  })
})

describe('response rewrite rule table validation', () => {
  test('accepts a valid unique rule list', () => {
    expect(validateResponseRewriteRules([makeRule()])).toBeNull()
  })

  test('accepts rules without groups (global scope)', () => {
    expect(validateResponseRewriteRules([makeRule({ groups: [] })])).toBeNull()
  })

  test('rejects an empty rule id', () => {
    expect(validateResponseRewriteRules([makeRule({ id: '  ' })])).toBe(
      'Rule ID is required'
    )
  })

  test('rejects duplicate rule ids', () => {
    expect(
      validateResponseRewriteRules([
        makeRule({ id: 'rule-1' }),
        makeRule({ id: 'rule-1', groups: ['vip'] }),
      ])
    ).toBe('Rule IDs must be unique')
  })

  test('rejects a missing find fragment', () => {
    expect(validateResponseRewriteRules([makeRule({ find: '  ' })])).toBe(
      'Find fragment is required'
    )
  })

  test('rejects duplicate find fragments inside the same group', () => {
    expect(
      validateResponseRewriteRules([
        makeRule({ id: 'rule-1', groups: ['vip', 'default'], find: 'gpt-4o' }),
        makeRule({ id: 'rule-2', groups: ['vip'], find: 'gpt-4o' }),
      ])
    ).toBe('Duplicate find fragment in the same group')
  })

  test('rejects a global rule that collides with a group-specific rule', () => {
    expect(
      validateResponseRewriteRules([
        makeRule({ id: 'rule-1', groups: [], find: 'gpt-4o' }),
        makeRule({ id: 'rule-2', groups: ['default'], find: 'gpt-4o' }),
      ])
    ).toBe('Duplicate find fragment in the same group')
    expect(
      validateResponseRewriteRules([
        makeRule({ id: 'rule-1', groups: ['default'], find: 'gpt-4o' }),
        makeRule({ id: 'rule-2', groups: [], find: 'gpt-4o' }),
      ])
    ).toBe('Duplicate find fragment in the same group')
  })

  test('rejects duplicate global find fragments', () => {
    expect(
      validateResponseRewriteRules([
        makeRule({ id: 'rule-1', groups: [], find: 'gpt-4o' }),
        makeRule({ id: 'rule-2', groups: [], find: 'gpt-4o' }),
      ])
    ).toBe('Duplicate find fragment in the same group')
  })

  test('allows the same find fragment in different groups', () => {
    expect(
      validateResponseRewriteRules([
        makeRule({ id: 'rule-1', groups: ['vip'], find: 'gpt-4o' }),
        makeRule({ id: 'rule-2', groups: ['default'], find: 'gpt-4o' }),
      ])
    ).toBeNull()
  })

  test('allows different find fragments inside the same group', () => {
    expect(
      validateResponseRewriteRules([
        makeRule({ id: 'rule-1', groups: ['vip'], find: 'gpt-4o' }),
        makeRule({ id: 'rule-2', groups: ['vip'], find: 'claude-3' }),
      ])
    ).toBeNull()
  })
})

describe('response rewrite rule JSON', () => {
  test('round-trips rules while preserving find and replace fragments exactly', () => {
    const parsed = parseResponseRewriteRules(
      serializeResponseRewriteRules([
        makeRule({ find: ' gpt-4o ', replace: '  gpt-4  ' }),
      ])
    )
    expect(parsed).toHaveLength(1)
    expect(parsed[0]?.find).toBe(' gpt-4o ')
    expect(parsed[0]?.replace).toBe('  gpt-4  ')
    expect(parsed[0]?.name).toBe('hide vendor')
  })

  test('returns an empty list for invalid JSON', () => {
    expect(parseResponseRewriteRules('{')).toEqual([])
    expect(parseResponseRewriteRules('{}')).toEqual([])
  })

  test('unique-trims groups on parse and serialize', () => {
    const parsed = parseResponseRewriteRules(
      JSON.stringify([
        makeRule({ groups: ['vip', ' vip', 'default', 'vip', ''] }),
      ])
    )
    expect(parsed[0]?.groups).toEqual(['vip', 'default'])

    const serialized = JSON.parse(
      serializeResponseRewriteRules([
        makeRule({ groups: ['a', 'a', ' b ', ''] }),
      ])
    ) as Array<{ groups: string[] }>
    expect(serialized[0]?.groups).toEqual(['a', 'b'])
  })

  test('strict parse rejects a non-array payload', () => {
    expect(() => strictParseResponseRewriteRules('{}')).toThrow(
      'Rules JSON must be an array'
    )
  })

  test('strict parse rejects invalid JSON instead of returning an empty list', () => {
    expect(() => strictParseResponseRewriteRules('{')).toThrow(
      'Invalid rules JSON format'
    )
  })

  test('strict parse rejects a non-object rule entry', () => {
    expect(() =>
      strictParseResponseRewriteRules(JSON.stringify(['nope']))
    ).toThrow('Invalid rules JSON format')
  })

  test('strict parse generates unique non-empty ids for empty rule ids', () => {
    const parsed = strictParseResponseRewriteRules(
      JSON.stringify([
        makeRule({ id: '  ' }),
        makeRule({ id: '', groups: ['vip'] }),
      ])
    )
    expect(parsed[0]?.id).toBeTruthy()
    expect(parsed[1]?.id).toBeTruthy()
    expect(parsed[0]?.id).not.toBe(parsed[1]?.id)
    expect(validateResponseRewriteRules(parsed)).toBeNull()
  })
})

describe('response rewrite model id groups', () => {
  test('parses an empty JSON string or non-array payload as no groups', () => {
    expect(parseResponseRewriteGroups('')).toEqual([])
    expect(parseResponseRewriteGroups('{}')).toEqual([])
    expect(parseResponseRewriteGroups('{')).toEqual([])
  })

  test('unique-trims groups when parsing and serializing', () => {
    expect(
      parseResponseRewriteGroups('["vip", " vip ", "", "default"]')
    ).toEqual(['vip', 'default'])
    expect(serializeResponseRewriteGroups(['vip', 'vip', ' default '])).toBe(
      '["vip","default"]'
    )
    expect(serializeResponseRewriteGroups([])).toBe('[]')
  })
})
