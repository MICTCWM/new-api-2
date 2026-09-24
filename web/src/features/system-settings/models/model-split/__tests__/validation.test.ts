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
  buildModelSplitUsageRows,
  parseModelSplitRules,
  parseModelSplitUsage,
  resolveSplitModelQuotaDisplay,
  serializeModelSplitRules,
  strictParseModelSplitRules,
  validateModelSplitRule,
  validateModelSplitRules,
  type ModelSplitRule,
} from '../types'

const validRule = {
  name: 'night split',
  groups: ['default'],
  original_model: 'gpt-4',
  model_1: 'gpt-4o',
  percent_1: 70,
  model_2: 'claude-3',
  percent_2: 30,
  image_split_enabled: false,
  image_model: '',
}

function makeRule(patch: Partial<ModelSplitRule> = {}): ModelSplitRule {
  return {
    id: 'rule-1',
    enabled: true,
    ...validRule,
    ...patch,
  }
}

describe('model split rule validation', () => {
  test('accepts a complete 70/30 split without image routing', () => {
    expect(validateModelSplitRule(validRule)).toBeNull()
  })

  test('rejects a missing name', () => {
    expect(validateModelSplitRule({ ...validRule, name: '  ' })).toBe(
      'Name is required'
    )
  })

  test('rejects a rule with no groups', () => {
    expect(validateModelSplitRule({ ...validRule, groups: [] })).toBe(
      'At least one group is required'
    )
  })

  test('rejects a missing original model', () => {
    expect(validateModelSplitRule({ ...validRule, original_model: '' })).toBe(
      'Original model is required'
    )
  })

  test('rejects missing split models', () => {
    expect(validateModelSplitRule({ ...validRule, model_2: '' })).toBe(
      'Both split models are required'
    )
  })

  test('rejects identical split models', () => {
    expect(validateModelSplitRule({ ...validRule, model_2: 'gpt-4o' })).toBe(
      'Split models must be different'
    )
  })

  test('rejects non-integer or out-of-range percents', () => {
    expect(
      validateModelSplitRule({ ...validRule, percent_1: 50.5, percent_2: 49.5 })
    ).toBe('Percents must be integers between 0 and 100')
    expect(
      validateModelSplitRule({ ...validRule, percent_1: -1, percent_2: 101 })
    ).toBe('Percents must be integers between 0 and 100')
  })

  test('rejects percents that do not add up to 100', () => {
    expect(
      validateModelSplitRule({ ...validRule, percent_1: 40, percent_2: 40 })
    ).toBe('Percents must add up to 100')
  })

  test('accepts 0/100 percents', () => {
    expect(
      validateModelSplitRule({ ...validRule, percent_1: 0, percent_2: 100 })
    ).toBeNull()
  })

  test('requires image_model to be one of the split models when image split is on', () => {
    expect(
      validateModelSplitRule({
        ...validRule,
        image_split_enabled: true,
        image_model: '',
      })
    ).toBe('Image model is required when image split is enabled')
    expect(
      validateModelSplitRule({
        ...validRule,
        image_split_enabled: true,
        image_model: 'other',
      })
    ).toBe('Image model must be one of the two split models')
    expect(
      validateModelSplitRule({
        ...validRule,
        image_split_enabled: true,
        image_model: 'gpt-4o',
      })
    ).toBeNull()
  })
})

