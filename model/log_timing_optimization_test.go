// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func withLogTimingSetting(t *testing.T, enabled bool, groups []string, randIntn func(int) int) {
	t.Helper()
	setting := operation_setting.GetLogTimingOptimizationSetting()
	previousEnabled := setting.Enabled
	previousGroups := append([]string(nil), setting.Groups...)
	previousRand := logTimingRandIntn
	setting.Enabled = enabled
	setting.Groups = append([]string(nil), groups...)
	if randIntn != nil {
		logTimingRandIntn = randIntn
	}
	t.Cleanup(func() {
		setting.Enabled = previousEnabled
		setting.Groups = previousGroups
		logTimingRandIntn = previousRand
	})
}

func TestOptimizedFirstTokenDisplayMs(t *testing.T) {
	previousRand := logTimingRandIntn
	t.Cleanup(func() { logTimingRandIntn = previousRand })
	pickFirst := func(int) int { return 0 }
	pickLast := func(n int) int { return n - 1 }

	tests := []struct {
		name     string
		frtMs    float64
		pick     func(int) int
		expected float64
	}{
		{name: "below five seconds keeps real value", frtMs: 4200, pick: pickFirst, expected: 4200},
		{name: "five to fifteen seconds lower bound", frtMs: 9000, pick: pickFirst, expected: 6000},
		{name: "five to fifteen seconds upper bound", frtMs: 9000, pick: pickLast, expected: 9000},
		{name: "exactly fifteen seconds lower bound", frtMs: 15000, pick: pickFirst, expected: 5000},
		{name: "exactly fifteen seconds upper bound", frtMs: 15000, pick: pickLast, expected: 10000},
		{name: "displays as 15.0 seconds just below the limit", frtMs: 14960, pick: pickFirst, expected: 5000},
		{name: "displays as 15.0 seconds near the limit", frtMs: 14960, pick: pickLast, expected: 10000},
		{name: "displays as 15.0 seconds just above the limit", frtMs: 15040, pick: pickFirst, expected: 5000},
		{name: "displays as 15.0 seconds upper bound", frtMs: 15040, pick: pickLast, expected: 10000},
		{name: "14950 renders 14.9 and keeps the two thirds band", frtMs: 14950, pick: pickLast, expected: 14900},
		{name: "15050 renders 15.1 and keeps the high band", frtMs: 15050, pick: pickLast, expected: 15000},
		{name: "displays as 14.9 seconds keeps the two thirds band", frtMs: 14940, pick: pickLast, expected: 14900},
		{name: "displays as 15.1 seconds keeps the high band", frtMs: 15060, pick: pickLast, expected: 15000},
		{name: "above fifteen seconds lower bound", frtMs: 15200, pick: pickFirst, expected: 8000},
		{name: "above fifteen seconds upper bound", frtMs: 30000, pick: pickLast, expected: 15000},
		{name: "uses one hundred millisecond steps", frtMs: 9000, pick: func(int) int { return 1 }, expected: 6100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logTimingRandIntn = tt.pick
			require.Equal(t, tt.expected, optimizedFirstTokenDisplayMs(tt.frtMs))
		})
	}
}

