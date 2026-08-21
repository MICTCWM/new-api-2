package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useCommandUsageDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &CommandChannelUsage{}, &CommandPoolUsage{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func TestCommandUsageUsesOneCombinedPoolAcrossPricingPeriods(t *testing.T) {
	useCommandUsageDB(t)
	peakNow := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	offPeakNow := time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC)
	halfPool := CommandPoolLimitMicroUSD / 2

	require.NoError(t, SettleCommandCost(501, halfPool, peakNow))
	require.NoError(t, SettleCommandCost(501, halfPool, offPeakNow))

	snapshot, err := GetCommandUsageSnapshot(501, offPeakNow.Unix())
	require.NoError(t, err)
	require.Equal(t, CommandFiveHourLimitMicroUSD, snapshot.FiveHour.LimitMicroUSD)
	require.Equal(t, CommandWeekLimitMicroUSD, snapshot.Week.LimitMicroUSD)
	require.Equal(t, CommandMonthLimitMicroUSD, snapshot.Month.LimitMicroUSD)
	require.Equal(t, CommandPoolLimitMicroUSD, snapshot.Pool.LimitMicroUSD)
	require.Equal(t, CommandPoolLimitMicroUSD, snapshot.Pool.UsedMicroUSD)
	require.Equal(t, OpencodePricingOffPeak, snapshot.PricingPeriod)
}

func TestCommandUsageKeepsRPMPerChannel(t *testing.T) {
	useCommandUsageDB(t)
	now := int64(3_000)

	allowed, resetAt, err := ReserveCommandRPM(502, now)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Equal(t, now+60, resetAt)

	snapshot, err := GetCommandUsageSnapshot(502, now)
	require.NoError(t, err)
	require.Equal(t, 1, snapshot.RPMCount)
	require.NoError(t, ReleaseCommandRPM(502, now+1))
}

func TestCalculateCommandCostMicroUSDForModelUsesProPricing(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     2_000_000,
		CompletionTokens: 1_000_000,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 500_000,
		},
	}
	offPeakNow := time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC)
	peakNow := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)

	flashCost, err := CalculateCommandCostMicroUSDForModel(usage, CommandFlashModel, offPeakNow)
	require.NoError(t, err)
	require.Equal(t, int64(993_500), flashCost)

	proCost, err := CalculateCommandCostMicroUSDForModel(usage, CommandProModel, offPeakNow)
	require.NoError(t, err)
	require.Equal(t, int64(2_981_000), proCost)

	peakProCost, err := CalculateCommandCostMicroUSDForModel(usage, CommandProModel, peakNow)
	require.NoError(t, err)
	require.Equal(t, int64(5_962_000), peakProCost)
}

func TestCommandUsageSplitsPoolsWhenProIsConfigured(t *testing.T) {
	useCommandUsageDB(t)
	channel := &Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "command-key",
		Models: CommandFlashModel + "," + CommandProModel,
		Group:  "default",
	}
	require.NoError(t, DB.Create(channel).Error)

	now := time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC)
	require.NoError(t, SettleCommandCostForModel(channel.Id, CommandFlashModel, 40_000_000, now))
	require.NoError(t, SettleCommandCostForModel(channel.Id, CommandProModel, 15_000_000, now))

	snapshot, err := GetCommandUsageSnapshot(channel.Id, now.Unix())
	require.NoError(t, err)
	require.True(t, snapshot.SplitPools)
	require.Equal(t, int64(50_000_000), snapshot.FlashPool.LimitMicroUSD)
	require.Equal(t, int64(40_000_000), snapshot.FlashPool.UsedMicroUSD)
	require.Equal(t, int64(20_000_000), snapshot.ProPool.LimitMicroUSD)
	require.Equal(t, int64(15_000_000), snapshot.ProPool.UsedMicroUSD)
	require.Equal(t, int64(70_000_000), snapshot.Pool.LimitMicroUSD)
	require.Equal(t, int64(55_000_000), snapshot.Pool.UsedMicroUSD)
}

func TestCommandUsageMigratesLegacyPoolIntoFlashPool(t *testing.T) {
	useCommandUsageDB(t)
	channel := &Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "command-key",
		Models: CommandFlashModel + "," + CommandProModel,
		Group:  "default",
	}
	require.NoError(t, DB.Create(channel).Error)
	require.NoError(t, DB.Create(&CommandPoolUsage{
		PoolKey: CommandPoolKey(channel.Id),
		Used:    45_000_000,
	}).Error)

	snapshot, err := GetCommandUsageSnapshot(channel.Id, time.Now().Unix())
	require.NoError(t, err)
	require.Equal(t, int64(45_000_000), snapshot.FlashPool.UsedMicroUSD)
	require.Equal(t, int64(0), snapshot.ProPool.UsedMicroUSD)

	var legacy CommandPoolUsage
	require.NoError(t, DB.Where("pool_key = ?", CommandPoolKey(channel.Id)).First(&legacy).Error)
	require.Equal(t, int64(0), legacy.Used)
}

func TestSetCommandUsageOverrideUpdatesConfiguredPoolsAndPreservesRPM(t *testing.T) {
	useCommandUsageDB(t)
	channel := &Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "command-key",
		Models: CommandFlashModel + "," + CommandProModel,
		Group:  "default",
	}
	require.NoError(t, DB.Create(channel).Error)
	now := time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC)
	allowed, _, err := ReserveCommandRPM(channel.Id, now.Unix())
	require.NoError(t, err)
	require.True(t, allowed)

	flash := int64(12_000_000)
	pro := int64(4_000_000)
	require.NoError(t, SetCommandUsageOverride(channel.Id, CodeProviderUsageOverride{
		FiveHourUsedMicroUSD:  3_000_000,
		WeekUsedMicroUSD:      9_000_000,
		MonthUsedMicroUSD:     18_000_000,
		PoolUsedMicroUSD:      flash + pro,
		FlashPoolUsedMicroUSD: &flash,
		ProPoolUsedMicroUSD:   &pro,
	}, now.Unix()))

	snapshot, err := GetCommandUsageSnapshot(channel.Id, now.Unix())
	require.NoError(t, err)
	require.Equal(t, int64(3_000_000), snapshot.FiveHour.UsedMicroUSD)
	require.Equal(t, flash, snapshot.FlashPool.UsedMicroUSD)
	require.Equal(t, pro, snapshot.ProPool.UsedMicroUSD)
	require.Equal(t, flash+pro, snapshot.Pool.UsedMicroUSD)
	require.Equal(t, 1, snapshot.RPMCount)
}
