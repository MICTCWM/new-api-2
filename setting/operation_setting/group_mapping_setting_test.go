package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func withGroupMapping(t *testing.T, enabled bool, rules []GroupMappingRule) {
	t.Helper()
	setting := GetGroupMappingSetting()
	originalEnabled := setting.Enabled
	originalRules := setting.Rules
	setting.Enabled = enabled
	setting.Rules = rules
	t.Cleanup(func() {
		setting.Enabled = originalEnabled
		setting.Rules = originalRules
	})
}

func TestResolveGroupMappedModelChain(t *testing.T) {
	withGroupMapping(t, true, []GroupMappingRule{{
		ID:      "r1",
		Enabled: true,
		Groups:  []string{"vip"},
		Entries: []GroupMappingEntry{
			{From: "六六", To: "五五"},
		},
	}})

	require.Equal(t, "五五", ResolveGroupMappedModel("vip", "六六"))
	require.Equal(t, "六六", ResolveGroupMappedModel("default", "六六"))
	require.Equal(t, "gpt-4", ResolveGroupMappedModel("vip", "gpt-4"))
}

func TestGroupMappingDisabledIsNoop(t *testing.T) {
	withGroupMapping(t, false, []GroupMappingRule{{
		Enabled: true,
		Groups:  []string{"vip"},
		Entries: []GroupMappingEntry{{From: "六六", To: "五五"}},
	}})
	require.Equal(t, "六六", ResolveGroupMappedModel("vip", "六六"))
}

func TestApplyGroupMappingToModelListInjectAndHide(t *testing.T) {
	withGroupMapping(t, true, []GroupMappingRule{{
		Enabled: true,
		Groups:  []string{"vip"},
		Entries: []GroupMappingEntry{{
			From:         "六六",
			To:           "五五",
			FirstModel:   "六六",
			HideOriginal: true,
			HiddenModels: []string{"五五"},
		}},
	}})

	models := ApplyGroupMappingToModelList("vip", []string{"五五", "gpt-4"})
	require.Equal(t, []string{"六六", "gpt-4"}, models)
}

func TestGetGroupDefaultModelFallsBackToDisplayName(t *testing.T) {
	withGroupMapping(t, true, []GroupMappingRule{{
		Enabled: true,
		Groups:  []string{"vip"},
		Entries: []GroupMappingEntry{{
			From:         "六六",
			To:           "五五",
			FirstModel:   "六六",
			HideOriginal: true,
			HiddenModels: []string{"五五"},
		}},
	}})
	require.Equal(t, "六六", GetGroupDefaultModel([]string{"vip"}))
}

func TestGetGroupDefaultModelPrefersConfiguredValue(t *testing.T) {
	withGroupMapping(t, true, []GroupMappingRule{{
		Enabled: true,
		Groups:  []string{"vip"},
		Entries: []GroupMappingEntry{{
			From:         "六六",
			To:           "五五",
			DefaultModel: "六六",
		}},
	}})
	require.Equal(t, "六六", GetGroupDefaultModel([]string{"vip"}))
}

func TestValidateGroupMappingRejectsCycleAndConflict(t *testing.T) {
	err := ValidateGroupMappingRules([]GroupMappingRule{{
		Enabled: true,
		Groups:  []string{"vip"},
		Entries: []GroupMappingEntry{
			{From: "a", To: "b"},
			{From: "b", To: "a"},
		},
	}})
	require.Error(t, err)

	err = ValidateGroupMappingRules([]GroupMappingRule{
		{
			Enabled: true,
			Groups:  []string{"vip"},
			Entries: []GroupMappingEntry{{From: "六六", To: "五五"}},
		},
		{
			Enabled: true,
			Groups:  []string{"vip"},
			Entries: []GroupMappingEntry{{From: "六六", To: "gpt-4"}},
		},
	})
	require.Error(t, err)
}

func TestWalkModelMappingSelfMapIsNoop(t *testing.T) {
	mapped, changed, err := WalkModelMapping(map[string]string{"gpt-4": "gpt-4"}, "gpt-4")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, "gpt-4", mapped)
}
