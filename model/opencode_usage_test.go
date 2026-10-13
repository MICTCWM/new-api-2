package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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
	require.NoError(t, db.AutoMigrate(&Channel{}, &OpencodeChannelUsage{}, &OpencodePoolUsage{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func TestCalculateOpencodeCostMicroUSDUsesFlatPricingAndCache(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     2_000_000,
		CompletionTokens: 1_000_000,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 500_000,
		},
	}

	// Flat DeepSeek rates: 1.5M * 0.22 + 1M * 0.66 + 0.5M * 0.007 = 330_000 + 660_000 + 3_500 = 993_500
	offPeak, err := CalculateOpencodeCostMicroUSD(usage, time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, int64(993_500), offPeak)

	peak, err := CalculateOpencodeCostMicroUSD(usage, time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, int64(993_500), peak)
}

func TestCalculateOpencodeCostMicroUSDForModelUsesGlmFlatPricing(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     2_000_000,
		CompletionTokens: 1_000_000,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 500_000,
		},
	}

	// GLM-5.3-Flash: $0.15 input / $0.50 output / $0.03 cache (flat, no peak).
	// 1.5M uncached * 0.15 + 1M * 0.50 + 0.5M * 0.03 = 225_000 + 500_000 + 15_000 = 740_000
	offPeak, err := CalculateOpencodeCostMicroUSDForModel(usage, OpencodeGlmModel, time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, int64(740_000), offPeak)

	peak, err := CalculateOpencodeCostMicroUSDForModel(usage, OpencodeGlmModel, time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, int64(740_000), peak)
}

func TestCalculateOpencodeCostMicroUSDForModelAppliesPeakPricing(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     1_000_000,
		CompletionTokens: 1_000_000,
	}
	peak := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)     // Wednesday 01:00 UTC
	offPeak := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC) // Saturday: all day off-peak
	require.True(t, IsOpencodePeak(peak))
	require.False(t, IsOpencodePeak(offPeak))

	for _, test := range []struct {
		model   string
		peak    int64
		offPeak int64
	}{
		{model: OpencodeV41FlashModel, peak: 1_500_000, offPeak: 750_000},
		{model: OpencodeProModel, peak: 5_280_000, offPeak: 2_640_000},
	} {
		peakCost, err := CalculateOpencodeCostMicroUSDForModel(usage, test.model, peak)
		require.NoError(t, err)
		require.Equal(t, test.peak, peakCost, test.model)

		offPeakCost, err := CalculateOpencodeCostMicroUSDForModel(usage, test.model, offPeak)
		require.NoError(t, err)
		require.Equal(t, test.offPeak, offPeakCost, test.model)
	}
}

func TestSettleOpencodeCostForModelRoutesToModelPools(t *testing.T) {
	useOpencodeUsageDB(t)
	now := int64(6_000)
	channel := &Channel{
		Type:   constant.ChannelTypeOpencode,
		Status: common.ChannelStatusEnabled,
		Key:    "opencode-key",
		Models: OpencodeModel + "," + OpencodeGlmModel + "," + OpencodeV41FlashModel + "," + OpencodeProModel,
	}
	require.NoError(t, DB.Create(channel).Error)

	require.NoError(t, SettleOpencodeCostForModel(channel.Id, OpencodeModel, 1_000_000, time.Unix(now, 0).UTC()))
	require.NoError(t, SettleOpencodeCostForModel(channel.Id, OpencodeGlmModel, 2_000_000, time.Unix(now, 0).UTC()))
	require.NoError(t, SettleOpencodeCostForModel(channel.Id, OpencodeV41FlashModel, 3_000_000, time.Unix(now, 0).UTC()))
	require.NoError(t, SettleOpencodeCostForModel(channel.Id, OpencodeProModel, 500_000, time.Unix(now, 0).UTC()))

	snapshot, err := GetOpencodeUsageSnapshot(channel.Id, now)
	require.NoError(t, err)
	require.Equal(t, int64(1_000_000), snapshot.FlashPool.UsedMicroUSD)
	require.Equal(t, int64(2_000_000), snapshot.GlmPool.UsedMicroUSD)
	require.Equal(t, int64(3_000_000), snapshot.V41Pool.UsedMicroUSD)
	require.Equal(t, int64(500_000), snapshot.ProPool.UsedMicroUSD)
	// 四个启用池之和服务总池展示值，互不串池。
	require.Equal(t, int64(6_500_000), snapshot.Pool.UsedMicroUSD)
	require.Equal(t, int64(6_500_000), snapshot.Month.UsedMicroUSD)
	require.True(t, snapshot.SplitPools)
}

