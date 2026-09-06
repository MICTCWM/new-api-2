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
