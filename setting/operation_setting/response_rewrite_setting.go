// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package operation_setting

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

const (
	ResponseRewriteModelIdEnabledOptionKey = "response_rewrite_setting.model_id_enabled"
	ResponseRewriteModelIdGroupsOptionKey  = "response_rewrite_setting.model_id_groups"
	ResponseRewriteTextEnabledOptionKey    = "response_rewrite_setting.text_enabled"
	ResponseRewriteTextRulesOptionKey      = "response_rewrite_setting.text_rules"
)

type ResponseRewriteRule struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Groups  []string `json:"groups"`
	Find    string   `json:"find"`
	Replace string   `json:"replace"`
}

type ResponseRewriteSetting struct {
	ModelIdEnabled bool                  `json:"model_id_enabled"`
	ModelIdGroups  []string              `json:"model_id_groups"`
	TextEnabled    bool                  `json:"text_enabled"`
	TextRules      []ResponseRewriteRule `json:"text_rules"`
}

var responseRewriteSetting = ResponseRewriteSetting{
	ModelIdEnabled: false,
	ModelIdGroups:  []string{},
	TextEnabled:    false,
	TextRules:      []ResponseRewriteRule{},
}

func init() {
	config.GlobalConfig.Register("response_rewrite_setting", &responseRewriteSetting)
}

func GetResponseRewriteSetting() *ResponseRewriteSetting {
	return &responseRewriteSetting
}

func ValidateResponseRewriteTextRulesJSON(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var rules []ResponseRewriteRule
	if err := common.UnmarshalJsonStr(trimmed, &rules); err != nil {
		return fmt.Errorf("response rewrite text rules must be a JSON array")
	}
	return ValidateResponseRewriteTextRules(rules)
}

func ValidateResponseRewriteTextRules(rules []ResponseRewriteRule) error {
	seenIDs := make(map[string]int, len(rules))
	// Empty groups mean the rule applies to every group, so its find must not
	// collide with either the global bucket or any group-specific bucket.
	globalFinds := make(map[string]int, len(rules))
	findsByGroup := make(map[string]map[string]int, len(rules))
	for i, rule := range rules {
		index := i + 1
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			return fmt.Errorf("rule %d: id is required", index)
		}
		if prev, exists := seenIDs[id]; exists {
			return fmt.Errorf("rule %d: id %q is duplicated with rule %d", index, id, prev)
		}
		seenIDs[id] = index
		find := rule.Find
		if strings.TrimSpace(find) == "" {
			return fmt.Errorf("rule %d: find is required", index)
		}
		if prev, exists := globalFinds[find]; exists {
			return fmt.Errorf("rule %d: find %q is duplicated in the same group with rule %d", index, find, prev)
		}
		groups := uniqueTrimmed(rule.Groups)
		if len(groups) == 0 {
			for _, groupFinds := range findsByGroup {
				if prev, exists := groupFinds[find]; exists {
					return fmt.Errorf("rule %d: find %q is duplicated in the same group with rule %d", index, find, prev)
				}
			}
			globalFinds[find] = index
			continue
		}
		for _, group := range groups {
			groupFinds := findsByGroup[group]
			if groupFinds == nil {
				groupFinds = make(map[string]int)
				findsByGroup[group] = groupFinds
			}
			if prev, exists := groupFinds[find]; exists {
				return fmt.Errorf("rule %d: find %q is duplicated in the same group with rule %d", index, find, prev)
			}
			groupFinds[find] = index
		}
	}
	return nil
}

// MatchResponseRewriteRulesForGroups returns the enabled text rules that apply
// to any of the candidate groups. A rule with no groups applies globally.
func MatchResponseRewriteRulesForGroups(groups []string) []ResponseRewriteRule {
	setting := &responseRewriteSetting
	if !setting.TextEnabled || len(setting.TextRules) == 0 {
		return nil
	}
	rules := make([]ResponseRewriteRule, 0, len(setting.TextRules))
	for _, rule := range setting.TextRules {
		if strings.TrimSpace(rule.Find) == "" {
			continue
		}
		if !responseRewriteGroupsMatch(rule.Groups, groups) {
			continue
		}
		rules = append(rules, rule)
	}
	if len(rules) == 0 {
		return nil
	}
	return rules
}

// ResponseRewriteModelIdAppliesToGroups reports whether model id rewriting is
// enabled for any of the candidate groups. Empty groups means all groups.
func ResponseRewriteModelIdAppliesToGroups(groups []string) bool {
	setting := &responseRewriteSetting
	if !setting.ModelIdEnabled {
		return false
	}
	return responseRewriteGroupsMatch(setting.ModelIdGroups, groups)
}

func responseRewriteGroupsMatch(ruleGroups []string, groups []string) bool {
	trimmedRuleGroups := uniqueTrimmed(ruleGroups)
	if len(trimmedRuleGroups) == 0 {
		return true
	}
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		for _, ruleGroup := range trimmedRuleGroups {
			if ruleGroup == group {
				return true
			}
		}
	}
	return false
}
