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
	ModelSplitEnabledOptionKey = "model_split_setting.enabled"
	ModelSplitRulesOptionKey   = "model_split_setting.rules"
	ModelSplitWindowSize       = 100
)

type ModelSplitRule struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Enabled           bool     `json:"enabled"`
	Groups            []string `json:"groups"`
	OriginalModel     string   `json:"original_model"`
	Model1            string   `json:"model_1"`
	Percent1          int      `json:"percent_1"`
	Model2            string   `json:"model_2"`
	Percent2          int      `json:"percent_2"`
	ImageSplitEnabled bool     `json:"image_split_enabled"`
	ImageModel        string   `json:"image_model"`
}

type ModelSplitSetting struct {
	Enabled bool             `json:"enabled"`
	Rules   []ModelSplitRule `json:"rules"`
}

var modelSplitSetting = ModelSplitSetting{
	Enabled: false,
	Rules:   []ModelSplitRule{},
}

func init() {
	config.GlobalConfig.Register("model_split_setting", &modelSplitSetting)
}

func GetModelSplitSetting() *ModelSplitSetting {
	return &modelSplitSetting
}

func ValidateModelSplitRulesJSON(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var rules []ModelSplitRule
	if err := common.UnmarshalJsonStr(trimmed, &rules); err != nil {
		return fmt.Errorf("model split rules must be a JSON array")
	}
	return ValidateModelSplitRules(rules)
}

func ValidateModelSplitRules(rules []ModelSplitRule) error {
	sourceByGroup := make(map[string]map[string]string)
	seenIDs := make(map[string]int, len(rules))
	for i, rule := range rules {
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			return fmt.Errorf("rule %d: id is required", i+1)
		}
		if prev, exists := seenIDs[id]; exists {
			return fmt.Errorf("rule %d: id %q is duplicated with rule %d", i+1, id, prev)
		}
		seenIDs[id] = i + 1
		if !rule.Enabled {
			continue
		}
		groups := uniqueTrimmed(rule.Groups)
		if len(groups) == 0 {
			return fmt.Errorf("rule %d: at least one group is required", i+1)
		}
		original := strings.TrimSpace(rule.OriginalModel)
		model1 := strings.TrimSpace(rule.Model1)
		model2 := strings.TrimSpace(rule.Model2)
		if original == "" {
			return fmt.Errorf("rule %d: original_model is required", i+1)
		}
		if model1 == "" || model2 == "" {
			return fmt.Errorf("rule %d: model_1 and model_2 are required", i+1)
		}
		if model1 == model2 {
			return fmt.Errorf("rule %d: model_1 and model_2 must be different", i+1)
		}
		if rule.Percent1 < 0 || rule.Percent1 > 100 || rule.Percent2 < 0 || rule.Percent2 > 100 {
			return fmt.Errorf("rule %d: percent_1 and percent_2 must be integers between 0 and 100", i+1)
		}
		if rule.Percent1+rule.Percent2 != ModelSplitWindowSize {
			return fmt.Errorf("rule %d: percent_1 and percent_2 must sum to %d", i+1, ModelSplitWindowSize)
		}
		if rule.ImageSplitEnabled {
			imageModel := strings.TrimSpace(rule.ImageModel)
			if imageModel != model1 && imageModel != model2 {
				return fmt.Errorf("rule %d: image_model must equal model_1 or model_2", i+1)
			}
		}
		for _, group := range groups {
			existing, ok := sourceByGroup[group]
			if !ok {
				existing = make(map[string]string)
				sourceByGroup[group] = existing
			}
			if _, exists := existing[original]; exists {
				return fmt.Errorf("group %s has conflicting split rules for %s", group, original)
			}
			existing[original] = strings.TrimSpace(rule.ID)
		}
	}
	return nil
}

// MatchModelSplitRuleForGroups matches against a candidate group list so that
// Auto requests can hit rules by their real (resolved) groups.
func MatchModelSplitRuleForGroups(groups []string, originalModel string) *ModelSplitRule {
	if !modelSplitSetting.Enabled {
		return nil
	}
	originalModel = strings.TrimSpace(originalModel)
	if originalModel == "" {
		return nil
	}
	for i := range modelSplitSetting.Rules {
		rule := modelSplitSetting.Rules[i]
		if !rule.Enabled {
			continue
		}
		if strings.TrimSpace(rule.OriginalModel) != originalModel {
			continue
		}
		if !modelSplitRuleAppliesToGroups(rule, groups) {
			continue
		}
		return &rule
	}
	return nil
}

func MatchModelSplitRule(group, originalModel string) *ModelSplitRule {
	return MatchModelSplitRuleForGroups([]string{group}, originalModel)
}

func modelSplitRuleAppliesToGroups(rule ModelSplitRule, groups []string) bool {
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		for _, item := range rule.Groups {
			if strings.TrimSpace(item) == group {
				return true
			}
		}
	}
	return false
}