describe('model split rule table validation', () => {
  test('accepts a valid unique rule list', () => {
    expect(validateModelSplitRules([makeRule()])).toBeNull()
  })

  test('rejects an empty rule id', () => {
    expect(validateModelSplitRules([makeRule({ id: '  ' })])).toBe(
      'Rule ID is required'
    )
  })

  test('rejects duplicate rule ids', () => {
    expect(
      validateModelSplitRules([
        makeRule({ id: 'rule-1' }),
        makeRule({
          id: 'rule-1',
          original_model: 'gpt-4.1',
        }),
      ])
    ).toBe('Rule IDs must be unique')
  })

  test('rejects enabled rules that share a group and original model', () => {
    expect(
      validateModelSplitRules([
        makeRule({
          id: 'rule-1',
          groups: ['vip', 'default'],
          original_model: 'gpt-4',
        }),
        makeRule({
          id: 'rule-2',
          groups: ['vip'],
          original_model: 'gpt-4',
        }),
      ])
    ).toBe('Enabled rules conflict for the same group and original model')
  })

  test('allows a disabled rule to overlap an enabled group and original model', () => {
    expect(
      validateModelSplitRules([
        makeRule({
          id: 'rule-1',
          groups: ['vip'],
          original_model: 'gpt-4',
        }),
        makeRule({
          id: 'rule-2',
          enabled: false,
          groups: ['vip'],
          original_model: 'gpt-4',
        }),
      ])
    ).toBeNull()
  })

  test('allows enabled rules that share a group with different original models', () => {
    expect(
      validateModelSplitRules([
        makeRule({
          id: 'rule-1',
          groups: ['vip'],
          original_model: 'gpt-4',
        }),
        makeRule({
          id: 'rule-2',
          groups: ['vip'],
          original_model: 'gpt-4.1',
        }),
      ])
    ).toBeNull()
  })
})

describe('model split rule JSON', () => {
  test('round-trips a rule and clears image_model when image split is off', () => {
    const parsed = parseModelSplitRules(
      serializeModelSplitRules([
        makeRule({
          image_split_enabled: false,
          image_model: 'gpt-4o',
        }),
      ])
    )
    expect(parsed).toHaveLength(1)
    expect(parsed[0]?.image_model).toBe('')
    expect(parsed[0]?.percent_1).toBe(70)
    expect(parsed[0]?.model_2).toBe('claude-3')
  })

  test('returns an empty list for invalid JSON', () => {
    expect(parseModelSplitRules('{')).toEqual([])
    expect(parseModelSplitRules('{}')).toEqual([])
  })

  test('unique-trims groups on parse and serialize', () => {
    const parsed = parseModelSplitRules(
      JSON.stringify([
        makeRule({
          groups: ['vip', ' vip', 'default', 'vip', ''],
        }),
      ])
    )
    expect(parsed[0]?.groups).toEqual(['vip', 'default'])

    const serialized = JSON.parse(
      serializeModelSplitRules([
        makeRule({
          groups: ['a', 'a', ' b ', ''],
        }),
      ])
    ) as Array<{ groups: string[] }>
    expect(serialized[0]?.groups).toEqual(['a', 'b'])

    const rows = buildModelSplitUsageRows(parsed, [])
    expect(rows.map((row) => row.key)).toEqual(['rule-1:vip', 'rule-1:default'])
  })

  test('strict parse rejects a non-array payload', () => {
    expect(() => strictParseModelSplitRules('{}')).toThrow(
      'Rules JSON must be an array'
    )
  })

  test('strict parse rejects invalid JSON instead of returning an empty list', () => {
    expect(() => strictParseModelSplitRules('{')).toThrow(
      'Invalid rules JSON format'
    )
  })

  test('strict parse rejects a non-integer percent instead of rewriting it to 50', () => {
    expect(() =>
      strictParseModelSplitRules(
        JSON.stringify([
          {
            ...makeRule(),
            percent_1: 50.5,
            percent_2: 49.5,
          },
        ])
      )
    ).toThrow('Percents must be integers between 0 and 100')
  })

  test('strict parse generates unique non-empty ids for empty rule ids', () => {
    const parsed = strictParseModelSplitRules(
      JSON.stringify([
        makeRule({ id: '  ' }),
        makeRule({
          id: '',
          original_model: 'gpt-4.1',
        }),
      ])
    )
    expect(parsed[0]?.id).toBeTruthy()
    expect(parsed[1]?.id).toBeTruthy()
    expect(parsed[0]?.id).not.toBe(parsed[1]?.id)
    expect(validateModelSplitRules(parsed)).toBeNull()
  })
})

