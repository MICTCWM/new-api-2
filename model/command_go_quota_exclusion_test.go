package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func TestSettleCommandCostForModelExcludesCommandGo(t *testing.T) {
	useCommandUsageDB(t)
	channel := &Channel{
		Type:   constant.ChannelTypeCommandGo,
		Status: common.ChannelStatusEnabled,
		Key:    "k63",
		Models: CommandV41FlashModel,
	}
	require.NoError(t, DB.Create(channel).Error)

	now := time.Unix(1_800_000_000, 0)
	require.NoError(t, SettleCommandCostForModel(channel.Id, CommandV41FlashModel, 5_000_000, now))

	var channelUsageCount int64
	require.NoError(t, DB.Model(&CommandChannelUsage{}).Where("channel_id = ?", channel.Id).Count(&channelUsageCount).Error)
	require.Zero(t, channelUsageCount, "command go must not write channel-level usage windows")

	var poolCount int64
	require.NoError(t, DB.Model(&CommandPoolUsage{}).Count(&poolCount).Error)
	require.Zero(t, poolCount, "command go must not write command pool usage")
}

func TestCommandGoCapacityOnlyChecksConcurrency(t *testing.T) {
	useCommandUsageDB(t)
	channel := &Channel{
		Type:   constant.ChannelTypeCommandGo,
		Status: common.ChannelStatusEnabled,
		Key:    "k63",
		Models: CommandV41FlashModel,
	}
	require.NoError(t, DB.Create(channel).Error)

	now := int64(1_800_000_000)
	// Rolling windows are fully exhausted; only concurrency should matter.
	require.NoError(t, DB.Create(&CommandChannelUsage{
		ChannelID:      channel.Id,
		FiveHourUsed:   CommandFiveHourLimitMicroUSD,
		FiveHourStart:  now,
		WeekUsed:       CommandWeekLimitMicroUSD,
		WeekStart:      now,
		MonthUsed:      CommandMonthLimitMicroUSD,
		MonthStart:     now,
		RPMWindowStart: now,
	}).Error)

	available, err := CommandGoChannelHasConcurrencyCapacity(channel, now)
	require.NoError(t, err)
	require.True(t, available)

	// Quota-only and concurrency checks both consider 63 available while the
	// concurrency slot is free.
	available, err = CodeProviderChannelHasCapacity(channel, CommandV41FlashModel, now, false)
	require.NoError(t, err)
	require.True(t, available)
	available, err = CodeProviderChannelHasCapacity(channel, CommandV41FlashModel, now, true)
	require.NoError(t, err)
	require.True(t, available)

	require.NoError(t, DB.Model(&CommandChannelUsage{}).
		Where("channel_id = ?", channel.Id).
		Update("concurrency_count", operation_setting.GetOpencodeConcurrencyLimit()).Error)
	available, err = CommandGoChannelHasConcurrencyCapacity(channel, now)
	require.NoError(t, err)
	require.False(t, available)

	// Even with every rolling quota exhausted, the quota reconcile path must
	// never auto-disable a Command Go channel.
	require.NoError(t, ReconcileCodeProviderQuotaChannel(channel.Id, now))
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	require.Equal(t, common.ChannelStatusEnabled, stored.Status)
}

func TestEnsureCommandPoolUsageRowsExcludesCommandGo(t *testing.T) {
	useCommandUsageDB(t)
	channel := &Channel{
		Type:   constant.ChannelTypeCommandGo,
		Status: common.ChannelStatusEnabled,
		Key:    "k63",
		Models: CommandV41FlashModel,
	}
	require.NoError(t, DB.Create(channel).Error)

	require.NoError(t, EnsureCommandPoolUsageRows())

	var poolCount int64
	require.NoError(t, DB.Model(&CommandPoolUsage{}).Count(&poolCount).Error)
	require.Zero(t, poolCount, "command go channels must not get command pool rows")
}
