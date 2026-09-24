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
export type GroupMappingEntry = {
  from: string
  to: string
  hide_original: boolean
  hidden_models?: string[]
  first_model?: string
  default_model?: string
}

export type GroupMappingRule = {
  id: string
  name: string
  enabled: boolean
  groups: string[]
  entries: GroupMappingEntry[]
}

export type GroupMappingSettings = {
  'group_mapping_setting.enabled': boolean
  'group_mapping_setting.rules': string
}

export function createGroupMappingRuleId(): string {
  return `rule-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

export function parseGroupMappingRules(jsonStr: string): GroupMappingRule[] {
  try {
    const parsed = JSON.parse(jsonStr || '[]')
    if (!Array.isArray(parsed)) return []
    return parsed.map((rule: Record<string, unknown>, index: number) => {
      const entries = Array.isArray(rule.entries)
        ? rule.entries.map((entry: Record<string, unknown>) => ({
            from: String(entry.from ?? '').trim(),
            to: String(entry.to ?? '').trim(),
            hide_original: Boolean(entry.hide_original),
            hidden_models: Array.isArray(entry.hidden_models)
              ? entry.hidden_models
                  .map((modelName) => String(modelName).trim())
                  .filter(Boolean)
              : [],
            first_model: String(entry.first_model ?? '').trim(),
            default_model: String(entry.default_model ?? '').trim(),
          }))
        : []
      return {
        id: String(rule.id ?? '').trim() || createGroupMappingRuleId() + index,
        name: String(rule.name ?? '').trim(),
        enabled: rule.enabled !== false,
        groups: Array.isArray(rule.groups)
          ? rule.groups.map((group) => String(group).trim()).filter(Boolean)
          : [],
        entries,
      }
    })
  } catch {
    return []
  }
}

export function serializeGroupMappingRules(rules: GroupMappingRule[]): string {
  return JSON.stringify(
    rules.map((rule) => ({
      id: rule.id,
      name: rule.name,
      enabled: rule.enabled,
      groups: rule.groups,
      entries: rule.entries.map((entry) => ({
        from: entry.from.trim(),
        to: entry.to.trim(),
        hide_original: entry.hide_original,
        hidden_models: entry.hide_original
          ? (entry.hidden_models ?? []).filter(Boolean)
          : [],
        first_model: (entry.first_model || entry.from).trim(),
        default_model: (entry.default_model || entry.to).trim(),
      })),
    }))
  )
}
