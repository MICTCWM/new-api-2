package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useCodeProviderQuotaDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	previousMemoryCache := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&Channel{},
		&Ability{},
		&OpencodeChannelUsage{},
		&OpencodePoolUsage{},
		&CommandChannelUsage{},
		&CommandPoolUsage{},
	))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled = previousMemoryCache
	})
}

func TestReconcileOpencodeQuotaDisablesUntilRollingReset(t *testing.T) {
	useCodeProviderQuotaDB(t)
	channel := &Channel{
		Type:   constant.ChannelTypeOpencode,
		Status: common.ChannelStatusEnabled,
		Key:    "opencode-key",
		Models: OpencodeModel,
	}
	require.NoError(t, DB.Create(channel).Error)
	now := int64(1_800_000_000)
	require.NoError(t, DB.Create(&OpencodeChannelUsage{
		ChannelID:      channel.Id,
		FiveHourUsed:   OpencodeFiveHourLimitMicroUSD,
		FiveHourStart:  now,
		WeekStart:      now,
		MonthStart:     now,
		RPMWindowStart: now,
	}).Error)

	require.NoError(t, ReconcileCodeProviderQuotaChannel(channel.Id, now))
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	require.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	resetAt, err := GetCodeProviderQuotaDisabledUntil(channel.Id)
	require.NoError(t, err)
	require.Equal(t, now+int64((5*time.Hour)/time.Second), resetAt)

	require.NoError(t, ReconcileCodeProviderQuotaChannel(channel.Id, resetAt))
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	require.Equal(t, common.ChannelStatusEnabled, stored.Status)
	resetAt, err = GetCodeProviderQuotaDisabledUntil(channel.Id)
	require.NoError(t, err)
	require.Zero(t, resetAt)
}

func TestReconcileCommandQuotaDisablesUntilLatestRollingReset(t *testing.T) {
	useCodeProviderQuotaDB(t)
	channel := &Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "command-key",
		Models: CommandFlashModel,
	}
	require.NoError(t, DB.Create(channel).Error)
	now := int64(1_800_000_000)
	require.NoError(t, DB.Create(&CommandChannelUsage{
		ChannelID:      channel.Id,
		FiveHourUsed:   CommandFiveHourLimitMicroUSD,
		FiveHourStart:  now,
		WeekUsed:       CommandWeekLimitMicroUSD,
		WeekStart:      now,
		MonthStart:     now,
		RPMWindowStart: now,
	}).Error)

	require.NoError(t, ReconcileCodeProviderQuotaChannel(channel.Id, now))
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	require.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	resetAt, err := GetCodeProviderQuotaDisabledUntil(channel.Id)
	require.NoError(t, err)
	require.Equal(t, now+int64((7*24*time.Hour)/time.Second), resetAt)

	require.NoError(t, ReconcileCodeProviderQuotaChannel(channel.Id, resetAt))
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	require.Equal(t, common.ChannelStatusEnabled, stored.Status)
}
