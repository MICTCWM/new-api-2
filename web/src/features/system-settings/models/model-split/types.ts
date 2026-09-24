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
export type ModelSplitRule = {
  id: string
  name: string
  enabled: boolean
  groups: string[]
  original_model: string
  model_1: string
  percent_1: number
  model_2: string
  percent_2: number
  image_split_enabled: boolean
  image_model: string
}

export type ModelSplitSettings = {
  'model_split_setting.enabled': boolean
  'model_split_setting.rules': string
  'group_mapping_setting.rules': string
}

export type ModelSplitUsageItem = {
  rule_id: string
  group: string
  remaining_total: number | null
  remaining_model_1: number | null
  remaining_model_2: number | null
  window: number
}

export type ModelSplitUsageRow = {
  key: string
  rule_id: string
  rule_name: string
  group: string
  remaining_total: number | null
  window: number
  model_1: string
  model_2: string
  percent_1: number
  percent_2: number
  remaining_model_1: number | null
  remaining_model_2: number | null
}

const MODEL_SPLIT_DEFAULT_WINDOW = 100

export function createModelSplitRuleId(): string {
  return `rule-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

export function parseModelSplitRules(jsonStr: string): ModelSplitRule[] {
  try {
    const parsed = JSON.parse(jsonStr || '[]')
    if (!Array.isArray(parsed)) return []
    return parsed.map((rule: Record<string, unknown>, index: number) => ({
      id: String(rule.id ?? '').trim() || createModelSplitRuleId() + index,
      name: String(rule.name ?? '').trim(),
      enabled: rule.enabled !== false,
      groups: Array.isArray(rule.groups)
        ? uniqueTrimmedStrings(rule.groups)
        : [],
      original_model: String(rule.original_model ?? '').trim(),
      model_1: String(rule.model_1 ?? '').trim(),
      percent_1: toNumber(rule.percent_1, 50),
      model_2: String(rule.model_2 ?? '').trim(),
      percent_2: toNumber(rule.percent_2, 50),
      image_split_enabled: Boolean(rule.image_split_enabled),
      image_model: String(rule.image_model ?? '').trim(),
    }))
  } catch {
    return []
  }
}

export function strictParseModelSplitRules(jsonStr: string): ModelSplitRule[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(jsonStr)
  } catch {
    throw new Error('Invalid rules JSON format')
  }
  if (!Array.isArray(parsed)) {
    throw new Error('Rules JSON must be an array')
  }
  return parsed.map((item) => {
    if (!isRecord(item)) {
      throw new Error('Invalid rules JSON format')
    }
    return {
      id: String(item.id ?? '').trim() || createModelSplitRuleId(),
      name: String(item.name ?? '').trim(),
      enabled: item.enabled !== false,
      groups: Array.isArray(item.groups)
        ? uniqueTrimmedStrings(item.groups)
        : [],
      original_model: String(item.original_model ?? '').trim(),
      model_1: String(item.model_1 ?? '').trim(),
      percent_1: toRequiredInteger(item.percent_1),
      model_2: String(item.model_2 ?? '').trim(),
      percent_2: toRequiredInteger(item.percent_2),
      image_split_enabled: Boolean(item.image_split_enabled),
      image_model: String(item.image_model ?? '').trim(),
    }
  })
}

export function serializeModelSplitRules(rules: ModelSplitRule[]): string {
  return JSON.stringify(
    rules.map((rule) => ({
      id: rule.id,
      name: rule.name,
      enabled: rule.enabled,
      groups: uniqueTrimmedStrings(rule.groups),
      original_model: rule.original_model.trim(),
      model_1: rule.model_1.trim(),
      percent_1: rule.percent_1,
      model_2: rule.model_2.trim(),
      percent_2: rule.percent_2,
      image_split_enabled: rule.image_split_enabled,
      image_model: rule.image_split_enabled ? rule.image_model.trim() : '',
    }))
  )
}

export function validateModelSplitRule(rule: {
  name: string
  groups: string[]
  original_model: string
  model_1: string
  percent_1: number
  model_2: string
  percent_2: number
  image_split_enabled: boolean
  image_model: string
}): string | null {
  if (!rule.name.trim()) return 'Name is required'
  if (rule.groups.length === 0) return 'At least one group is required'
  if (!rule.original_model.trim()) return 'Original model is required'
  const model1 = rule.model_1.trim()
  const model2 = rule.model_2.trim()
  if (!model1 || !model2) return 'Both split models are required'
  if (model1 === model2) return 'Split models must be different'
  if (
    !Number.isInteger(rule.percent_1) ||
    !Number.isInteger(rule.percent_2) ||
    rule.percent_1 < 0 ||
    rule.percent_1 > 100 ||
    rule.percent_2 < 0 ||
    rule.percent_2 > 100
  ) {
    return 'Percents must be integers between 0 and 100'
  }
  if (rule.percent_1 + rule.percent_2 !== 100) {
    return 'Percents must add up to 100'
  }
  if (!rule.image_split_enabled) return null
  const imageModel = rule.image_model.trim()
  if (!imageModel) {
    return 'Image model is required when image split is enabled'
  }
  if (imageModel !== model1 && imageModel !== model2) {
    return 'Image model must be one of the two split models'
  }
  return null
}

export function validateModelSplitRules(
  rules: ModelSplitRule[]
): string | null {
  const ids = new Set<string>()
  const originalByGroup = new Map<string, Set<string>>()

  for (const rule of rules) {
    const id = rule.id.trim()
    if (!id) return 'Rule ID is required'
    if (ids.has(id)) return 'Rule IDs must be unique'
    ids.add(id)

    const errorKey = validateModelSplitRule(rule)
    if (errorKey) return errorKey

    if (!rule.enabled) continue
    const original = rule.original_model.trim()
    for (const group of uniqueTrimmedStrings(rule.groups)) {
      let originals = originalByGroup.get(group)
      if (!originals) {
        originals = new Set<string>()
        originalByGroup.set(group, originals)
      }
      if (originals.has(original)) {
        return 'Enabled rules conflict for the same group and original model'
      }
      originals.add(original)
    }
  }
  return null
}

export function parseModelSplitUsage(data: unknown): ModelSplitUsageItem[] {
  return extractUsageRecords(data)
    .map((item) => ({
      rule_id: String(item.rule_id ?? item.ruleId ?? '').trim(),
      group: String(item.group ?? '').trim(),
      remaining_total: toOptionalNumber(item.remaining_total),
      remaining_model_1: toOptionalNumber(item.remaining_model_1),
      remaining_model_2: toOptionalNumber(item.remaining_model_2),
      window: toWindowSize(item.window),
    }))
    .filter((item) => item.rule_id)
}

export function buildModelSplitUsageRows(
  rules: ModelSplitRule[],
  usages: ModelSplitUsageItem[],
  savedRules: ModelSplitRule[] = []
): ModelSplitUsageRow[] {
  const usageMap = new Map<string, ModelSplitUsageItem>()
  for (const usage of usages) {
    usageMap.set(`${usage.rule_id}\0${usage.group}`, usage)
  }
  const savedById = new Map<string, ModelSplitRule>()
  for (const saved of savedRules) {
    savedById.set(saved.id, saved)
  }
  const rows: ModelSplitUsageRow[] = []
  for (const rule of rules) {
    // Server windows are keyed to the saved rule, so show the saved percents
    // for already-stored rules and only fall back to the local value for rules
    // that have not been saved yet.
    const saved = savedById.get(rule.id)
    for (const group of rule.groups) {
      const usage = usageMap.get(`${rule.id}\0${group}`)
      rows.push({
        key: `${rule.id}:${group}`,
        rule_id: rule.id,
        rule_name: rule.name,
        group,
        remaining_total: usage?.remaining_total ?? null,
        window: usage?.window ?? MODEL_SPLIT_DEFAULT_WINDOW,
        model_1: rule.model_1,
        model_2: rule.model_2,
        percent_1: saved?.percent_1 ?? rule.percent_1,
        percent_2: saved?.percent_2 ?? rule.percent_2,
        remaining_model_1: usage?.remaining_model_1 ?? null,
        remaining_model_2: usage?.remaining_model_2 ?? null,
      })
    }
  }
  return rows
}

export type SplitModelQuotaDisplay = {
  showPercent: boolean
  used: number | null
}

export function resolveSplitModelQuotaDisplay(
  remaining: number | null,
  percent: number
): SplitModelQuotaDisplay {
  // Server windows are keyed to the saved rule. After the admin lowers one
  // side's percent, the live window can still hold more remaining requests
  // than the new percent (e.g. remaining 30 with percent 20). Showing
  // "30 / 20" and a negative Used would be misleading, so drop the stale
  // denominator and hide Used until the next window picks up the new percent.
  if (remaining != null && remaining > percent) {
    return { showPercent: false, used: null }
  }
  return {
    showPercent: true,
    used: remaining == null ? null : percent - remaining,
  }
}

function extractUsageRecords(data: unknown): Record<string, unknown>[] {
  if (Array.isArray(data)) {
    return data.filter(isRecord)
  }
  if (!isRecord(data)) return []
  if (Array.isArray(data.items)) return data.items.filter(isRecord)
  if (Array.isArray(data.usages)) return data.usages.filter(isRecord)
  return []
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function uniqueTrimmedStrings(values: unknown[]): string[] {
  const result: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const trimmed = String(value).trim()
    if (!trimmed || seen.has(trimmed)) continue
    seen.add(trimmed)
    result.push(trimmed)
  }
  return result
}

function toNumber(value: unknown, fallback: number): number {
  const parsed = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(parsed)) return fallback
  return parsed
}

function toRequiredInteger(value: unknown): number {
  if (typeof value === 'number' && Number.isInteger(value)) {
    return value
  }
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value)
    if (Number.isInteger(parsed)) return parsed
  }
  throw new Error('Percents must be integers between 0 and 100')
}

function toOptionalNumber(value: unknown): number | null {
  if (value == null || value === '') return null
  const parsed = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(parsed)) return null
  return parsed
}

function toWindowSize(value: unknown): number {
  const parsed = toNumber(value, MODEL_SPLIT_DEFAULT_WINDOW)
  if (!Number.isInteger(parsed) || parsed <= 0) {
    return MODEL_SPLIT_DEFAULT_WINDOW
  }
  return parsed
}
