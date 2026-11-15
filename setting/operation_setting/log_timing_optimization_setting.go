// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package operation_setting

import (
	"strings"

	"github.com/QuantumNous/new-api/setting/config"
)

const (
	LogTimingOptimizationEnabledOptionKey = "log_timing_optimization.enabled"
	LogTimingOptimizationGroupsOptionKey  = "log_timing_optimization.groups"
)

// LogTimingOptimizationSetting controls the display-only first-token / duration
// rewrite for usage logs. Real values are never persisted; only the rewritten
// values reach the log record.
type LogTimingOptimizationSetting struct {
	Enabled bool     `json:"enabled"`
	Groups  []string `json:"groups"`
}

var logTimingOptimizationSetting = LogTimingOptimizationSetting{
	Enabled: false,
	Groups:  []string{},
}

func init() {
	config.GlobalConfig.Register("log_timing_optimization", &logTimingOptimizationSetting)
}

func GetLogTimingOptimizationSetting() *LogTimingOptimizationSetting {
	return &logTimingOptimizationSetting
}

// LogTimingOptimizationAppliesToGroup reports whether usage-log timing display
// optimization is enabled for the given group. An empty group list means no
// group is optimized, matching the explicit group selection in the UI.
func LogTimingOptimizationAppliesToGroup(group string) bool {
	setting := &logTimingOptimizationSetting
	if !setting.Enabled {
		return false
	}
	group = strings.TrimSpace(group)
	if group == "" {
		return false
	}
	for _, candidate := range setting.Groups {
		if strings.TrimSpace(candidate) == group {
			return true
		}
	}
	return false
}