// 渠道模型映射把源模型解析到 v4.1-flash / v4-pro 时，结算必须落入对应的
// v41 / pro 池，且计价按解析后的原生模型峰谷价计算。
func TestSettleOpencodeCostForModelRoutesMappedSourceToModelPool(t *testing.T) {
	useOpencodeUsageDB(t)
	now := int64(7_000)
	mapping := `{"source-v41":"deepseek-v4.1-flash","source-pro":"deepseek-v4-pro"}`
	channel := &Channel{
		Type:         constant.ChannelTypeOpencode,
		Status:       common.ChannelStatusEnabled,
		Key:          "opencode-key",
		Models:       OpencodeModel + "," + OpencodeGlmModel + "," + OpencodeV41FlashModel + "," + OpencodeProModel,
		ModelMapping: &mapping,
	}
	require.NoError(t, DB.Create(channel).Error)

	require.Equal(t, OpencodeV41FlashModel, ResolveOpencodeModel(channel, "source-v41"))
	require.Equal(t, OpencodeProModel, ResolveOpencodeModel(channel, "source-pro"))

	require.NoError(t, SettleOpencodeCostForModel(channel.Id, "source-v41", 3_000_000, time.Unix(now, 0).UTC()))
	require.NoError(t, SettleOpencodeCostForModel(channel.Id, "source-pro", 4_000_000, time.Unix(now, 0).UTC()))

	snapshot, err := GetOpencodeUsageSnapshot(channel.Id, now)
	require.NoError(t, err)
	require.Equal(t, int64(0), snapshot.FlashPool.UsedMicroUSD)
	require.Equal(t, int64(3_000_000), snapshot.V41Pool.UsedMicroUSD)
	require.Equal(t, int64(4_000_000), snapshot.ProPool.UsedMicroUSD)
	require.Equal(t, int64(7_000_000), snapshot.Pool.UsedMicroUSD)

	// 解析后的原生模型决定峰谷价：以映射源结算的请求应复用该模型的费率。
	usage := &dto.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	peak := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	offPeak := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	v41Peak, err := CalculateOpencodeCostMicroUSDForModel(usage, ResolveOpencodeModel(channel, "source-v41"), peak)
	require.NoError(t, err)
	require.Equal(t, int64(1_500_000), v41Peak)
	v41OffPeak, err := CalculateOpencodeCostMicroUSDForModel(usage, ResolveOpencodeModel(channel, "source-v41"), offPeak)
	require.NoError(t, err)
	require.Equal(t, int64(750_000), v41OffPeak)
	proPeak, err := CalculateOpencodeCostMicroUSDForModel(usage, ResolveOpencodeModel(channel, "source-pro"), peak)
	require.NoError(t, err)
	require.Equal(t, int64(5_280_000), proPeak)
}