describe('model split remaining usage', () => {
  test('aligns remaining counts by rule_id and group, including negative image remainings', () => {
    const rows = buildModelSplitUsageRows(
      [
        makeRule({
          id: 'rule-1',
          groups: ['default', 'vip'],
          image_split_enabled: true,
          image_model: 'gpt-4o',
        }),
      ],
      parseModelSplitUsage([
        {
          rule_id: 'rule-1',
          group: 'vip',
          remaining_total: 12,
          remaining_model_1: -3,
          remaining_model_2: 8,
        },
      ])
    )
    expect(rows).toHaveLength(2)
    expect(rows[0]).toMatchObject({
      group: 'default',
      remaining_total: null,
      remaining_model_1: null,
    })
    expect(rows[1]).toMatchObject({
      group: 'vip',
      remaining_total: 12,
      remaining_model_1: -3,
      remaining_model_2: 8,
    })
  })

  test('reads usage lists nested under items', () => {
    const usages = parseModelSplitUsage({
      items: [{ rule_id: 'rule-1', group: 'default', remaining_total: 100 }],
    })
    expect(usages).toEqual([
      {
        rule_id: 'rule-1',
        group: 'default',
        remaining_total: 100,
        remaining_model_1: null,
        remaining_model_2: null,
        window: 100,
      },
    ])
  })

  test('falls back to the default window when the usage payload omits or invalidates it', () => {
    const usages = parseModelSplitUsage([
      { rule_id: 'rule-1', group: 'default' },
      { rule_id: 'rule-1', group: 'vip', window: 0 },
      { rule_id: 'rule-1', group: 'pro', window: '250' },
    ])
    expect(usages.map((usage) => usage.window)).toEqual([100, 100, 250])
  })

  test('carries window size and per-model quotas into usage rows', () => {
    const rows = buildModelSplitUsageRows(
      [
        makeRule({
          id: 'rule-1',
          groups: ['vip'],
          percent_1: 70,
          percent_2: 30,
        }),
      ],
      parseModelSplitUsage([
        {
          rule_id: 'rule-1',
          group: 'vip',
          window: 200,
          remaining_total: 150,
        },
      ])
    )
    expect(rows[0]).toMatchObject({
      window: 200,
      percent_1: 70,
      percent_2: 30,
      remaining_total: 150,
    })
  })

  test('shows the saved percents when the local rule has unsaved percent edits', () => {
    const rows = buildModelSplitUsageRows(
      [
        makeRule({
          id: 'rule-1',
          groups: ['vip'],
          percent_1: 50,
          percent_2: 50,
        }),
      ],
      parseModelSplitUsage([
        {
          rule_id: 'rule-1',
          group: 'vip',
          remaining_model_1: 40,
          remaining_model_2: 20,
        },
      ]),
      [
        makeRule({
          id: 'rule-1',
          groups: ['vip'],
          percent_1: 70,
          percent_2: 30,
        }),
      ]
    )
    expect(rows[0]).toMatchObject({ percent_1: 70, percent_2: 30 })
  })

  test('falls back to the local percents for a rule that is not saved yet', () => {
    const rows = buildModelSplitUsageRows(
      [
        makeRule({
          id: 'rule-new',
          groups: ['vip'],
          percent_1: 20,
          percent_2: 80,
        }),
      ],
      [],
      [makeRule({ id: 'rule-1', groups: ['vip'] })]
    )
    expect(rows[0]).toMatchObject({ percent_1: 20, percent_2: 80 })
  })
})

describe('model split quota display', () => {
  test('keeps the denominator and a non-negative Used while the window fits the percent', () => {
    expect(resolveSplitModelQuotaDisplay(5, 20)).toEqual({
      showPercent: true,
      used: 15,
    })
    expect(resolveSplitModelQuotaDisplay(20, 20)).toEqual({
      showPercent: true,
      used: 0,
    })
    expect(resolveSplitModelQuotaDisplay(-3, 20)).toEqual({
      showPercent: true,
      used: 23,
    })
  })

  test('hides the denominator and Used when the window still holds more than the saved percent', () => {
    expect(resolveSplitModelQuotaDisplay(30, 20)).toEqual({
      showPercent: false,
      used: null,
    })
    expect(resolveSplitModelQuotaDisplay(1, 0)).toEqual({
      showPercent: false,
      used: null,
    })
  })

  test('never derives a Used value without a remaining count', () => {
    expect(resolveSplitModelQuotaDisplay(null, 20)).toEqual({
      showPercent: true,
      used: null,
    })
  })
})
