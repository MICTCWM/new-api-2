package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func ResolveGroupMappedModel(group, modelName string) string {
	return operation_setting.ResolveGroupMappedModel(group, modelName)
}

func MergeGroupMappingIntoChannelMapping(c *gin.Context, channelMapping string) string {
	groupMaps := groupMappingMapsForContext(c)
	if len(groupMaps) == 0 {
		return channelMapping
	}

	merged := make(map[string]string)
	trimmed := strings.TrimSpace(channelMapping)
	if trimmed != "" && trimmed != "{}" {
		if err := common.UnmarshalJsonStr(trimmed, &merged); err != nil {
			merged = make(map[string]string)
		}
	}
	changed := false
	for _, groupMap := range groupMaps {
		for from, to := range groupMap {
			if merged[from] != to {
				changed = true
			}
			merged[from] = to
		}
	}
	if !changed && (trimmed == "" || trimmed == "{}") {
		return channelMapping
	}
	if len(merged) == 0 {
		return channelMapping
	}
	encoded, err := common.Marshal(merged)
	if err != nil {
		return channelMapping
	}
	return string(encoded)
}

func ApplyGroupMappingToPricing(pricing []model.Pricing) []model.Pricing {
	setting := operation_setting.GetGroupMappingSetting()
	if setting == nil || !setting.Enabled || len(setting.Rules) == 0 {
		return pricing
	}

	byName := make(map[string]int, len(pricing))
	for i := range pricing {
		byName[pricing[i].ModelName] = i
	}

	cloneFromTarget := func(publicName, target string, groups []string) {
		if publicName == "" {
			return
		}
		sourceName := target
		sourceIdx, ok := byName[sourceName]
		if !ok {
			sourceName = publicName
			sourceIdx, ok = byName[sourceName]
			if !ok {
				return
			}
		}
		if existingIdx, exists := byName[publicName]; exists {
			pricing[existingIdx].EnableGroup = unionStrings(pricing[existingIdx].EnableGroup, groups)
			return
		}
		cloned := pricing[sourceIdx]
		cloned.ModelName = publicName
		cloned.EnableGroup = append([]string{}, groups...)
		byName[publicName] = len(pricing)
		pricing = append(pricing, cloned)
	}

	hiddenByGroup := make(map[string]map[string]struct{})
	for _, rule := range setting.Rules {
		if !rule.Enabled {
			continue
		}
		groups := uniqueTrimmedStrings(rule.Groups)
		if len(groups) == 0 {
			continue
		}
		for _, entry := range rule.Entries {
			publicName := strings.TrimSpace(entry.FirstModel)
			if publicName == "" {
				publicName = strings.TrimSpace(entry.From)
			}
			target := strings.TrimSpace(entry.To)
			if publicName != "" {
				resolved := target
				if from := strings.TrimSpace(entry.From); from != "" {
					if mapped := operation_setting.ResolveGroupMappedModel(groups[0], from); mapped != "" {
						resolved = mapped
					}
				}
				cloneFromTarget(publicName, resolved, groups)
			}
			if !entry.HideOriginal {
				continue
			}
			for _, group := range groups {
				hidden := hiddenByGroup[group]
				if hidden == nil {
					hidden = make(map[string]struct{})
					hiddenByGroup[group] = hidden
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
	}

	if len(hiddenByGroup) == 0 {
		return pricing
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		enableGroup := make([]string, 0, len(item.EnableGroup))
		for _, group := range item.EnableGroup {
			if hidden := hiddenByGroup[group]; hidden != nil {
				if _, skip := hidden[item.ModelName]; skip {
					continue
				}
			}
			enableGroup = append(enableGroup, group)
		}
		if len(enableGroup) == 0 && len(item.EnableGroup) > 0 {
			continue
		}
		item.EnableGroup = enableGroup
		filtered = append(filtered, item)
	}
	return filtered
}

func GroupMappedModelHasBilling(groups []string, modelName string, hasBilling func(string) bool) bool {
	if hasBilling != nil && hasBilling(modelName) {
		return true
	}
	for _, group := range groups {
		mapped := operation_setting.ResolveGroupMappedModel(group, modelName)
		if mapped != modelName && hasBilling != nil && hasBilling(mapped) {
			return true
		}
	}
	return false
}

func groupMappingMapsForContext(c *gin.Context) []map[string]string {
	if c == nil {
		return nil
	}
	group := common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
	if group == "" {
		group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	}
	if group == "" {
		return nil
	}
	if group != "auto" {
		if mapping := operation_setting.GetGroupMappingMap(group); len(mapping) > 0 {
			return []map[string]string{mapping}
		}
		return nil
	}
	userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	autoGroups := GetRequestAutoGroups(c, userGroup)
	maps := make([]map[string]string, 0, len(autoGroups))
	for _, autoGroup := range autoGroups {
		if mapping := operation_setting.GetGroupMappingMap(autoGroup); len(mapping) > 0 {
			maps = append(maps, mapping)
		}
	}
	return maps
}

func uniqueTrimmedStrings(values []string) []string {
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

func unionStrings(existing []string, extra []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(extra))
	result := make([]string, 0, len(existing)+len(extra))
	for _, item := range existing {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	for _, item := range extra {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}