func TestResolveOpencodeModelFollowsMappingChainAndStopsOnCycle(t *testing.T) {
	require.Equal(t, OpencodeV41FlashModel, ResolveOpencodeModel(nil, OpencodeV41FlashModel))
	require.Equal(t, OpencodeModel, ResolveOpencodeModel(nil, "  "))

	chain := `{"a":"b","b":"deepseek-v4-pro"}`
	chainChannel := &Channel{Type: constant.ChannelTypeOpencode, ModelMapping: &chain}
	require.Equal(t, OpencodeProModel, ResolveOpencodeModel(chainChannel, "a"))
	require.Equal(t, OpencodeProModel, ResolveOpencodeModel(chainChannel, "b"))
	// 未命中映射的模型原样返回，由调用方按 Flash 兜底计价。
	require.Equal(t, "unknown-target", ResolveOpencodeModel(chainChannel, "unknown-target"))

	// 环路必须终止而非死循环：x -> y -> x 停在重复节点。
	cycle := `{"x":"y","y":"x"}`
	cycleChannel := &Channel{Type: constant.ChannelTypeOpencode, ModelMapping: &cycle}
	require.Equal(t, "y", ResolveOpencodeModel(cycleChannel, "x"))
}

func TestGetCodeProviderCandidatesIncludesModelMappingSource(t *testing.T) {
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &OpencodeChannelUsage{}, &OpencodePoolUsage{}, &CommandChannelUsage{}, &CommandPoolUsage{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})

	mapping := `{"client-model":"deepseek/deepseek-v4-flash"}`
	channel := &Channel{
		Type:         constant.ChannelTypeCommand,
		Status:       common.ChannelStatusEnabled,
		Key:          "command-key",
		Models:       CommandModel + ",client-model",
		ModelMapping: &mapping,
		Group:        "default",
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&Ability{
		Group:     "default",
		Model:     "client-model",
		ChannelId: channel.Id,
		Enabled:   true,
	}).Error)

	openCodeChannel := &Channel{
		Type:   constant.ChannelTypeOpencode,
		Status: common.ChannelStatusEnabled,
		Key:    "opencode-key",
		Models: OpencodeModel,
		Group:  "default",
	}
	require.NoError(t, db.Create(openCodeChannel).Error)
	require.NoError(t, db.Create(&Ability{
		Group:     "default",
		Model:     "client-model",
		ChannelId: openCodeChannel.Id,
		Enabled:   true,
	}).Error)

	candidates, err := GetCodeProviderCandidates("default", "client-model", nil)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, channel.Id, candidates[0].Id)
}

func TestGetCodeProviderCandidatesIncludesOpencodeModelMappingSource(t *testing.T) {
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &OpencodeChannelUsage{}, &OpencodePoolUsage{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})

	mapping := `{"client-model":"deepseek-v4-flash"}`
	channel := &Channel{
		Type:         constant.ChannelTypeOpencode,
		Status:       common.ChannelStatusEnabled,
		Key:          "opencode-key",
		Models:       OpencodeModel + ",client-model",
		ModelMapping: &mapping,
		Group:        "default",
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&Ability{
		Group:     "default",
		Model:     "client-model",
		ChannelId: channel.Id,
		Enabled:   true,
	}).Error)

	require.True(t, IsCodeProviderModel("client-model"))
	candidates, err := GetCodeProviderCandidates("default", "client-model", nil)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, channel.Id, candidates[0].Id)
}

func TestGetCodeProviderCandidatesSkipsExhaustedCommandChannelAndKeepsGroup(t *testing.T) {
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &OpencodeChannelUsage{}, &OpencodePoolUsage{}, &CommandChannelUsage{}, &CommandPoolUsage{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})

	exhausted := &Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "exhausted-key",
		Models: CommandFlashModel,
		Group:  "default",
	}
	available := &Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "available-key",
		Models: CommandFlashModel,
		Group:  "default",
	}
	otherGroup := &Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "other-group-key",
		Models: CommandFlashModel,
		Group:  "other",
	}
	for _, channel := range []*Channel{exhausted, available, otherGroup} {
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&Ability{Group: channel.Group, Model: CommandFlashModel, ChannelId: channel.Id, Enabled: true}).Error)
	}

	require.NoError(t, SetCommandUsageOverride(exhausted.Id, CodeProviderUsageOverride{
		FiveHourUsedMicroUSD: 0,
		WeekUsedMicroUSD:     0,
		MonthUsedMicroUSD:    0,
		PoolUsedMicroUSD:     CommandCombinedPoolLimitMicroUSD,
	}, 4_000))

	candidates, err := GetCodeProviderCandidates("default", CommandFlashModel, nil)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, available.Id, candidates[0].Id)

	candidates, err = GetCodeProviderCandidates("missing", CommandFlashModel, nil)
	require.NoError(t, err)
	require.Empty(t, candidates)
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
	require.Equal(t, int64(0), snapshot.PeakPool.UsedMicroUSD)
	require.Equal(t, int64(0), snapshot.OffPeakPool.UsedMicroUSD)
	require.Equal(t, OpencodePricingPeriodAt(now), snapshot.PricingPeriod)
}