func TestApplyLogTimingDisplayOptimization(t *testing.T) {
	pickFirst := func(int) int { return 0 }
	pickLast := func(n int) int { return n - 1 }

	t.Run("shortens use time by saved first token time", func(t *testing.T) {
		withLogTimingSetting(t, true, []string{"vip"}, pickLast)
		other := map[string]interface{}{"frt": 20000.0}
		useTime := applyLogTimingDisplayOptimization("vip", other, 30, true)
		require.Equal(t, 25, useTime)
		require.Equal(t, 15000.0, other["frt"])
	})

	t.Run("clamps duration to at least the displayed first token", func(t *testing.T) {
		withLogTimingSetting(t, true, []string{"vip"}, pickLast)
		other := map[string]interface{}{"frt": 30000.0}
		useTime := applyLogTimingDisplayOptimization("vip", other, 20, true)
		require.Equal(t, 15, useTime)
		require.Equal(t, 15000.0, other["frt"])
	})

	t.Run("never exceeds original duration", func(t *testing.T) {
		withLogTimingSetting(t, true, []string{"vip"}, pickFirst)
		other := map[string]interface{}{"frt": 15000.0}
		useTime := applyLogTimingDisplayOptimization("vip", other, 12, true)
		require.Equal(t, 5, useTime)
		require.Equal(t, 5000.0, other["frt"])
	})

	t.Run("below five seconds is untouched", func(t *testing.T) {
		withLogTimingSetting(t, true, []string{"vip"}, func(int) int {
			t.Fatal("random source must not be used below five seconds")
			return 0
		})
		other := map[string]interface{}{"frt": 4000.0}
		require.Equal(t, 12, applyLogTimingDisplayOptimization("vip", other, 12, true))
		require.Equal(t, 4000.0, other["frt"])
	})

	t.Run("group outside selection is untouched", func(t *testing.T) {
		withLogTimingSetting(t, true, []string{"vip"}, pickFirst)
		other := map[string]interface{}{"frt": 20000.0}
		require.Equal(t, 30, applyLogTimingDisplayOptimization("default", other, 30, true))
		require.Equal(t, 20000.0, other["frt"])
	})

	t.Run("disabled setting is untouched", func(t *testing.T) {
		withLogTimingSetting(t, false, []string{"vip"}, pickFirst)
		other := map[string]interface{}{"frt": 20000.0}
		require.Equal(t, 30, applyLogTimingDisplayOptimization("vip", other, 30, true))
		require.Equal(t, 20000.0, other["frt"])
	})

	t.Run("non-stream request is untouched", func(t *testing.T) {
		withLogTimingSetting(t, true, []string{"vip"}, pickFirst)
		other := map[string]interface{}{"frt": 20000.0}
		require.Equal(t, 30, applyLogTimingDisplayOptimization("vip", other, 30, false))
		require.Equal(t, 20000.0, other["frt"])
	})
}

func setupLogTimingDB(t *testing.T) {
	t.Helper()
	previousLogDB := LOG_DB
	previousDB := DB
	logDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, logDB.AutoMigrate(&Log{}))
	LOG_DB = logDB
	DB = logDB
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		DB = previousDB
	})
}

func newLogTimingTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("username", "tester")
	return c
}

func TestRecordConsumeLogAppliesTimingDisplayOptimization(t *testing.T) {
	setupLogTimingDB(t)
	previousLogConsumeEnabled := common.LogConsumeEnabled
	common.LogConsumeEnabled = true
	t.Cleanup(func() { common.LogConsumeEnabled = previousLogConsumeEnabled })
	withLogTimingSetting(t, true, []string{"vip"}, func(n int) int { return n - 1 })
	c := newLogTimingTestContext()

	RecordConsumeLog(c, 42, RecordConsumeLogParams{
		ModelName:        "test-model",
		Group:            "vip",
		UseTimeSeconds:   30,
		IsStream:         true,
		CompletionTokens: 100,
		Other:            map[string]interface{}{"frt": 20000.0},
	})

	var logRecord Log
	require.NoError(t, LOG_DB.Last(&logRecord).Error)
	require.Equal(t, LogTypeConsume, logRecord.Type)
	require.Equal(t, 25, logRecord.UseTime)
	other, err := common.StrToMap(logRecord.Other)
	require.NoError(t, err)
	require.Equal(t, 15000.0, other["frt"])
}

func TestRecordErrorLogAppliesTimingDisplayOptimization(t *testing.T) {
	setupLogTimingDB(t)
	withLogTimingSetting(t, true, []string{"vip"}, func(int) int { return 0 })
	c := newLogTimingTestContext()

	RecordErrorLog(c, 42, 9, "test-model", "tk", "upstream failed", 3, 20, true, "vip",
		map[string]interface{}{"frt": 15200.0})

	var logRecord Log
	require.NoError(t, LOG_DB.Last(&logRecord).Error)
	require.Equal(t, LogTypeError, logRecord.Type)
	require.Equal(t, 13, logRecord.UseTime)
	other, err := common.StrToMap(logRecord.Other)
	require.NoError(t, err)
	require.Equal(t, 8000.0, other["frt"])
}
