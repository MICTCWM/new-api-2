package model

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

	usage, err := fetchOpencodeRemoteUsage(t.Context(), "test-key", nil)
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

	snapshot, err := CalibrateOpencodeChannel(t.Context(), channel.Id, now, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2_880_000), snapshot.FiveHour.UsedMicroUSD)
	require.Equal(t, int64(2_700_000), snapshot.Week.UsedMicroUSD)
	require.Equal(t, int64(2_400_000), snapshot.Month.UsedMicroUSD)
	// 遗留合并池用量（321）先迁入 flash 池，再按 r = 月权威值(2.4M) / Σ池(321) 缩放。
	require.Equal(t, int64(2_400_000), snapshot.FlashPool.UsedMicroUSD)
	require.Equal(t, int64(2_400_000), snapshot.Pool.UsedMicroUSD)
	require.Equal(t, now, snapshot.Calibration.LastSuccessAt)
	require.Greater(t, snapshot.Calibration.NextAt, now)
	require.Equal(t, int64(1787392722), snapshot.FiveHour.ResetAt)
}

func TestApplyOpencodePoolCalibrationScalesAndClampsServedPools(t *testing.T) {
	useOpencodeCalibrationDB(t)
	now := int64(7_000)
	channel := &Channel{
		Type:   constant.ChannelTypeOpencode,
		Status: common.ChannelStatusEnabled,
		Key:    "test-key",
		Models: OpencodeModel + "," + OpencodeV41FlashModel + "," + OpencodeProModel,
	}
	require.NoError(t, DB.Create(channel).Error)
	for key, used := range map[string]int64{
		OpencodeFlashPoolKey(channel.Id): 10_000_000,
		OpencodeV41PoolKey(channel.Id):   5_000_000,
		OpencodeProPoolKey(channel.Id):   10_000_000,
	} {
		require.NoError(t, DB.Create(&OpencodePoolUsage{PoolKey: key, Used: used, ResetTime: now}).Error)
	}

	// 月权威值 60M（100%），oldMonth 20M → 非滚动；ratio = 60M / 25M = 2.4。
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return applyOpencodePoolCalibration(tx, channel.Id, 60_000_000, now, 20_000_000)
	}))

	flash, _, err := findOpencodePoolUsageTx(DB, OpencodeFlashPoolKey(channel.Id), now)
	require.NoError(t, err)
	v41, _, err := findOpencodePoolUsageTx(DB, OpencodeV41PoolKey(channel.Id), now)
	require.NoError(t, err)
	pro, _, err := findOpencodePoolUsageTx(DB, OpencodeProPoolKey(channel.Id), now)
	require.NoError(t, err)
	require.Equal(t, int64(24_000_000), flash.Used)
	require.Equal(t, int64(12_000_000), v41.Used)
	// 24M 超过 pro 池限额 15M，clamp 到限额。
	require.Equal(t, int64(15_000_000), pro.Used)
}

func TestApplyOpencodePoolCalibrationClearsPoolsOnRolloverAndReset(t *testing.T) {
	useOpencodeCalibrationDB(t)
	now := int64(8_000)
	channel := &Channel{Type: constant.ChannelTypeOpencode, Status: common.ChannelStatusEnabled, Key: "test-key", Models: OpencodeModel}
	require.NoError(t, DB.Create(channel).Error)
	flashKey := OpencodeFlashPoolKey(channel.Id)
	require.NoError(t, DB.Create(&OpencodePoolUsage{PoolKey: flashKey, Used: 40_000_000, ResetTime: now}).Error)

	// 月滚动：oldMonth 40M，远端月值 3M（< 20M）→ 启用池清零。
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return applyOpencodePoolCalibration(tx, channel.Id, 3_000_000, now, 40_000_000)
	}))
	flash, _, err := findOpencodePoolUsageTx(DB, flashKey, now)
	require.NoError(t, err)
	require.Zero(t, flash.Used)

	// 上游清零：远端月值为 0 → 启用池清零。
	require.NoError(t, DB.Model(&OpencodePoolUsage{}).Where("pool_key = ?", flashKey).Update("used", 40_000_000).Error)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return applyOpencodePoolCalibration(tx, channel.Id, 0, now, 40_000_000)
	}))
	flash, _, err = findOpencodePoolUsageTx(DB, flashKey, now)
	require.NoError(t, err)
	require.Zero(t, flash.Used)
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

	_, err := CalibrateOpencodeChannel(t.Context(), channel.Id, now, nil)
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

// stubOpencodeTransport 记录请求并返回固定用量响应，用于验证校准请求
// 确实走注入的客户端（即代理工厂产物）。
type stubOpencodeTransport struct {
	requests int
	body     string
}

func (s *stubOpencodeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	s.requests++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Request:    request,
	}, nil
}

func TestCalibrateOpencodeChannelUsesInjectedClient(t *testing.T) {
	useOpencodeCalibrationDB(t)
	transport := &stubOpencodeTransport{body: `{"usage":{"rolling":{"percent":10,"resetsAt":"2026-08-22T09:58:42Z"},"weekly":{"percent":5,"resetsAt":"2026-08-24T00:00:00Z"},"monthly":{"percent":2,"resetsAt":"2026-09-22T04:25:57Z"}}}`}

	channel := &Channel{Type: constant.ChannelTypeOpencode, Status: common.ChannelStatusEnabled, Key: "test-key", Models: OpencodeModel}
	require.NoError(t, DB.Create(channel).Error)
	now := time.Date(2026, time.August, 22, 5, 0, 0, 0, time.UTC).Unix()

	factory := OpencodeCalibrationClientFactory(func(channel *Channel) (*http.Client, error) {
		return &http.Client{Transport: transport, Timeout: 15 * time.Second}, nil
	})
	snapshot, err := CalibrateOpencodeChannel(t.Context(), channel.Id, now, factory)
	require.NoError(t, err)
	require.Equal(t, 1, transport.requests, "校准请求必须经由注入的客户端发出")
	require.Equal(t, int64(1_200_000), snapshot.FiveHour.UsedMicroUSD)
}

func TestCalibrateOpencodeChannelRecordsClientFactoryFailure(t *testing.T) {
	useOpencodeCalibrationDB(t)
	channel := &Channel{Type: constant.ChannelTypeOpencode, Status: common.ChannelStatusEnabled, Key: "test-key", Models: OpencodeModel}
	require.NoError(t, DB.Create(channel).Error)
	now := int64(1_800_000_000)

	factory := OpencodeCalibrationClientFactory(func(channel *Channel) (*http.Client, error) {
		return nil, errors.New("bad proxy config")
	})
	_, err := CalibrateOpencodeChannel(t.Context(), channel.Id, now, factory)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bad proxy config")

	snapshot, snapshotErr := GetOpencodeUsageSnapshot(channel.Id, now)
	require.NoError(t, snapshotErr)
	require.Contains(t, snapshot.Calibration.LastError, "bad proxy config")
}