func TestOpencodeUsesCombinedPoolAcrossFormerPeakWindows(t *testing.T) {
	useOpencodeUsageDB(t)
	peakNow := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	offPeakNow := time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC)

	require.NoError(t, SettleOpencodeCost(406, 10_000_000, peakNow))
	require.NoError(t, SettleOpencodeCost(406, 5_000_000, offPeakNow))
	snapshot, err := GetOpencodeUsageSnapshot(406, peakNow.Unix())
	require.NoError(t, err)
	require.Equal(t, int64(15_000_000), snapshot.Pool.UsedMicroUSD)

	otherChannelSnapshot, err := GetOpencodeUsageSnapshot(407, peakNow.Unix())
	require.NoError(t, err)
	require.Equal(t, int64(0), otherChannelSnapshot.Pool.UsedMicroUSD)
	peakAvailable, err := OpencodeChannelHasQuotaCapacity(407, peakNow.Unix())
	require.NoError(t, err)
	require.True(t, peakAvailable)

	require.NoError(t, SettleOpencodeCost(407, OpencodePoolLimitMicroUSD, offPeakNow))
	offPeakSnapshot, err := GetOpencodeUsageSnapshot(407, offPeakNow.Unix())
	require.NoError(t, err)
	require.Equal(t, OpencodePoolLimitMicroUSD, offPeakSnapshot.Pool.UsedMicroUSD)
	available, err := OpencodeChannelHasQuotaCapacity(407, offPeakNow.Unix())
	require.NoError(t, err)
	require.False(t, available)
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
		require.Equal(t, int64(0), usage.Pool.UsedMicroUSD)
		require.Equal(t, 0, usage.RPMCount)
		require.Equal(t, 0, usage.ConcurrencyCount)
	}
}

func TestSettleOpencodeCostRejectsNegativeCharge(t *testing.T) {
	useOpencodeUsageDB(t)
	require.Error(t, SettleOpencodeCost(405, -1, time.Now().UTC()))
}

func TestSetOpencodeUsageOverrideUpdatesModelPoolAndPreservesRPM(t *testing.T) {
	useOpencodeUsageDB(t)
	now := time.Date(2026, time.August, 19, 1, 0, 0, 0, time.UTC)
	channel := &Channel{Type: constant.ChannelTypeOpencode, Status: common.ChannelStatusEnabled, Key: "opencode-key", Models: OpencodeModel}
	require.NoError(t, DB.Create(channel).Error)

	allowed, _, err := ReserveOpencodeRPM(channel.Id, now.Unix())
	require.NoError(t, err)
	require.True(t, allowed)

	flashPool := int64(18_000_000)
	require.NoError(t, SetOpencodeUsageOverride(channel.Id, CodeProviderUsageOverride{
		FiveHourUsedMicroUSD:  2_000_000,
		WeekUsedMicroUSD:      8_000_000,
		MonthUsedMicroUSD:     15_000_000,
		PoolUsedMicroUSD:      18_000_000,
		FlashPoolUsedMicroUSD: &flashPool,
	}, now.Unix()))

	snapshot, err := GetOpencodeUsageSnapshot(channel.Id, now.Unix())
	require.NoError(t, err)
	require.Equal(t, int64(2_000_000), snapshot.FiveHour.UsedMicroUSD)
	require.Equal(t, int64(8_000_000), snapshot.Week.UsedMicroUSD)
	require.Equal(t, int64(15_000_000), snapshot.Month.UsedMicroUSD)
	require.Equal(t, int64(18_000_000), snapshot.Pool.UsedMicroUSD)
	require.Equal(t, int64(18_000_000), snapshot.FlashPool.UsedMicroUSD)
	require.True(t, snapshot.FlashPoolActive)
	require.False(t, snapshot.GlmPoolActive)
	require.Equal(t, 1, snapshot.RPMCount)
}

