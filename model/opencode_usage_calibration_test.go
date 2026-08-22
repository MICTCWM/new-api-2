package model

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useOpencodeCalibrationDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &OpencodeChannelUsage{}, &OpencodePoolUsage{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func TestFetchOpencodeRemoteUsageParsesOfficialResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
		require.Equal(t, "application/json", request.Header.Get("Accept"))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
          "usage": {
            "rolling": {"status": "ok", "percent": 24, "resetsAt": "2026-08-22T09:58:42.423Z"},
            "weekly": {"status": "ok", "percent": 9, "resetsAt": "2026-08-24T00:00:00.423Z"},
            "monthly": {"status": "ok", "percent": 4, "resetsAt": "2026-09-22T04:25:57.423Z"}
          }
        }`))
	}))
	defer server.Close()

	previousEndpoint := opencodeUsageEndpoint
	opencodeUsageEndpoint = server.URL
	t.Cleanup(func() { opencodeUsageEndpoint = previousEndpoint })

	usage, err := fetchOpencodeRemoteUsage(t.Context(), "test-key")
	require.NoError(t, err)
	require.Equal(t, float64(24), usage.RollingPercent)
	require.Equal(t, float64(9), usage.WeeklyPercent)
	require.Equal(t, float64(4), usage.MonthlyPercent)
	require.Equal(t, int64(1787392722), usage.RollingResetAt)
}

func TestOpencodeRemoteUsedMicroUSDConvertsPercentToBoundedAmount(t *testing.T) {
	require.Equal(t, int64(0), opencodeRemoteUsedMicroUSD(0, OpencodeFiveHourLimitMicroUSD))
	require.Equal(t, int64(2_880_000), opencodeRemoteUsedMicroUSD(24, OpencodeFiveHourLimitMicroUSD))
	require.Equal(t, OpencodeFiveHourLimitMicroUSD, opencodeRemoteUsedMicroUSD(100, OpencodeFiveHourLimitMicroUSD))
	require.Equal(t, OpencodeFiveHourLimitMicroUSD, opencodeRemoteUsedMicroUSD(120, OpencodeFiveHourLimitMicroUSD))
}

func TestCalibrateOpencodeChannelReplacesWindowsAndPreservesPricingPools(t *testing.T) {
	useOpencodeCalibrationDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"usage":{"rolling":{"percent":24,"resetsAt":"2026-08-22T09:58:42Z"},"weekly":{"percent":9,"resetsAt":"2026-08-24T00:00:00Z"},"monthly":{"percent":4,"resetsAt":"2026-09-22T04:25:57Z"}}}`))
	}))
	defer server.Close()
	previousEndpoint := opencodeUsageEndpoint
	opencodeUsageEndpoint = server.URL
	t.Cleanup(func() { opencodeUsageEndpoint = previousEndpoint })

	channel := &Channel{
		Type:   constant.ChannelTypeOpencode,
		Status: common.ChannelStatusEnabled,
		Key:    "test-key",
		Models: OpencodeModel,
	}
	require.NoError(t, DB.Create(channel).Error)
	now := time.Date(2026, time.August, 22, 5, 0, 0, 0, time.UTC).Unix()
	poolKey := OpencodePoolKeyAt(channel.Id, time.Unix(now, 0).UTC())
	require.NoError(t, DB.Create(&OpencodePoolUsage{PoolKey: poolKey, Used: 321, ResetTime: now}).Error)

	snapshot, err := CalibrateOpencodeChannel(t.Context(), channel.Id, now)
	require.NoError(t, err)
	require.Equal(t, int64(2_880_000), snapshot.FiveHour.UsedMicroUSD)
	require.Equal(t, int64(2_700_000), snapshot.Week.UsedMicroUSD)
	require.Equal(t, int64(2_400_000), snapshot.Month.UsedMicroUSD)
	require.Equal(t, int64(321), snapshot.Pool.UsedMicroUSD)
	require.Equal(t, now, snapshot.Calibration.LastSuccessAt)
	require.Greater(t, snapshot.Calibration.NextAt, now)
	require.Equal(t, int64(1787392722), snapshot.FiveHour.ResetAt)
}

func TestFailedOpencodeCalibrationKeepsLocalUsage(t *testing.T) {
	useOpencodeCalibrationDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	previousEndpoint := opencodeUsageEndpoint
	opencodeUsageEndpoint = server.URL
	t.Cleanup(func() { opencodeUsageEndpoint = previousEndpoint })

	channel := &Channel{Type: constant.ChannelTypeOpencode, Status: common.ChannelStatusEnabled, Key: "test-key", Models: OpencodeModel}
	require.NoError(t, DB.Create(channel).Error)
	now := int64(1_800_000_000)
	require.NoError(t, DB.Create(&OpencodeChannelUsage{
		ChannelID:         channel.Id,
		FiveHourUsed:      123,
		FiveHourStart:     now,
		WeekUsed:          456,
		WeekStart:         now,
		MonthUsed:         789,
		MonthStart:        now,
		NextCalibrationAt: now,
	}).Error)

	_, err := CalibrateOpencodeChannel(t.Context(), channel.Id, now)
	require.Error(t, err)
	snapshot, snapshotErr := GetOpencodeUsageSnapshot(channel.Id, now)
	require.NoError(t, snapshotErr)
	require.Equal(t, int64(123), snapshot.FiveHour.UsedMicroUSD)
	require.Equal(t, int64(456), snapshot.Week.UsedMicroUSD)
	require.Equal(t, int64(789), snapshot.Month.UsedMicroUSD)
	require.Equal(t, now, snapshot.Calibration.LastAttemptAt)
	require.NotEmpty(t, snapshot.Calibration.LastError)
}

func TestOpencodeCalibrationSchedulesChannelsOnDifferentSlots(t *testing.T) {
	now := int64(1_800_000_000)
	first := nextOpencodeUsageCalibrationAt(1, now)
	second := nextOpencodeUsageCalibrationAt(2, now)
	require.Greater(t, first, now)
	require.Greater(t, second, now)
	require.NotEqual(t, first, second)
	require.LessOrEqual(t, second-first, int64(OpencodeUsageCalibrationInterval/time.Second))
}
