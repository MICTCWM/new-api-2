// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"math"
	"math/rand"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

const (
	// Logged first-token time uses a 0.1s grid, matching the UI display.
	logTimingDisplayStepMs = 100.0

	logTimingFrtFreeMs       = 5000.0
	logTimingFrtExactLimitMs = 15000.0
	logTimingFrtHighMinMs    = 8000.0
	logTimingFrtHighMaxMs    = 15000.0
	logTimingFrtExactMinMs   = 5000.0
	logTimingFrtExactMaxMs   = 10000.0
)
var logTimingRandIntn = rand.Intn

func pickLogTimingDisplayMs(lowMs, highMs float64) float64 {
	lowStep := math.Ceil(lowMs / logTimingDisplayStepMs)
	highStep := math.Floor(highMs / logTimingDisplayStepMs)
	if highStep < lowStep {
		highStep = lowStep
	}
	steps := int(highStep - lowStep)
	return (lowStep + float64(logTimingRandIntn(steps+1))) * logTimingDisplayStepMs
}

func optimizedFirstTokenDisplayMs(frtMs float64) float64 {
	if frtMs < logTimingFrtFreeMs {
		return frtMs
	}
	// "exactly 15s" follows the UI, which renders one decimal: every real value
	// displayed as 15.0s must land in the same band, so an exact float compare
	// would leave the 5s-10s band effectively unreachable. toFixed rounds
	// 14950 down, hence the open interval on the 0.1s grid.
	if frtMs > logTimingFrtExactLimitMs-logTimingDisplayStepMs/2 &&
		frtMs < logTimingFrtExactLimitMs+logTimingDisplayStepMs/2 {
		return pickLogTimingDisplayMs(logTimingFrtExactMinMs, logTimingFrtExactMaxMs)
	}
	if frtMs < logTimingFrtExactLimitMs {
		return pickLogTimingDisplayMs(frtMs*2/3, frtMs)
	}
	return pickLogTimingDisplayMs(logTimingFrtHighMinMs, logTimingFrtHighMaxMs)
}

func logOtherFrtMillis(other map[string]interface{}) (float64, bool) {
	if other == nil {
		return 0, false
	}
	switch value := other["frt"].(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}

// applyLogTimingDisplayOptimization rewrites the first-token latency (frt, in
// milliseconds) and the use time recorded on a usage log. It mutates
// other["frt"] when a rewrite applies and returns the possibly shortened use
// time. Real timing values are intentionally discarded.
func applyLogTimingDisplayOptimization(group string, other map[string]interface{}, useTimeSeconds int, isStream bool) int {
	if !isStream || useTimeSeconds <= 0 {
		return useTimeSeconds
	}
	frtMs, ok := logOtherFrtMillis(other)
	if !ok || frtMs < logTimingFrtFreeMs {
		return useTimeSeconds
	}
	if !operation_setting.LogTimingOptimizationAppliesToGroup(group) {
		return useTimeSeconds
	}
	displayedFrtMs := optimizedFirstTokenDisplayMs(frtMs)
	if displayedFrtMs >= frtMs {
		return useTimeSeconds
	}
	savedSeconds := int(math.Round((frtMs - displayedFrtMs) / 1000))
	newUseTime := useTimeSeconds - savedSeconds
	// The displayed duration can never be shorter than the displayed
	// first-token time, otherwise the row contradicts itself.
	minUseTime := int(math.Ceil(displayedFrtMs / 1000))
	if minUseTime > useTimeSeconds {
		minUseTime = useTimeSeconds
	}
	if newUseTime < minUseTime {
		newUseTime = minUseTime
	}
	if newUseTime < 1 {
		newUseTime = 1
	}
	other["frt"] = displayedFrtMs
	return newUseTime
}
