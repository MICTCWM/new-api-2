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

import { createModelSchema } from '../model-ratio-schema'
import { normalizeJsonString, validateJsonString } from '../utils'

const t = (key: string) => key
const schema = createModelSchema(t)

const baseModelValues = {
  ModelPrice: '',
  ModelRatio: '',
  CacheRatio: '',
  CreateCacheRatio: '',
  CreateCacheRatio1h: '',
  CompletionRatio: '',
  ImageRatio: '',
  AudioRatio: '',
  AudioCompletionRatio: '',
  ExposeRatioEnabled: false,
  BillingMode: '{}',
  BillingExpr: '{}',
}

describe('CreateCacheRatio1h model pricing field', () => {
  test('registers the 1-hour cache creation ratio field in the model schema', () => {
    expect(Object.keys(schema.shape)).toContain('CreateCacheRatio1h')
  })

  test('allows an empty 1h ratio map to mean unconfigured (default fallback)', () => {
    for (const empty of ['', '   ']) {
      const result = schema.safeParse({
        ...baseModelValues,
        CreateCacheRatio1h: empty,
      })
      expect(result.success).toBe(true)
    }
  })

  test('accepts a per-model 1h cache creation ratio map', () => {
    const result = schema.safeParse({
      ...baseModelValues,
      CreateCacheRatio1h: '{"gpt-4o": 1.2}',
    })

    expect(result.success).toBe(true)
  })

  test('keeps an explicit zero ratio configured instead of collapsing it to empty', () => {
    const result = schema.safeParse({
      ...baseModelValues,
      CreateCacheRatio1h: '{"gpt-4o": 0}',
    })

    expect(result.success).toBe(true)

    const normalized = normalizeJsonString('{"gpt-4o": 0}')
    expect(normalized).toBe('{"gpt-4o":0}')
    expect(JSON.parse(normalized)).toEqual({ 'gpt-4o': 0 })
    // An explicit 0 must not be normalized into an empty (unconfigured) value.
    expect(normalized).not.toBe('')
    expect(normalized).not.toBe('0')
  })

  test('rejects invalid JSON for the 1h ratio map', () => {
    const result = schema.safeParse({
      ...baseModelValues,
      CreateCacheRatio1h: '{not-json',
    })

    expect(result.success).toBe(false)
  })

  test('normalization keeps empty maps empty and preserves configured ratios', () => {
    expect(normalizeJsonString('')).toBe('')
    expect(normalizeJsonString('   ')).toBe('')
    expect(normalizeJsonString('{"gpt-4o": 1.2}')).toBe('{"gpt-4o":1.2}')

    const emptyValidation = validateJsonString('')
    expect(emptyValidation.valid).toBe(true)
  })
})
