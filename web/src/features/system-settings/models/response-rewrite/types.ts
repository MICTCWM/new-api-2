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
export type ResponseRewriteRule = {
  id: string
  name: string
  groups: string[]
  find: string
  replace: string
}

export type ResponseRewriteSettings = {
  'response_rewrite_setting.model_id_enabled': boolean
  'response_rewrite_setting.model_id_groups': string
  'response_rewrite_setting.text_enabled': boolean
  'response_rewrite_setting.text_rules': string
}

export function createResponseRewriteRuleId(): string {
  return `rewrite-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

export function parseResponseRewriteGroups(jsonStr: string): string[] {
  try {
    const parsed = JSON.parse(jsonStr || '[]')
    if (!Array.isArray(parsed)) return []
    return uniqueTrimmedStrings(parsed)
  } catch {
    return []
  }
}

export function serializeResponseRewriteGroups(groups: string[]): string {
  return JSON.stringify(uniqueTrimmedStrings(groups))
}

export function parseResponseRewriteRules(
  jsonStr: string
): ResponseRewriteRule[] {
  try {
    const parsed = JSON.parse(jsonStr || '[]')
    if (!Array.isArray(parsed)) return []
    return parsed.map((rule, index) =>
      normalizeRule(rule, () => `${createResponseRewriteRuleId()}${index}`)
    )
  } catch {
    return []
  }
}

export function strictParseResponseRewriteRules(
  jsonStr: string
): ResponseRewriteRule[] {
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
    return normalizeRule(item, createResponseRewriteRuleId)
  })
}

export function serializeResponseRewriteRules(
  rules: ResponseRewriteRule[]
): string {
  return JSON.stringify(
    rules.map((rule) => ({
      id: rule.id,
      name: rule.name.trim(),
      groups: uniqueTrimmedStrings(rule.groups),
      find: rule.find,
      replace: rule.replace,
    }))
  )
}

export function validateResponseRewriteRule(rule: {
  find: string
}): string | null {
  if (!rule.find.trim()) return 'Find fragment is required'
  return null
}

export function validateResponseRewriteRules(
  rules: ResponseRewriteRule[]
): string | null {
  const ids = new Set<string>()
  // Empty groups mean the rule applies to every group, so it must be checked
  // against both the global bucket and every group-specific bucket.
  const globalFinds = new Set<string>()
  const findsByGroup = new Map<string, Set<string>>()

  for (const rule of rules) {
    const id = rule.id.trim()
    if (!id) return 'Rule ID is required'
    if (ids.has(id)) return 'Rule IDs must be unique'
    ids.add(id)

    const errorKey = validateResponseRewriteRule(rule)
    if (errorKey) return errorKey

    const find = rule.find
    if (globalFinds.has(find)) {
      return 'Duplicate find fragment in the same group'
    }
    const groups = uniqueTrimmedStrings(rule.groups)
    if (groups.length === 0) {
      for (const groupFinds of findsByGroup.values()) {
        if (groupFinds.has(find)) {
          return 'Duplicate find fragment in the same group'
        }
      }
      globalFinds.add(find)
      continue
    }
    for (const group of groups) {
      let groupFinds = findsByGroup.get(group)
      if (!groupFinds) {
        groupFinds = new Set<string>()
        findsByGroup.set(group, groupFinds)
      }
      if (groupFinds.has(find)) {
        return 'Duplicate find fragment in the same group'
      }
      groupFinds.add(find)
    }
  }
  return null
}

function normalizeRule(
  rule: unknown,
  createId: () => string
): ResponseRewriteRule {
  const record = isRecord(rule) ? rule : {}
  return {
    id: String(record.id ?? '').trim() || createId(),
    name: String(record.name ?? '').trim(),
    groups: Array.isArray(record.groups)
      ? uniqueTrimmedStrings(record.groups)
      : [],
    find: String(record.find ?? ''),
    replace: String(record.replace ?? ''),
  }
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
