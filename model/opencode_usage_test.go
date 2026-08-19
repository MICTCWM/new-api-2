package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useOpencodeUsageDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&OpencodeChannelUsage{}, &OpencodePoolUsage{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func TestCalculateOpencodeCostMicroUSDUsesPeakPricingAndCache(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     2_000_000,
		CompletionTokens: 1_000_000,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 500_000,
		},
	}

	offPeak, err := CalculateOpencodeCostMicroUSD(usage, time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, int64(993_500), offPeak)

	peak, err := CalculateOpencodeCostMicroUSD(usage, time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, int64(1_987_000), peak)
}

func TestOpencodePeakWindowBoundaries(t *testing.T) {
	for _, test := range []struct {
		hour int
		peak bool
	}{
		{hour: 0, peak: false},
		{hour: 1, peak: true},
		{hour: 3, peak: true},
		{hour: 4, peak: false},
		{hour: 5, peak: false},
		{hour: 6, peak: true},
		{hour: 9, peak: true},
		{hour: 10, peak: false},
	} {
		now := time.Date(2026, time.August, 19, test.hour, 0, 0, 0, time.UTC)
		require.Equal(t, test.peak, IsOpencodePeak(now))
	}
}

func TestCalculateOpencodeCostMicroUSDRejectsMissingUsage(t *testing.T) {
	_, err := CalculateOpencodeCostMicroUSD(nil, time.Now().UTC())
	require.Error(t, err)
}

func TestCalculateOpencodeCostMicroUSDFallsBackToProviderCacheField(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:         1_000_000,
		PromptCacheHitTokens: 400_000,
		CompletionTokens:     0,
	}

	cost, err := CalculateOpencodeCostMicroUSD(usage, time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, int64(134_800), cost)
}

func TestSettleOpencodeCostUsesTheProvidedCharge(t *testing.T) {
	useOpencodeUsageDB(t)
	now := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	cost := int64(1_987_000)

	require.NoError(t, SettleOpencodeCost(404, cost, now))

	snapshot, err := GetOpencodeUsageSnapshot(404, now.Unix())
	require.NoError(t, err)
	require.Equal(t, cost, snapshot.FiveHour.UsedMicroUSD)
	require.Equal(t, cost, snapshot.Week.UsedMicroUSD)
	require.Equal(t, cost, snapshot.Month.UsedMicroUSD)
	require.Equal(t, cost, snapshot.Pool.UsedMicroUSD)
	require.Equal(t, cost, snapshot.PeakPool.UsedMicroUSD)
	require.Equal(t, OpencodePricingPeak, snapshot.PricingPeriod)
}

func TestOpencodePeakAndOffPeakPoolsAreIndependent(t *testing.T) {
	useOpencodeUsageDB(t)
	peakNow := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	offPeakNow := time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC)

	require.NoError(t, SettleOpencodeCost(406, OpencodePoolLimitMicroUSD, peakNow))
	peakSnapshot, err := GetOpencodeUsageSnapshot(406, peakNow.Unix())
	require.NoError(t, err)
	require.Equal(t, OpencodePoolLimitMicroUSD, peakSnapshot.PeakPool.UsedMicroUSD)
	require.Equal(t, int64(0), peakSnapshot.OffPeakPool.UsedMicroUSD)

	otherChannelSnapshot, err := GetOpencodeUsageSnapshot(407, peakNow.Unix())
	require.NoError(t, err)
	require.Equal(t, int64(0), otherChannelSnapshot.PeakPool.UsedMicroUSD)
	require.Equal(t, int64(0), otherChannelSnapshot.OffPeakPool.UsedMicroUSD)
	peakAvailable, err := OpencodeChannelHasQuotaCapacity(407, peakNow.Unix())
	require.NoError(t, err)
	require.True(t, peakAvailable)

	offPeakAvailable, err := OpencodeChannelHasQuotaCapacity(407, offPeakNow.Unix())
	require.NoError(t, err)
	require.True(t, offPeakAvailable)
	require.NoError(t, SettleOpencodeCost(407, OpencodePoolLimitMicroUSD, offPeakNow))

	offPeakSnapshot, err := GetOpencodeUsageSnapshot(407, offPeakNow.Unix())
	require.NoError(t, err)
	require.Equal(t, int64(0), offPeakSnapshot.PeakPool.UsedMicroUSD)
	require.Equal(t, OpencodePoolLimitMicroUSD, offPeakSnapshot.OffPeakPool.UsedMicroUSD)
	require.Equal(t, OpencodePricingOffPeak, offPeakSnapshot.PricingPeriod)
}

