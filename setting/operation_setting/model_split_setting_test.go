// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func withModelSplit(t *testing.T, enabled bool, rules []ModelSplitRule) {
	t.Helper()
	setting := GetModelSplitSetting()
	originalEnabled := setting.Enabled
	originalRules := setting.Rules
	setting.Enabled = enabled
	setting.Rules = rules
	t.Cleanup(func() {
		setting.Enabled = originalEnabled
		setting.Rules = originalRules
	})
}

func validSplitRule(id string, groups []string, original string) ModelSplitRule {
	return ModelSplitRule{
		ID:            id,
		Name:          id,
		Enabled:       true,
		Groups:        groups,
		OriginalModel: original,
		Model1:        "model-a",
		Percent1:      57,
		Model2:        "model-b",
		Percent2:      43,
	}
}

func TestValidateModelSplitRulesPercentSum(t *testing.T) {
	rule := validSplitRule("r1", []string{"vip"}, "nn")
	rule.Percent1 = 40
	rule.Percent2 = 50
	require.Error(t, ValidateModelSplitRules([]ModelSplitRule{rule}))

	rule.Percent1 = 101
	rule.Percent2 = -1
	require.Error(t, ValidateModelSplitRules([]ModelSplitRule{rule}))

	rule.Percent1 = 0
	rule.Percent2 = 100
	require.NoError(t, ValidateModelSplitRules([]ModelSplitRule{rule}))
}

func TestValidateModelSplitRulesConflictAndDisabled(t *testing.T) {
	enabledA := validSplitRule("r1", []string{"vip", "default"}, "nn")
	enabledB := validSplitRule("r2", []string{"vip"}, "nn")
	require.Error(t, ValidateModelSplitRules([]ModelSplitRule{enabledA, enabledB}))

	disabledB := enabledB
	disabledB.Enabled = false
	disabledB.Percent1 = 1
	disabledB.Percent2 = 1
	disabledB.Groups = nil
	require.NoError(t, ValidateModelSplitRules([]ModelSplitRule{enabledA, disabledB}))

	otherModel := validSplitRule("r3", []string{"vip"}, "other")
	require.NoError(t, ValidateModelSplitRules([]ModelSplitRule{enabledA, otherModel}))
}

func TestValidateModelSplitRulesImageModel(t *testing.T) {
	rule := validSplitRule("r1", []string{"vip"}, "nn")
	rule.ImageSplitEnabled = true
	rule.ImageModel = "model-c"
	require.Error(t, ValidateModelSplitRules([]ModelSplitRule{rule}))

	rule.ImageModel = "model-a"
	require.NoError(t, ValidateModelSplitRules([]ModelSplitRule{rule}))

	rule.ImageModel = "model-b"
	require.NoError(t, ValidateModelSplitRules([]ModelSplitRule{rule}))

	emptyEnabled := validSplitRule("r2", []string{}, "nn")
	require.Error(t, ValidateModelSplitRules([]ModelSplitRule{emptyEnabled}))

	sameModels := validSplitRule("r3", []string{"vip"}, "nn")
	sameModels.Model2 = sameModels.Model1
	require.Error(t, ValidateModelSplitRules([]ModelSplitRule{sameModels}))
}

func TestValidateModelSplitRulesJSONEmpty(t *testing.T) {
	require.NoError(t, ValidateModelSplitRulesJSON(""))
	require.NoError(t, ValidateModelSplitRulesJSON("[]"))
	require.Error(t, ValidateModelSplitRulesJSON("{"))
}

func TestValidateModelSplitRulesRequireNonEmptyUniqueIDs(t *testing.T) {
	emptyID := validSplitRule("r1", []string{"vip"}, "nn")
	emptyID.ID = ""
	err := ValidateModelSplitRules([]ModelSplitRule{emptyID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "id is required")

	whitespaceID := validSplitRule("r1", []string{"vip"}, "nn")
	whitespaceID.ID = "   "
	err = ValidateModelSplitRules([]ModelSplitRule{whitespaceID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "id is required")

	disabledEmpty := validSplitRule("r1", []string{"vip"}, "nn")
	disabledEmpty.Enabled = false
	disabledEmpty.ID = ""
	disabledEmpty.Groups = nil
	err = ValidateModelSplitRules([]ModelSplitRule{disabledEmpty})
	require.Error(t, err)
	require.Contains(t, err.Error(), "id is required")

	dupA := validSplitRule("r1", []string{"vip"}, "nn")
	dupB := validSplitRule("r1", []string{"default"}, "other")
	err = ValidateModelSplitRules([]ModelSplitRule{dupA, dupB})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicated")
	require.Contains(t, err.Error(), "r1")

	trimDupA := validSplitRule("r1", []string{"vip"}, "nn")
	trimDupB := validSplitRule(" r1 ", []string{"default"}, "other")
	err = ValidateModelSplitRules([]ModelSplitRule{trimDupA, trimDupB})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicated")

	enabled := validSplitRule("r1", []string{"vip"}, "nn")
	disabledDup := validSplitRule("r1", []string{"default"}, "other")
	disabledDup.Enabled = false
	disabledDup.Groups = nil
	err = ValidateModelSplitRules([]ModelSplitRule{enabled, disabledDup})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicated")

	require.NoError(t, ValidateModelSplitRules([]ModelSplitRule{
		validSplitRule("r1", []string{"vip"}, "nn"),
		validSplitRule("r2", []string{"vip"}, "other"),
	}))
}

func TestMatchModelSplitRuleByGroupsAndEnabledSwitch(t *testing.T) {
	rule := validSplitRule("r1", []string{"vip"}, "nn")
	withModelSplit(t, true, []ModelSplitRule{rule})
	// Non-auto groups keep matching by literal group name.
	require.NotNil(t, MatchModelSplitRule("vip", "nn"))
	require.Nil(t, MatchModelSplitRule("auto", "nn"))
	require.Nil(t, MatchModelSplitRule("vip", "other"))

	// Auto requests match by their resolved candidate groups, even though the
	// literal using group is "auto".
	require.Nil(t, MatchModelSplitRuleForGroups([]string{"auto"}, "nn"))
	require.Nil(t, MatchModelSplitRuleForGroups([]string{"default"}, "nn"))
	require.NotNil(t, MatchModelSplitRuleForGroups([]string{"vip", "default"}, "nn"))
	require.NotNil(t, MatchModelSplitRuleForGroups([]string{"default", "vip"}, "nn"))
	require.Nil(t, MatchModelSplitRuleForGroups([]string{"vip"}, "other"))
	require.Nil(t, MatchModelSplitRuleForGroups(nil, "nn"))

	// A rule that literally targets "auto" still matches the compatibility call.
	autoRule := validSplitRule("r2", []string{"auto"}, "nn")
	withModelSplit(t, true, []ModelSplitRule{autoRule})
	require.NotNil(t, MatchModelSplitRule("auto", "nn"))
	require.Nil(t, MatchModelSplitRule("vip", "nn"))

	withModelSplit(t, false, []ModelSplitRule{rule})
	require.Nil(t, MatchModelSplitRule("vip", "nn"))
	require.Nil(t, MatchModelSplitRuleForGroups([]string{"vip", "default"}, "nn"))
}
