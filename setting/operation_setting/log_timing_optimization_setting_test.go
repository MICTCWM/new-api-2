// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogTimingOptimizationAppliesToGroup(t *testing.T) {
	setting := GetLogTimingOptimizationSetting()
	previousEnabled := setting.Enabled
	previousGroups := append([]string(nil), setting.Groups...)
	t.Cleanup(func() {
		setting.Enabled = previousEnabled
		setting.Groups = previousGroups
	})

	setting.Enabled = false
	setting.Groups = []string{"vip"}
	require.False(t, LogTimingOptimizationAppliesToGroup("vip"))

	setting.Enabled = true
	require.True(t, LogTimingOptimizationAppliesToGroup("vip"))
	require.True(t, LogTimingOptimizationAppliesToGroup(" vip "))
	require.False(t, LogTimingOptimizationAppliesToGroup("default"))
	require.False(t, LogTimingOptimizationAppliesToGroup(""))

	setting.Groups = nil
	require.False(t, LogTimingOptimizationAppliesToGroup("vip"))
}