func TestSetOpencodeUsageOverrideRequiresServedPoolsAndConsistentSum(t *testing.T) {
	useOpencodeUsageDB(t)
	now := int64(5_000)
	channel := &Channel{
		Type:   constant.ChannelTypeOpencode,
		Status: common.ChannelStatusEnabled,
		Key:    "opencode-key",
		Models: OpencodeModel + "," + OpencodeGlmModel + "," + OpencodeV41FlashModel + "," + OpencodeProModel,
	}
	require.NoError(t, DB.Create(channel).Error)

	flash, glm, v41, pro := int64(1_000_000), int64(2_000_000), int64(3_000_000), int64(4_000_000)
	require.NoError(t, SetOpencodeUsageOverride(channel.Id, CodeProviderUsageOverride{
		FiveHourUsedMicroUSD:  0,
		WeekUsedMicroUSD:      0,
		MonthUsedMicroUSD:     0,
		PoolUsedMicroUSD:      flash + glm + v41 + pro,
		FlashPoolUsedMicroUSD: &flash,
		GlmPoolUsedMicroUSD:   &glm,
		V41PoolUsedMicroUSD:   &v41,
		ProPoolUsedMicroUSD:   &pro,
	}, now))

	snapshot, err := GetOpencodeUsageSnapshot(channel.Id, now)
	require.NoError(t, err)
	require.Equal(t, flash, snapshot.FlashPool.UsedMicroUSD)
	require.Equal(t, glm, snapshot.GlmPool.UsedMicroUSD)
	require.Equal(t, v41, snapshot.V41Pool.UsedMicroUSD)
	require.Equal(t, pro, snapshot.ProPool.UsedMicroUSD)
	require.Equal(t, flash+glm+v41+pro, snapshot.Pool.UsedMicroUSD)
	require.True(t, snapshot.SplitPools)
	require.True(t, snapshot.FlashPoolActive)
	require.True(t, snapshot.GlmPoolActive)
	require.True(t, snapshot.V41PoolActive)
	require.True(t, snapshot.ProPoolActive)

	// 缺少启用池（glm）时拒绝。
	require.ErrorContains(t, SetOpencodeUsageOverride(channel.Id, CodeProviderUsageOverride{
		PoolUsedMicroUSD:      flash,
		FlashPoolUsedMicroUSD: &flash,
	}, now), "glm pool usage is required")

	// 启用池总和必须等于 pool_used_usd。
	require.ErrorContains(t, SetOpencodeUsageOverride(channel.Id, CodeProviderUsageOverride{
		PoolUsedMicroUSD:      flash + glm + v41 + pro + 1,
		FlashPoolUsedMicroUSD: &flash,
		GlmPoolUsedMicroUSD:   &glm,
		V41PoolUsedMicroUSD:   &v41,
		ProPoolUsedMicroUSD:   &pro,
	}, now), "must equal the total pool usage")
}

