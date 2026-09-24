// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func withResponseRewrite(t *testing.T, mutate func(setting *ResponseRewriteSetting)) {
	t.Helper()
	setting := GetResponseRewriteSetting()
	original := *setting
	t.Cleanup(func() {
		*setting = original
	})
	setting.ModelIdEnabled = false
	setting.ModelIdGroups = []string{}
	setting.TextEnabled = false
	setting.TextRules = []ResponseRewriteRule{}
	mutate(setting)
}

func textRule(id string, groups []string, find string, replace string) ResponseRewriteRule {
	return ResponseRewriteRule{ID: id, Name: id, Groups: groups, Find: find, Replace: replace}
}

func TestValidateResponseRewriteTextRulesJSON(t *testing.T) {
	require.NoError(t, ValidateResponseRewriteTextRulesJSON(""))
	require.NoError(t, ValidateResponseRewriteTextRulesJSON("   "))
	require.NoError(t, ValidateResponseRewriteTextRulesJSON("[]"))
	require.NoError(t, ValidateResponseRewriteTextRulesJSON(`[{"id":"r1","find":"a","replace":"b"}]`))
	// Malformed JSON must be rejected rather than silently ignored.
	require.Error(t, ValidateResponseRewriteTextRulesJSON("{"))
}

func TestValidateResponseRewriteTextRules(t *testing.T) {
	require.NoError(t, ValidateResponseRewriteTextRules(nil))
	require.NoError(t, ValidateResponseRewriteTextRules([]ResponseRewriteRule{
		textRule("r1", nil, "a", "b"),
		textRule("r2", []string{"vip"}, "c", "d"),
	}))

	err := ValidateResponseRewriteTextRules([]ResponseRewriteRule{textRule("", nil, "a", "b")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "id is required")

	err = ValidateResponseRewriteTextRules([]ResponseRewriteRule{textRule("  ", nil, "a", "b")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "id is required")

	err = ValidateResponseRewriteTextRules([]ResponseRewriteRule{
		textRule("r1", nil, "a", "b"),
		textRule(" r1 ", nil, "c", "d"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicated")

	err = ValidateResponseRewriteTextRules([]ResponseRewriteRule{textRule("r1", nil, "", "b")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "find is required")
}

func TestValidateResponseRewriteTextRulesRejectsDuplicateFindWithinGroup(t *testing.T) {
	// Same explicit group, duplicated find.
	err := ValidateResponseRewriteTextRules([]ResponseRewriteRule{
		textRule("r1", []string{"vip"}, "SECRET", "a"),
		textRule("r2", []string{"vip"}, "SECRET", "b"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicated in the same group")

	// Two group-specific rules that overlap only through a shared group.
	err = ValidateResponseRewriteTextRules([]ResponseRewriteRule{
		textRule("r1", []string{"vip", "default"}, "SECRET", "a"),
		textRule("r2", []string{"default"}, "SECRET", "b"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicated in the same group")

	// Both rules are global (empty groups).
	err = ValidateResponseRewriteTextRules([]ResponseRewriteRule{
		textRule("r1", nil, "SECRET", "a"),
		textRule("r2", []string{}, "SECRET", "b"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicated in the same group")
}

func TestValidateResponseRewriteTextRulesRejectsGlobalGroupCrossDuplicate(t *testing.T) {
	// Global rule first, group-specific rule second.
	err := ValidateResponseRewriteTextRules([]ResponseRewriteRule{
		textRule("r1", nil, "SECRET", "a"),
		textRule("r2", []string{"vip"}, "SECRET", "b"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicated in the same group")

	// Group-specific rule first, global rule second.
	err = ValidateResponseRewriteTextRules([]ResponseRewriteRule{
		textRule("r1", []string{"vip"}, "SECRET", "a"),
		textRule("r2", nil, "SECRET", "b"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicated in the same group")
}

func TestValidateResponseRewriteTextRulesAllowsSameFindAcrossDisjointGroups(t *testing.T) {
	require.NoError(t, ValidateResponseRewriteTextRules([]ResponseRewriteRule{
		textRule("r1", []string{"vip"}, "SECRET", "a"),
		textRule("r2", []string{"default"}, "SECRET", "b"),
	}))
}

func TestMatchResponseRewriteRulesForGroups(t *testing.T) {
	global := textRule("global", nil, "a", "b")
	emptyGroups := textRule("empty", []string{}, "c", "d")
	vipOnly := textRule("vip", []string{"vip"}, "e", "f")

	// Disabled switch means no rule applies even when configured.
	withResponseRewrite(t, func(setting *ResponseRewriteSetting) {
		setting.TextEnabled = false
		setting.TextRules = []ResponseRewriteRule{global}
	})
	require.Nil(t, MatchResponseRewriteRulesForGroups([]string{"vip"}))

	withResponseRewrite(t, func(setting *ResponseRewriteSetting) {
		setting.TextEnabled = true
		setting.TextRules = []ResponseRewriteRule{global, emptyGroups}
	})
	// A rule with no groups applies globally, including to an empty candidate set.
	require.Equal(t, []ResponseRewriteRule{global, emptyGroups}, MatchResponseRewriteRulesForGroups([]string{"vip"}))
	require.Equal(t, []ResponseRewriteRule{global, emptyGroups}, MatchResponseRewriteRulesForGroups(nil))

	withResponseRewrite(t, func(setting *ResponseRewriteSetting) {
		setting.TextEnabled = true
		setting.TextRules = []ResponseRewriteRule{global, vipOnly}
	})
	require.Equal(t, []ResponseRewriteRule{global, vipOnly}, MatchResponseRewriteRulesForGroups([]string{"vip", "default"}))
	require.Equal(t, []ResponseRewriteRule{global}, MatchResponseRewriteRulesForGroups([]string{"default"}))
	// Ordering must follow configuration order, not candidate group order.
	require.Equal(t, []ResponseRewriteRule{global, vipOnly}, MatchResponseRewriteRulesForGroups([]string{"default", "vip"}))

	// A rule whose find is blank can never apply, so it is skipped entirely.
	blankFind := textRule("blank", nil, "   ", "b")
	withResponseRewrite(t, func(setting *ResponseRewriteSetting) {
		setting.TextEnabled = true
		setting.TextRules = []ResponseRewriteRule{blankFind, global}
	})
	require.Equal(t, []ResponseRewriteRule{global}, MatchResponseRewriteRulesForGroups([]string{"vip"}))

	// No configured rules at all yields nil so callers can skip rewriting.
	withResponseRewrite(t, func(setting *ResponseRewriteSetting) {
		setting.TextEnabled = true
	})
	require.Nil(t, MatchResponseRewriteRulesForGroups([]string{"vip"}))
}

func TestResponseRewriteModelIdAppliesToGroups(t *testing.T) {
	// Disabled switch never applies.
	withResponseRewrite(t, func(setting *ResponseRewriteSetting) {
		setting.ModelIdEnabled = false
		setting.ModelIdGroups = []string{}
	})
	require.False(t, ResponseRewriteModelIdAppliesToGroups([]string{"vip"}))

	// Enabled with empty groups means all groups.
	withResponseRewrite(t, func(setting *ResponseRewriteSetting) {
		setting.ModelIdEnabled = true
		setting.ModelIdGroups = []string{}
	})
	require.True(t, ResponseRewriteModelIdAppliesToGroups([]string{"vip"}))
	require.True(t, ResponseRewriteModelIdAppliesToGroups([]string{"auto"}))
	require.True(t, ResponseRewriteModelIdAppliesToGroups(nil))

	// Enabled with explicit groups only matches those groups.
	withResponseRewrite(t, func(setting *ResponseRewriteSetting) {
		setting.ModelIdEnabled = true
		setting.ModelIdGroups = []string{"vip"}
	})
	require.True(t, ResponseRewriteModelIdAppliesToGroups([]string{"vip"}))
	require.True(t, ResponseRewriteModelIdAppliesToGroups([]string{"default", "vip"}))
	require.False(t, ResponseRewriteModelIdAppliesToGroups([]string{"default"}))
	require.False(t, ResponseRewriteModelIdAppliesToGroups(nil))
}
