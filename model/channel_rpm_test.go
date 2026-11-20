package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useChannelRPMDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &ChannelRPMUsage{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func newChannelWithRPMLimit(t *testing.T, id int, channelType int, limit int) *Channel {
	t.Helper()
	channel := &Channel{
		Id:     id,
		Type:   channelType,
		Status: common.ChannelStatusEnabled,
	}
	channel.SetOtherSettings(dto.ChannelOtherSettings{ChannelRPMLimit: limit})
	return channel
}

func TestReserveChannelRPMRejectsAfterConfiguredLimit(t *testing.T) {
	useChannelRPMDB(t)
	channel := newChannelWithRPMLimit(t, 101, constant.ChannelTypeOpenAI, 2)
	now := int64(1_000)

	for i := 0; i < 2; i++ {
		allowed, resetAt, err := ReserveChannelRPM(channel, now)
		require.NoError(t, err)
		require.True(t, allowed)
		require.Equal(t, now+ChannelRPMWindowSeconds, resetAt)
	}

	allowed, _, err := ReserveChannelRPM(channel, now)
	require.NoError(t, err)
	require.False(t, allowed)

	capacity, err := ChannelHasRPMCapacity(channel, now)
	require.NoError(t, err)
	require.False(t, capacity)
}

func TestReleaseChannelRPMReservationAllowsRetry(t *testing.T) {
	useChannelRPMDB(t)
	channel := newChannelWithRPMLimit(t, 102, constant.ChannelTypeOpenAI, 1)
	now := int64(2_000)

	allowed, _, err := ReserveChannelRPM(channel, now)
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, _, err = ReserveChannelRPM(channel, now)
	require.NoError(t, err)
	require.False(t, allowed)

	require.NoError(t, ReleaseChannelRPM(channel, now+1))
	allowed, _, err = ReserveChannelRPM(channel, now+1)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestChannelRPMWindowResetsAfterOneMinute(t *testing.T) {
	useChannelRPMDB(t)
	channel := newChannelWithRPMLimit(t, 103, constant.ChannelTypeOpenAI, 1)
	now := int64(3_000)

	allowed, _, err := ReserveChannelRPM(channel, now)
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, resetAt, err := ReserveChannelRPM(channel, now+ChannelRPMWindowSeconds)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Equal(t, now+ChannelRPMWindowSeconds*2, resetAt)
}

func TestChannelRPMLimitIsIndependentPerChannel(t *testing.T) {
	useChannelRPMDB(t)
	first := newChannelWithRPMLimit(t, 104, constant.ChannelTypeOpenAI, 1)
	second := newChannelWithRPMLimit(t, 105, constant.ChannelTypeOpenAI, 1)
	now := int64(4_000)

	allowed, _, err := ReserveChannelRPM(first, now)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, _, err = ReserveChannelRPM(second, now)
	require.NoError(t, err)
	require.True(t, allowed)

	firstCapacity, err := ChannelHasRPMCapacity(first, now)
	require.NoError(t, err)
	secondCapacity, err := ChannelHasRPMCapacity(second, now)
	require.NoError(t, err)
	require.False(t, firstCapacity)
	require.False(t, secondCapacity)
}

func TestChannelRPMLimitDoesNotApplyToCodeProviderChannels(t *testing.T) {
	useChannelRPMDB(t)
	now := int64(5_000)

	for _, channelType := range []int{constant.ChannelTypeOpencode, constant.ChannelTypeCommand} {
		channel := newChannelWithRPMLimit(t, channelType+100, channelType, 1)
		for i := 0; i < 2; i++ {
			allowed, resetAt, err := ReserveChannelRPM(channel, now)
			require.NoError(t, err)
			require.True(t, allowed)
			require.Zero(t, resetAt)
		}
		capacity, err := ChannelHasRPMCapacity(channel, now)
		require.NoError(t, err)
		require.True(t, capacity)
	}
}

func TestValidateChannelRPMLimit(t *testing.T) {
	require.NoError(t, ValidateChannelRPMLimit(0))
	require.NoError(t, ValidateChannelRPMLimit(MaxChannelRPMLimit))
	require.Error(t, ValidateChannelRPMLimit(-1))
	require.Error(t, ValidateChannelRPMLimit(MaxChannelRPMLimit+1))
}

func TestChannelConcurrencyIsReleasedExplicitlyInsteadOfByTime(t *testing.T) {
	useChannelRPMDB(t)
	channel := newChannelWithRPMLimit(t, 106, constant.ChannelTypeOpenAI, 2)
	now := int64(5_000)

	allowed, err := ReserveChannelConcurrency(channel, now)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = ReserveChannelConcurrency(channel, now)
	require.NoError(t, err)
	require.True(t, allowed)

	capacity, err := ChannelHasConcurrencyCapacity(channel, now+ChannelRPMWindowSeconds*2)
	require.NoError(t, err)
	require.False(t, capacity)

	require.NoError(t, ReleaseChannelConcurrency(channel))
	allowed, err = ReserveChannelConcurrency(channel, now+ChannelRPMWindowSeconds*2)
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, ReleaseChannelConcurrency(channel))
	require.NoError(t, ReleaseChannelConcurrency(channel))
	capacity, err = ChannelHasConcurrencyCapacity(channel, now+ChannelRPMWindowSeconds*2)
	require.NoError(t, err)
	require.True(t, capacity)
}

func newChannelWithCapacitySettings(t *testing.T, id int, channelType int, concurrencyLimit, tpmLimit, tpmBuffer int, limitMode string) *Channel {
	t.Helper()
	channel := &Channel{
		Id:     id,
		Type:   channelType,
		Status: common.ChannelStatusEnabled,
	}
	channel.SetOtherSettings(dto.ChannelOtherSettings{
		ChannelConcurrencyLimit: concurrencyLimit,
		ChannelTPMLimit:         tpmLimit,
		ChannelTPMBuffer:        common.GetPointer(tpmBuffer),
		ChannelLimitMode:        limitMode,
	})
	require.NoError(t, DB.Create(channel).Error)
	return channel
}

func getChannelTokensUsed(t *testing.T, channelID int) int64 {
	t.Helper()
	usage, err := getOrCreateChannelRPMUsage(channelID, common.GetTimestamp())
	require.NoError(t, err)
	return usage.TokensUsed
}

func TestReserveChannelCapacityRejectsWhenTPMExhausted(t *testing.T) {
	useChannelRPMDB(t)
	// 1000 tokens with a 200-token buffer enforces an 800-token window cap.
	channel := newChannelWithCapacitySettings(t, 201, constant.ChannelTypeOpenAI, 0, 1000, 200, ChannelLimitModeReject)
	now := int64(6_000)

	allowed, reason, err := ReserveChannelCapacity(channel, now, 700)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Empty(t, reason)

	allowed, reason, err = ReserveChannelCapacity(channel, now, 200)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Equal(t, "tpm", reason)

	// The rejected attempt must not consume window tokens: the remaining 100
	// tokens stay available for a smaller request.
	allowed, _, err = ReserveChannelCapacity(channel, now, 100)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestReserveChannelCapacityRejectsWhenConcurrencyFull(t *testing.T) {
	useChannelRPMDB(t)
	channel := newChannelWithCapacitySettings(t, 202, constant.ChannelTypeOpenAI, 1, 0, 0, ChannelLimitModeQueue)
	now := int64(7_000)

	allowed, reason, err := ReserveChannelCapacity(channel, now, 0)
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, reason, err = ReserveChannelCapacity(channel, now, 0)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Equal(t, "concurrency", reason)

	require.NoError(t, ReleaseChannelConcurrency(channel))
	allowed, _, err = ReserveChannelCapacity(channel, now, 0)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestRollbackChannelTPMTokensReturnsReservation(t *testing.T) {
	useChannelRPMDB(t)
	channel := newChannelWithCapacitySettings(t, 203, constant.ChannelTypeOpenAI, 0, 1000, 0, ChannelLimitModeReject)
	// Rollback/settle use the real clock internally, so the reserve must too.
	now := common.GetTimestamp()

	allowed, _, err := ReserveChannelCapacity(channel, now, 900)
	require.NoError(t, err)
	require.True(t, allowed)

	// The attempt never reached the upstream: the reservation goes back and a
	// full-size request fits again.
	require.NoError(t, RollbackChannelTPMTokens(channel, 900))
	require.Equal(t, int64(0), getChannelTokensUsed(t, channel.Id))

	allowed, _, err = ReserveChannelCapacity(channel, now, 900)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestSettleChannelTPMUsageReplacesEstimateWithRealUsage(t *testing.T) {
	useChannelRPMDB(t)
	channel := newChannelWithCapacitySettings(t, 204, constant.ChannelTypeOpenAI, 0, 1000, 0, ChannelLimitModeReject)
	now := common.GetTimestamp()

	allowed, _, err := ReserveChannelCapacity(channel, now, 700)
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, SettleChannelTPMUsage(channel.Id, 700, 900))
	require.Equal(t, int64(900), getChannelTokensUsed(t, channel.Id))

	// A second request both reserves and settles: estimate 100 in, real
	// prompt+completion 260 out bumps the window to the charged totals.
	allowed, _, err = ReserveChannelCapacity(channel, now, 100)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, SettleChannelTPMUsage(channel.Id, 100, 260))
	// A second request both reserves and settles: the window keeps its
	// settled 900, drops the 100-token estimate and charges the real 260.
	require.Equal(t, int64(1160), getChannelTokensUsed(t, channel.Id))
}

func TestSettleChannelTPMUsageSkipsChannelsWithoutTPMLimit(t *testing.T) {
	useChannelRPMDB(t)
	codeChannel := newChannelWithCapacitySettings(t, 205, constant.ChannelTypeOpencode, 0, 1000, 0, ChannelLimitModeReject)
	plainChannel := newChannelWithCapacitySettings(t, 206, constant.ChannelTypeOpenAI, 0, 0, 0, ChannelLimitModeReject)

	require.NoError(t, SettleChannelTPMUsage(codeChannel.Id, 100, 900))
	require.NoError(t, SettleChannelTPMUsage(plainChannel.Id, 100, 900))
	require.Equal(t, int64(0), getChannelTokensUsed(t, codeChannel.Id))
	require.Equal(t, int64(0), getChannelTokensUsed(t, plainChannel.Id))
}

func TestGetFallbackChannelRetryTimesRequiresOptIn(t *testing.T) {
	channel := &Channel{Id: 207}
	channel.SetOtherSettings(dto.ChannelOtherSettings{
		ChannelRetryTimes:    3,
		FallbackRetryEnabled: true,
	})
	require.Equal(t, 3, GetFallbackChannelRetryTimes(channel))

	channel.SetOtherSettings(dto.ChannelOtherSettings{ChannelRetryTimes: 3})
	require.Equal(t, 3, GetChannelRetryTimes(channel))
	require.Equal(t, 0, GetFallbackChannelRetryTimes(channel))

	channel.SetOtherSettings(dto.ChannelOtherSettings{
		ChannelRetryTimes:    9,
		FallbackRetryEnabled: true,
	})
	require.Equal(t, MaxChannelRetryTimes, GetFallbackChannelRetryTimes(channel))
}