func TestResetAllOpencodeUsageResetsEveryChannelAndPool(t *testing.T) {
	useOpencodeUsageDB(t)
	peakNow := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	offPeakNow := time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC)
	require.NoError(t, SettleOpencodeCost(408, 1_000, peakNow))
	require.NoError(t, SettleOpencodeCost(409, 2_000, offPeakNow))
	allowed, _, err := ReserveOpencodeRPM(408, peakNow.Unix())
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, ResetAllOpencodeUsage())
	for _, channelID := range []int{408, 409} {
		usage, err := GetOpencodeUsageSnapshot(channelID, time.Now().Unix())
		require.NoError(t, err)
		require.Equal(t, int64(0), usage.FiveHour.UsedMicroUSD)
		require.Equal(t, int64(0), usage.Week.UsedMicroUSD)
		require.Equal(t, int64(0), usage.Month.UsedMicroUSD)
		require.Equal(t, int64(0), usage.PeakPool.UsedMicroUSD)
		require.Equal(t, int64(0), usage.OffPeakPool.UsedMicroUSD)
		require.Equal(t, 0, usage.RPMCount)
	}
}

func TestSettleOpencodeCostRejectsNegativeCharge(t *testing.T) {
	useOpencodeUsageDB(t)
	require.Error(t, SettleOpencodeCost(405, -1, time.Now().UTC()))
}

func TestOpencodeRPMReservationCanBeReleasedAfterFailure(t *testing.T) {
	useOpencodeUsageDB(t)
	now := int64(1_000)

	for i := 0; i < OpencodeRPMLimit; i++ {
		allowed, resetAt, err := ReserveOpencodeRPM(101, now)
		require.NoError(t, err)
		require.True(t, allowed)
		require.Equal(t, now+60, resetAt)
	}

	allowed, _, err := ReserveOpencodeRPM(101, now)
	require.NoError(t, err)
	require.False(t, allowed)

	require.NoError(t, ReleaseOpencodeRPM(101, now+1))
	allowed, _, err = ReserveOpencodeRPM(101, now+1)
	require.NoError(t, err)
	require.True(t, allowed)

	usage, err := GetOpencodeUsageSnapshot(101, now+1)
	require.NoError(t, err)
	require.Equal(t, OpencodeRPMLimit, usage.RPMCount)
}

func TestOpencodeCapacityRejectsFullRPMWindow(t *testing.T) {
	useOpencodeUsageDB(t)
	now := int64(2_000)

	for i := 0; i < OpencodeRPMLimit; i++ {
		allowed, _, err := ReserveOpencodeRPM(202, now)
		require.NoError(t, err)
		require.True(t, allowed)
	}

	capacity, err := OpencodeChannelHasCapacity(202, now)
	require.NoError(t, err)
	require.False(t, capacity)

	require.NoError(t, ReleaseOpencodeRPM(202, now+1))
	capacity, err = OpencodeChannelHasCapacity(202, now+1)
	require.NoError(t, err)
	require.True(t, capacity)
}

func TestOpencodeRPMReservationUsesRuntimeLimit(t *testing.T) {
	useOpencodeUsageDB(t)
	previousLimit := operation_setting.GetOpencodeRPMLimit()
	t.Cleanup(func() {
		require.NoError(t, operation_setting.SetOpencodeRPMLimit(strconv.Itoa(previousLimit)))
	})
	require.NoError(t, operation_setting.SetOpencodeRPMLimit("5"))

	now := int64(2_500)
	for i := 0; i < 5; i++ {
		allowed, _, err := ReserveOpencodeRPM(250, now)
		require.NoError(t, err)
		require.True(t, allowed)
	}
	allowed, _, err := ReserveOpencodeRPM(250, now)
	require.NoError(t, err)
	require.False(t, allowed)

	snapshot, err := GetOpencodeUsageSnapshot(250, now)
	require.NoError(t, err)
	require.Equal(t, 5, snapshot.RPMLimit)
}

func TestOpencodeUsageSnapshotClampsCorruptCounters(t *testing.T) {
	useOpencodeUsageDB(t)
	now := int64(3_000)
	require.NoError(t, DB.Create(&OpencodeChannelUsage{
		ChannelID:      303,
		FiveHourUsed:   -1,
		FiveHourStart:  now,
		WeekUsed:       OpencodeWeekLimitMicroUSD + 1,
		WeekStart:      now,
		MonthUsed:      OpencodeMonthLimitMicroUSD + 1,
		MonthStart:     now,
		RPMWindowStart: now,
		RPMCount:       -4,
	}).Error)
	require.NoError(t, DB.Create(&OpencodePoolUsage{
		PoolKey: OpencodePoolKeyAt(303, time.Unix(now, 0).UTC()),
		Used:    -1,
	}).Error)

	usage, err := GetOpencodeUsageSnapshot(303, now)
	require.NoError(t, err)
	require.Equal(t, int64(0), usage.FiveHour.UsedMicroUSD)
	require.Equal(t, OpencodeWeekLimitMicroUSD, usage.Week.UsedMicroUSD)
	require.Equal(t, OpencodeMonthLimitMicroUSD, usage.Month.UsedMicroUSD)
	require.Equal(t, int64(0), usage.Pool.UsedMicroUSD)
	require.Equal(t, 0, usage.RPMCount)
}
