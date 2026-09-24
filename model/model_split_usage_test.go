// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useModelSplitDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ModelSplitUsage{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func TestDecideModelSplitZeroPercentSideNeverChosenForNoImage(t *testing.T) {
	_, r2, total, side, deductTotal := decideModelSplit(0, 100, 100, 0, 100, 0)
	require.Equal(t, 2, side)
	require.True(t, deductTotal)
	require.Equal(t, 99, r2)
	require.Equal(t, 99, total)
}

func TestDecideModelSplitInterleave57_43(t *testing.T) {
	r1, r2, total, side, deductTotal := decideModelSplit(57, 43, 100, 57, 43, 0)
	require.Equal(t, 1, side)
	require.True(t, deductTotal)
	require.Equal(t, 56, r1)
	require.Equal(t, 43, r2)
	require.Equal(t, 99, total)

	r1, r2, total, side, deductTotal = decideModelSplit(r1, r2, total, 57, 43, 0)
	require.Equal(t, 2, side)
	require.True(t, deductTotal)
	require.Equal(t, 56, r1)
	require.Equal(t, 42, r2)
	require.Equal(t, 98, total)
}

func TestDecideModelSplitImageDebtAndNoImageSkipsNonPositive(t *testing.T) {
	r1, r2, total := 57, 43, 100
	var side int
	var deductTotal bool
	for i := 0; i < 57; i++ {
		r1, r2, total, side, deductTotal = decideModelSplit(r1, r2, total, 57, 43, 1)
		require.Equal(t, 1, side)
		require.True(t, deductTotal)
	}
	require.Equal(t, 0, r1)
	require.Equal(t, 43, r2)
	require.Equal(t, 43, total)

	r1, r2, total, side, deductTotal = decideModelSplit(r1, r2, total, 57, 43, 1)
	require.Equal(t, 1, side)
	require.False(t, deductTotal)
	require.Equal(t, -1, r1)
	require.Equal(t, 43, r2)
	require.Equal(t, 43, total)

	r1, r2, total, side, deductTotal = decideModelSplit(r1, r2, total, 57, 43, 0)
	require.Equal(t, 2, side)
	require.True(t, deductTotal)
	require.Equal(t, -1, r1)
	require.Equal(t, 42, r2)
	require.Equal(t, 42, total)
}

func TestDecideModelSplitNoImageNeverSelectsDebtSide(t *testing.T) {
	// side2 is deeply negative from image debt; a normal request must stay on
	// the side that still has balance.
	r1, r2, total, side, deductTotal := decideModelSplit(30, -50, 80, 57, 43, 0)
	require.Equal(t, 1, side)
	require.True(t, deductTotal)
	require.Equal(t, 29, r1)
	require.Equal(t, -50, r2)
	require.Equal(t, 79, total)

	// Symmetric case: side1 in debt, side2 has balance.
	r1, r2, total, side, deductTotal = decideModelSplit(-50, 30, 80, 57, 43, 0)
	require.Equal(t, 2, side)
	require.True(t, deductTotal)
	require.Equal(t, -50, r1)
	require.Equal(t, 29, r2)
	require.Equal(t, 79, total)

	// A zero-percent side with balance is only picked when the weighted side is
	// exhausted, so a normal request is never routed into the debt side.
	_, _, _, side, deductTotal = decideModelSplit(5, -50, 20, 0, 100, 0)
	require.Equal(t, 1, side)
	require.True(t, deductTotal)
}

func TestDecideModelSplitRefillAddsQuotaAndKeepsDebt(t *testing.T) {
	r1, r2, total, side, deductTotal := decideModelSplit(-80, 0, 20, 57, 43, 1)
	require.Equal(t, 1, side)
	require.False(t, deductTotal)
	require.Equal(t, -24, r1) // -80+57-1
	require.Equal(t, 43, r2)
	require.Equal(t, 120, total) // 20+100, image debt does not deduct T

	r1, r2, total, side, deductTotal = decideModelSplit(r1, r2, total, 57, 43, 0)
	require.Equal(t, 2, side)
	require.True(t, deductTotal)
	require.Equal(t, -24, r1)
	require.Equal(t, 42, r2)
	require.Equal(t, 119, total)
}

func TestReserveRefundAndGroupIsolation(t *testing.T) {
	useModelSplitDB(t)

	first, err := ReserveModelSplitUsage("rule-1", "vip", 57, 43, 0)
	require.NoError(t, err)
	require.Equal(t, 1, first.Side)
	require.True(t, first.DeductTotal)

	second, err := ReserveModelSplitUsage("rule-1", "vip", 57, 43, 0)
	require.NoError(t, err)
	require.Equal(t, 2, second.Side)

	otherGroup, err := ReserveModelSplitUsage("rule-1", "default", 57, 43, 0)
	require.NoError(t, err)
	require.Equal(t, 1, otherGroup.Side)

	vip, err := GetModelSplitUsage("rule-1", "vip")
	require.NoError(t, err)
	require.Equal(t, 56, vip.Remaining1)
	require.Equal(t, 42, vip.Remaining2)
	require.Equal(t, 98, vip.RemainingTotal)

	defaultUsage, err := GetModelSplitUsage("rule-1", "default")
	require.NoError(t, err)
	require.Equal(t, 56, defaultUsage.Remaining1)
	require.Equal(t, 43, defaultUsage.Remaining2)
	require.Equal(t, 99, defaultUsage.RemainingTotal)

	require.NoError(t, RefundModelSplitUsage(second))
	vip, err = GetModelSplitUsage("rule-1", "vip")
	require.NoError(t, err)
	require.Equal(t, 56, vip.Remaining1)
	require.Equal(t, 43, vip.Remaining2)
	require.Equal(t, 99, vip.RemainingTotal)

	debt, err := ReserveModelSplitUsage("rule-1", "vip", 57, 43, 1)
	require.NoError(t, err)
	for debt.DeductTotal {
		debt, err = ReserveModelSplitUsage("rule-1", "vip", 57, 43, 1)
		require.NoError(t, err)
	}
	require.False(t, debt.DeductTotal)
	before, err := GetModelSplitUsage("rule-1", "vip")
	require.NoError(t, err)
	require.LessOrEqual(t, before.Remaining1, 0)

	require.NoError(t, RefundModelSplitUsage(debt))
	after, err := GetModelSplitUsage("rule-1", "vip")
	require.NoError(t, err)
	require.Equal(t, before.Remaining1+1, after.Remaining1)
	require.Equal(t, before.RemainingTotal, after.RemainingTotal)
}

func TestSavingRulesDoesNotClearModelSplitUsage(t *testing.T) {
	useModelSplitDB(t)
	_, err := ReserveModelSplitUsage("rule-keep", "vip", 60, 40, 0)
	require.NoError(t, err)
	usage, err := GetModelSplitUsage("rule-keep", "vip")
	require.NoError(t, err)
	require.Equal(t, 59, usage.Remaining1)
	require.Equal(t, 99, usage.RemainingTotal)

	rows, err := ListModelSplitUsages()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 59, rows[0].Remaining1)
}

func TestValidateOptionValueModelSplitRules(t *testing.T) {
	require.NoError(t, validateOptionValue(operation_setting.ModelSplitRulesOptionKey, "[]"))
	require.Error(t, validateOptionValue(operation_setting.ModelSplitRulesOptionKey, "{"))
	require.Error(t, validateOptionValue(operation_setting.ModelSplitRulesOptionKey, `[{"enabled":true,"groups":["vip"],"original_model":"nn","model_1":"a","percent_1":40,"model_2":"b","percent_2":50}]`))
}
