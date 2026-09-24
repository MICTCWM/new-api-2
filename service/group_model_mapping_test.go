package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func withServiceGroupMapping(t *testing.T, rules []operation_setting.GroupMappingRule) {
	t.Helper()
	setting := operation_setting.GetGroupMappingSetting()
	originalEnabled := setting.Enabled
	originalRules := setting.Rules
	setting.Enabled = true
	setting.Rules = rules
	t.Cleanup(func() {
		setting.Enabled = originalEnabled
		setting.Rules = originalRules
	})
}

func TestMergeGroupMappingOverridesChannelMapping(t *testing.T) {
	withServiceGroupMapping(t, []operation_setting.GroupMappingRule{{
		Enabled: true,
		Groups:  []string{"vip"},
		Entries: []operation_setting.GroupMappingEntry{
			{From: "五五", To: "group-target"},
			{From: "六六", To: "五五"},
		},
	}})

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "vip")

	merged := MergeGroupMappingIntoChannelMapping(c, `{"五五":"DBC V4 Flash","other":"keep"}`)
	var parsed map[string]string
	require.NoError(t, common.Unmarshal([]byte(merged), &parsed))
	require.Equal(t, "group-target", parsed["五五"])
	require.Equal(t, "五五", parsed["六六"])
	require.Equal(t, "keep", parsed["other"])
}

func TestApplyGroupMappingToPricingInjectsAliasAndHidesOriginal(t *testing.T) {
	withServiceGroupMapping(t, []operation_setting.GroupMappingRule{{
		Enabled: true,
		Groups:  []string{"vip"},
		Entries: []operation_setting.GroupMappingEntry{{
			From:         "六六",
			To:           "五五",
			HideOriginal: true,
			HiddenModels: []string{"五五"},
		}},
	}})

	pricing := ApplyGroupMappingToPricing([]model.Pricing{
		{ModelName: "五五", EnableGroup: []string{"vip", "default"}, ModelRatio: 1},
		{ModelName: "gpt-4", EnableGroup: []string{"vip"}, ModelRatio: 2},
	})

	byName := map[string]model.Pricing{}
	for _, item := range pricing {
		byName[item.ModelName] = item
	}
	require.Contains(t, byName, "六六")
	require.Equal(t, []string{"vip"}, byName["六六"].EnableGroup)
	require.NotContains(t, byName["五五"].EnableGroup, "vip")
	require.Contains(t, byName["五五"].EnableGroup, "default")
	require.Contains(t, byName, "gpt-4")
}

func TestGroupMappedModelHasBillingInheritsTarget(t *testing.T) {
	withServiceGroupMapping(t, []operation_setting.GroupMappingRule{{
		Enabled: true,
		Groups:  []string{"vip"},
		Entries: []operation_setting.GroupMappingEntry{{From: "六六", To: "五五"}},
	}})
	hasBilling := func(name string) bool { return name == "五五" }
	require.True(t, GroupMappedModelHasBilling([]string{"vip"}, "六六", hasBilling))
	require.False(t, GroupMappedModelHasBilling([]string{"default"}, "六六", hasBilling))
}
