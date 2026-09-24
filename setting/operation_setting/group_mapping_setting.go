package operation_setting

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

const GroupMappingRulesOptionKey = "group_mapping_setting.rules"
const GroupMappingEnabledOptionKey = "group_mapping_setting.enabled"

type GroupMappingEntry struct {
	From         string   `json:"from"`
	To           string   `json:"to"`
	HideOriginal bool     `json:"hide_original"`
	HiddenModels []string `json:"hidden_models,omitempty"`
	FirstModel   string   `json:"first_model,omitempty"`
	DefaultModel string   `json:"default_model,omitempty"`
}

type GroupMappingRule struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Enabled bool                `json:"enabled"`
	Groups  []string            `json:"groups"`
	Entries []GroupMappingEntry `json:"entries"`
}

type GroupMappingSetting struct {
	Enabled bool               `json:"enabled"`
	Rules   []GroupMappingRule `json:"rules"`
}

var groupMappingSetting = GroupMappingSetting{
	Enabled: true,
	Rules:   []GroupMappingRule{},
}

func init() {
	config.GlobalConfig.Register("group_mapping_setting", &groupMappingSetting)
}

func GetGroupMappingSetting() *GroupMappingSetting {
	return &groupMappingSetting
}

func ValidateGroupMappingRulesJSON(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var rules []GroupMappingRule
	if err := common.UnmarshalJsonStr(trimmed, &rules); err != nil {
		return fmt.Errorf("group mapping rules must be a JSON array")
	}
	return ValidateGroupMappingRules(rules)
}

func ValidateGroupMappingRules(rules []GroupMappingRule) error {
	sourceByGroup := make(map[string]map[string]string)
	for i, rule := range rules {
		if !rule.Enabled {
			continue
		}
		groups := uniqueTrimmed(rule.Groups)
		if len(groups) == 0 {
			return fmt.Errorf("rule %d: at least one group is required", i+1)
		}
		seenFrom := make(map[string]struct{})
		mapping := make(map[string]string)
		for j, entry := range rule.Entries {
			from := strings.TrimSpace(entry.From)
			to := strings.TrimSpace(entry.To)
			if from == "" || to == "" {
				return fmt.Errorf("rule %d mapping %d: original and replacement models are required", i+1, j+1)
			}
			if _, dup := seenFrom[from]; dup {
				return fmt.Errorf("rule %d: duplicate source model %s", i+1, from)
			}
			seenFrom[from] = struct{}{}
			mapping[from] = to
			if first := strings.TrimSpace(entry.FirstModel); first != "" && first != from {
				if _, dup := mapping[first]; dup && mapping[first] != to {
					return fmt.Errorf("rule %d: display model %s conflicts with another mapping", i+1, first)
				}
				mapping[first] = to
			}
		}
		if _, err := detectMappingCycles(mapping); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
		for _, group := range groups {
			existing, ok := sourceByGroup[group]
			if !ok {
				existing = make(map[string]string)
				sourceByGroup[group] = existing
			}
			for from, to := range mapping {
				if prev, exists := existing[from]; exists && prev != to {
					return fmt.Errorf("group %s has conflicting mappings for %s", group, from)
				}
				existing[from] = to
			}
		}
	}
	for group, mapping := range sourceByGroup {
		if _, err := detectMappingCycles(mapping); err != nil {
			return fmt.Errorf("group %s: %w", group, err)
		}
	}
	return nil
}

