package model

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

var errTestCalibration = errors.New("calibration upstream unreachable")

func TestApplyCommandUsageCalibrationOverwritesWindows(t *testing.T) {
	useCommandUsageDB(t)
	channel := &Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "command-key",
		Models: CommandV41FlashModel + "," + CommandProModel,
		Group:  "default",
	}
	require.NoError(t, DB.Create(channel).Error)
	now := time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC).Unix()
	// 本地估算先积累一些用量。
	require.NoError(t, SettleCommandCostForModel(channel.Id, CommandV41FlashModel, 6_000_000, time.Unix(now, 0)))
	// 记录一次校准失败，校准成功后错误应被清空。
	require.NoError(t, RecordCommandCalibrationFailure(channel.Id, now, errTestCalibration))

	// 上游：5h 用 $3（还有 2h 重置），周用 $8（3 天后重置），月累计 $20。
	calib := CommandWindowCalibration{
		HasFiveHour:   true,
		FiveHourUsed:  3_000_000,
		FiveHourReset: now + 2*60*60,
		HasWeek:       true,
		WeekUsed:      8_000_000,
		WeekReset:     now + 3*24*60*60,
		HasMonth:      true,
		MonthUsed:     20_000_000,
	}
	require.NoError(t, ApplyCommandUsageCalibration(channel.Id, calib, now))

	snapshot, err := GetCommandUsageSnapshot(channel.Id, now)
	require.NoError(t, err)
	require.Equal(t, int64(3_000_000), snapshot.FiveHour.UsedMicroUSD)
	// 5h 窗口起点对齐上游重置时间：start = reset - 5h → 窗口重置 = 上游 reset。
	require.Equal(t, calib.FiveHourReset, snapshot.FiveHour.ResetAt)
	require.Equal(t, int64(8_000_000), snapshot.Week.UsedMicroUSD)
	require.Equal(t, int64(20_000_000), snapshot.Month.UsedMicroUSD)

	var usage CommandChannelUsage
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).First(&usage).Error)
	require.Empty(t, usage.CalibrationLastError)
}

func TestApplyCommandUsageCalibrationExpiredResetStartsFresh(t *testing.T) {
	useCommandUsageDB(t)
	channel := &Channel{Type: constant.ChannelTypeCommand, Status: common.ChannelStatusEnabled, Key: "k", Models: CommandFlashModel}
	require.NoError(t, DB.Create(channel).Error)
	now := int64(1_800_000_000)
	require.NoError(t, SettleCommandCostForModel(channel.Id, CommandFlashModel, 4_000_000, time.Unix(now, 0)))

	// 上游窗口已过期重置（resetAt <= now），视为新窗口。
	calib := CommandWindowCalibration{
		HasFiveHour:    true,
		FiveHourUsed:   0,
		FiveHourReset:  now - 60,
		ResetAllZeroOK: true,
	}
	require.NoError(t, ApplyCommandUsageCalibration(channel.Id, calib, now))

	snapshot, err := GetCommandUsageSnapshot(channel.Id, now)
	require.NoError(t, err)
	require.Equal(t, int64(0), snapshot.FiveHour.UsedMicroUSD)
}

func TestClaimCommandCalibrationAttemptStaggersChannels(t *testing.T) {
	useCommandUsageDB(t)
	now := int64(1_800_000_000)

	// 首次调用只排期，不认领。
	claimed, err := ClaimCommandCalibrationAttempt(701, now)
	require.NoError(t, err)
	require.False(t, claimed)
	claimed, err = ClaimCommandCalibrationAttempt(702, now)
	require.NoError(t, err)
	require.False(t, claimed)

	// 不同渠道排到不同时隙。
	var usage701, usage702 CommandChannelUsage
	require.NoError(t, DB.Where("channel_id = ?", 701).First(&usage701).Error)
	require.NoError(t, DB.Where("channel_id = ?", 702).First(&usage702).Error)
	require.NotEqual(t, usage701.NextCalibrationAt, usage702.NextCalibrationAt)
	require.Greater(t, usage701.NextCalibrationAt, now)

	// 到期后可认领，且认领推进下次时间。
	claimed, err = ClaimCommandCalibrationAttempt(701, usage701.NextCalibrationAt)
	require.NoError(t, err)
	require.True(t, claimed)
	var usage701b CommandChannelUsage
	require.NoError(t, DB.Where("channel_id = ?", 701).First(&usage701b).Error)
	require.Equal(t, usage701.NextCalibrationAt, usage701b.LastCalibrationAttemptAt)
	require.Greater(t, usage701b.NextCalibrationAt, usage701.NextCalibrationAt)
}