// 未启用的模型池不允许传值：仅启用 flash 的渠道带上 glm/v41 池用量必须被拒绝。
func TestSetOpencodeUsageOverrideRejectsUnservedPoolValue(t *testing.T) {
	useOpencodeUsageDB(t)
	now := int64(5_100)
	channel := &Channel{Type: constant.ChannelTypeOpencode, Status: common.ChannelStatusEnabled, Key: "opencode-key", Models: OpencodeModel}
	require.NoError(t, DB.Create(channel).Error)

	flash, glm, v41 := int64(1_000_000), int64(2_000_000), int64(3_000_000)
	require.ErrorContains(t, SetOpencodeUsageOverride(channel.Id, CodeProviderUsageOverride{
		PoolUsedMicroUSD:      flash + glm,
		FlashPoolUsedMicroUSD: &flash,
		GlmPoolUsedMicroUSD:   &glm,
	}, now), "opencode glm pool usage is not allowed")

	require.ErrorContains(t, SetOpencodeUsageOverride(channel.Id, CodeProviderUsageOverride{
		PoolUsedMicroUSD:      flash + v41,
		FlashPoolUsedMicroUSD: &flash,
		V41PoolUsedMicroUSD:   &v41,
	}, now), "opencode v4.1 pool usage is not allowed")
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

func TestOpencodeCapacityRejectsFullConcurrency(t *testing.T) {
	useOpencodeUsageDB(t)
	previousLimit := operation_setting.GetOpencodeConcurrencyLimit()
	t.Cleanup(func() {
		require.NoError(t, operation_setting.SetOpencodeConcurrencyLimit(strconv.Itoa(previousLimit)))
	})
	require.NoError(t, operation_setting.SetOpencodeConcurrencyLimit(strconv.Itoa(OpencodeRPMLimit)))

	now := int64(2_000)
	for i := 0; i < OpencodeRPMLimit; i++ {
		allowed, err := ReserveOpencodeConcurrency(202)
		require.NoError(t, err)
		require.True(t, allowed)
	}

	capacity, err := OpencodeChannelHasCapacity(202, now)
	require.NoError(t, err)
	require.False(t, capacity)

	require.NoError(t, ReleaseOpencodeConcurrency(202))
	capacity, err = OpencodeChannelHasCapacity(202, now+1)
	require.NoError(t, err)
	require.True(t, capacity)
}

func TestOpencodeRPMDoesNotAffectConcurrencyCount(t *testing.T) {
	useOpencodeUsageDB(t)
	now := int64(2_100)

	allowed, _, err := ReserveOpencodeRPM(203, now)
	require.NoError(t, err)
	require.True(t, allowed)

	snapshot, err := GetOpencodeUsageSnapshot(203, now)
	require.NoError(t, err)
	require.Equal(t, 1, snapshot.RPMCount)
	require.Equal(t, 0, snapshot.ConcurrencyCount)

	require.NoError(t, ReleaseOpencodeRPM(203, now+120))
	snapshot, err = GetOpencodeUsageSnapshot(203, now+120)
	require.NoError(t, err)
	require.Equal(t, 0, snapshot.RPMCount)
	require.Equal(t, 0, snapshot.ConcurrencyCount)
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
	require.Equal(t, 5, snapshot.RPMCount)
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

func TestOpencodeConcurrencyLimitUsesActiveRequestsAndExplicitRelease(t *testing.T) {
	useOpencodeUsageDB(t)
	previousLimit := operation_setting.GetOpencodeConcurrencyLimit()
	t.Cleanup(func() {
		require.NoError(t, operation_setting.SetOpencodeConcurrencyLimit(strconv.Itoa(previousLimit)))
	})
	require.NoError(t, operation_setting.SetOpencodeConcurrencyLimit("2"))

	allowed, err := ReserveOpencodeConcurrency(701)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = ReserveOpencodeConcurrency(701)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = ReserveOpencodeConcurrency(701)
	require.NoError(t, err)
	require.False(t, allowed)

	snapshot, err := GetOpencodeUsageSnapshot(701, time.Now().Unix()+120)
	require.NoError(t, err)
	require.Equal(t, 2, snapshot.ConcurrencyCount)
	require.Equal(t, 2, snapshot.ConcurrencyLimit)

	require.NoError(t, ReleaseOpencodeConcurrency(701))
	allowed, err = ReserveOpencodeConcurrency(701)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, ReleaseOpencodeConcurrency(701))
	require.NoError(t, ReleaseOpencodeConcurrency(701))

	snapshot, err = GetOpencodeUsageSnapshot(701, time.Now().Unix())
	require.NoError(t, err)
	require.Zero(t, snapshot.ConcurrencyCount)
}