func GetGroupMappingMap(group string) map[string]string {
	group = strings.TrimSpace(group)
	if group == "" || !groupMappingSetting.Enabled {
		return nil
	}
	merged := make(map[string]string)
	for _, rule := range groupMappingSetting.Rules {
		if !rule.Enabled || !ruleAppliesToGroup(rule, group) {
			continue
		}
		for _, entry := range rule.Entries {
			from := strings.TrimSpace(entry.From)
			to := strings.TrimSpace(entry.To)
			if from == "" || to == "" {
				continue
			}
			merged[from] = to
			if first := strings.TrimSpace(entry.FirstModel); first != "" && first != from {
				merged[first] = to
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func ResolveGroupMappedModel(group, modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return modelName
	}
	mapped, changed, err := WalkModelMapping(GetGroupMappingMap(group), modelName)
	if err != nil || !changed || mapped == "" {
		return modelName
	}
	return mapped
}

func GetGroupHiddenModels(group string) map[string]struct{} {
	hidden := make(map[string]struct{})
	group = strings.TrimSpace(group)
	if group == "" || !groupMappingSetting.Enabled {
		return hidden
	}
	for _, rule := range groupMappingSetting.Rules {
		if !rule.Enabled || !ruleAppliesToGroup(rule, group) {
			continue
		}
		for _, entry := range rule.Entries {
			if !entry.HideOriginal {
				continue
			}
			for _, modelName := range entry.HiddenModels {
				modelName = strings.TrimSpace(modelName)
				if modelName == "" {
					continue
				}
				hidden[modelName] = struct{}{}
			}
		}
	}
	return hidden
}

func GetGroupPublicModels(group string) []string {
	group = strings.TrimSpace(group)
	if group == "" || !groupMappingSetting.Enabled {
		return nil
	}
	seen := make(map[string]struct{})
	public := make([]string, 0)
	for _, rule := range groupMappingSetting.Rules {
		if !rule.Enabled || !ruleAppliesToGroup(rule, group) {
			continue
		}
		for _, entry := range rule.Entries {
			name := strings.TrimSpace(entry.FirstModel)
			if name == "" {
				name = strings.TrimSpace(entry.From)
			}
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			public = append(public, name)
		}
	}
	return public
}

func GetGroupDefaultModel(groups []string) string {
	if !groupMappingSetting.Enabled {
		return ""
	}
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		hidden := GetGroupHiddenModels(group)
		var fallback string
		for _, rule := range groupMappingSetting.Rules {
			if !rule.Enabled || !ruleAppliesToGroup(rule, group) {
				continue
			}
			for _, entry := range rule.Entries {
				if defaultModel := strings.TrimSpace(entry.DefaultModel); defaultModel != "" {
					if _, skip := hidden[defaultModel]; !skip {
						return defaultModel
					}
				}
				name := strings.TrimSpace(entry.FirstModel)
				if name == "" {
					name = strings.TrimSpace(entry.From)
				}
				if name == "" {
					continue
				}
				if _, skip := hidden[name]; skip {
					continue
				}
				if fallback == "" {
					fallback = name
				}
			}
		}
		if fallback != "" {
			return fallback
		}
	}
	return ""
}

func ApplyGroupMappingToModelList(group string, models []string) []string {
	hidden := GetGroupHiddenModels(group)
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, modelName := range models {
		if _, skip := hidden[modelName]; skip {
			continue
		}
		if _, ok := seen[modelName]; ok {
			continue
		}
		seen[modelName] = struct{}{}
		result = append(result, modelName)
	}
	for _, publicName := range GetGroupPublicModels(group) {
		if _, skip := hidden[publicName]; skip {
			continue
		}
		if _, ok := seen[publicName]; ok {
			continue
		}
		seen[publicName] = struct{}{}
		result = append(result, publicName)
	}
	if defaultModel := GetGroupDefaultModel([]string{group}); defaultModel != "" {
		for i, name := range result {
			if name != defaultModel {
				continue
			}
			if i > 0 {
				copy(result[1:i+1], result[:i])
				result[0] = defaultModel
			}
			break
		}
	}
	return result
}

func WalkModelMapping(modelMap map[string]string, start string) (string, bool, error) {
	if start == "" || len(modelMap) == 0 {
		return start, false, nil
	}
	current := start
	visited := map[string]bool{current: true}
	mapped := false
	for {
		next, exists := modelMap[current]
		if !exists || next == "" {
			break
		}
		if visited[next] {
			if next == current {
				if current == start {
					return start, false, nil
				}
				return current, true, nil
			}
			return start, false, errors.New("model_mapping_contains_cycle")
		}
		visited[next] = true
		current = next
		mapped = true
	}
	return current, mapped, nil
}

func detectMappingCycles(modelMap map[string]string) (bool, error) {
	for from := range modelMap {
		if _, _, err := WalkModelMapping(modelMap, from); err != nil {
			return true, err
		}
	}
	return false, nil
}

func ruleAppliesToGroup(rule GroupMappingRule, group string) bool {
	for _, item := range rule.Groups {
		if strings.TrimSpace(item) == group {
			return true
		}
	}
	return false
}

func uniqueTrimmed(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
