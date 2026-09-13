package service

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useChannelAffinityRoutingDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousMemoryCache := common.MemoryCacheEnabled
	channelAffinityProtectionMu.Lock()
	previousProtection := channelAffinityProtections
	channelAffinityProtections = make(map[string]channelAffinityProtectionLease)
	channelAffinityProtectionMu.Unlock()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.Ability{},
		&model.OpencodeChannelUsage{},
		&model.OpencodePoolUsage{},
		&model.CommandChannelUsage{},
		&model.CommandPoolUsage{},
		&model.SpeedProtectionState{},
	))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	t.Cleanup(func() {
		channelAffinityProtectionMu.Lock()
		channelAffinityProtections = previousProtection
		channelAffinityProtectionMu.Unlock()
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled = previousMemoryCache
		if previousMemoryCache && previousDB != nil {
			model.InitChannelCache()
		}
	})
	return db
}

func newCommandRoutingContext() *gin.Context {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	return ctx
}

func TestCodeProviderAffinityPrefersAvailableChannel(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	preferred := &model.Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "preferred",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	fallback := &model.Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "fallback",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	for _, channel := range []*model.Channel{preferred, fallback} {
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group:     "default",
			Model:     model.CommandFlashModel,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}
	model.InitChannelCache()

	ctx := newCommandRoutingContext()
	setChannelAffinityContext(ctx, channelAffinityMeta{
		CacheKey:   channelAffinityCacheNamespace + ":routing-hit",
		TTLSeconds: 60,
		RuleName:   "routing-test",
		ModelName:  model.CommandFlashModel,
	})
	MarkChannelAffinityUsed(ctx, "default", preferred.Id)

	selected, err := selectAffinityCodeProviderCandidate(&RetryParam{
		Ctx:                 ctx,
		TokenGroup:          "default",
		ModelName:           model.CommandFlashModel,
		RequestPath:         "/v1/chat/completions",
		AttemptedChannelIDs: map[int]struct{}{},
		OpencodePool:        true,
	}, "default")
	require.NoError(t, err)
	require.Equal(t, preferred.Id, selected.Id)
}

func TestCodeProviderAffinitySkipsExhaustedChannel(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	preferred := &model.Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "exhausted-preferred",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	fallback := &model.Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "available-fallback",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	for _, channel := range []*model.Channel{preferred, fallback} {
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group:     "default",
			Model:     model.CommandFlashModel,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}
	model.InitChannelCache()

	now := time.Now().Unix()
	require.NoError(t, model.SetCommandUsageOverride(preferred.Id, model.CodeProviderUsageOverride{
		FiveHourUsedMicroUSD: model.CommandFiveHourLimitMicroUSD,
		WeekUsedMicroUSD:     model.CommandWeekLimitMicroUSD,
		MonthUsedMicroUSD:    model.CommandMonthLimitMicroUSD,
		PoolUsedMicroUSD:     model.CommandCombinedPoolLimitMicroUSD,
	}, now))

	ctx := newCommandRoutingContext()
	setChannelAffinityContext(ctx, channelAffinityMeta{
		CacheKey:   channelAffinityCacheNamespace + ":routing-exhausted",
		TTLSeconds: 60,
		RuleName:   "routing-test",
		ModelName:  model.CommandFlashModel,
	})
	MarkChannelAffinityUsed(ctx, "default", preferred.Id)

	selected, err := selectAffinityCodeProviderCandidate(&RetryParam{
		Ctx:                 ctx,
		TokenGroup:          "default",
		ModelName:           model.CommandFlashModel,
		RequestPath:         "/v1/chat/completions",
		AttemptedChannelIDs: map[int]struct{}{},
		OpencodePool:        true,
	}, "default")
	require.NoError(t, err)
	require.Nil(t, selected)
	require.Equal(t, 0, GetCurrentChannelAffinitySelection(ctx))

}

func TestCodeProviderSelectionFallsBackToOrdinaryChannelWhenPoolIsDisabled(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	previousLogDB := model.LOG_DB
	previousLogType := common.LogDatabaseType()
	require.NoError(t, model.InitLogDB())
	t.Cleanup(func() {
		model.LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogType)
	})
	command := &model.Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusManuallyDisabled,
		Key:    "disabled-command",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	secondCommand := &model.Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusAutoDisabled,
		Key:    "auto-disabled-command",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	ordinary := &model.Channel{
		Type:   constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusEnabled,
		Key:    "ordinary-openai",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	for _, channel := range []*model.Channel{command, secondCommand, ordinary} {
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group:     "default",
			Model:     model.CommandFlashModel,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}
	model.InitChannelCache()

	ctx := newCommandRoutingContext()
	selected, group, err := CacheGetRandomSatisfiedChannelWithCodeProviderFallback(&RetryParam{
		Ctx:                    ctx,
		TokenGroup:             "default",
		ModelName:              model.CommandFlashModel,
		RequestPath:            "/v1/chat/completions",
		Retry:                  common.GetPointer(0),
		OpencodePool:           true,
		OpencodeIncludeRPMFull: true,
		AttemptedChannelIDs:    map[int]struct{}{},
	})
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, ordinary.Id, selected.Id)
	require.Equal(t, "default", group)
}

func TestCodeProviderSelectionDoesNotReturnExhaustedProviderOnOrdinaryFallback(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	command := &model.Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "exhausted-command",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	ordinary := &model.Channel{
		Type:   constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusEnabled,
		Key:    "ordinary-openai",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	for _, channel := range []*model.Channel{command, ordinary} {
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group:     "default",
			Model:     model.CommandFlashModel,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}
	model.InitChannelCache()
	now := time.Now().Unix()
	require.NoError(t, model.SetCommandUsageOverride(command.Id, model.CodeProviderUsageOverride{
		FiveHourUsedMicroUSD: model.CommandFiveHourLimitMicroUSD,
		WeekUsedMicroUSD:     model.CommandWeekLimitMicroUSD,
		MonthUsedMicroUSD:    model.CommandMonthLimitMicroUSD,
		PoolUsedMicroUSD:     model.CommandCombinedPoolLimitMicroUSD,
	}, now))

	selected, group, err := CacheGetRandomSatisfiedChannelWithCodeProviderFallback(&RetryParam{
		Ctx:                    newCommandRoutingContext(),
		TokenGroup:             "default",
		ModelName:              model.CommandFlashModel,
		RequestPath:            "/v1/chat/completions",
		Retry:                  common.GetPointer(0),
		OpencodePool:           true,
		OpencodeIncludeRPMFull: true,
		AttemptedChannelIDs:    make(map[int]struct{}),
	})
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, ordinary.Id, selected.Id)
	require.Equal(t, "default", group)
}

func TestCodeProviderQueueFallsBackToOrdinaryChannelWhenRPMIsFull(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	code := &model.Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "busy-command",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	ordinary := &model.Channel{
		Type:   constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusEnabled,
		Key:    "ordinary-openai",
		Models: model.CommandFlashModel,
		Group:  "default",
	}
	for _, channel := range []*model.Channel{code, ordinary} {
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group:     "default",
			Model:     model.CommandFlashModel,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}
	model.InitChannelCache()

	for i := 0; i < operation_setting.GetOpencodeConcurrencyLimit(); i++ {
		allowed, err := model.ReserveCommandConcurrency(code.Id)
		require.NoError(t, err)
		require.True(t, allowed)
	}

	param := &RetryParam{
		Ctx:                 newCommandRoutingContext(),
		TokenGroup:          "default",
		ModelName:           model.CommandFlashModel,
		RequestPath:         "/v1/chat/completions",
		Retry:               common.GetPointer(0),
		OpencodePool:        true,
		AttemptedChannelIDs: make(map[int]struct{}),
	}
	selected, group, err := WaitForOpencodeChannel(param, 0)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, ordinary.Id, selected.Id)
	require.Equal(t, "default", group)
	require.False(t, param.OpencodePool)
	require.True(t, param.ExcludeCodeProviderChannels)
}

func TestOpenCodeAffinityKeepsTheSameChannelAcrossRequests(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	previousRPMLimit := operation_setting.GetOpencodeRPMLimit()
	require.NoError(t, operation_setting.SetOpencodeRPMLimit("2"))
	t.Cleanup(func() {
		require.NoError(t, operation_setting.SetOpencodeRPMLimit(strconv.Itoa(previousRPMLimit)))
	})

	channels := make([]*model.Channel, 0, 3)
	for index := 0; index < 3; index++ {
		channel := &model.Channel{
			Type:          constant.ChannelTypeOpencode,
			Status:        common.ChannelStatusEnabled,
			Key:           "opencode-affinity-" + strconv.Itoa(index),
			Models:        model.OpencodeModel,
			Group:         "default",
			OtherSettings: "",
		}
		if index == 0 {
			priority := int64(10)
			channel.Priority = &priority
		}
		if index == 1 {
			channel.OtherSettings = `{"speed_protection_enabled":true,"speed_protection_groups":["default"]}`
		}
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group:     "default",
			Model:     model.OpencodeModel,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
		channels = append(channels, channel)
	}
	require.NoError(t, db.Create(&model.SpeedProtectionState{
		ChannelID: channels[1].Id,
		Group:     "default",
		Active:    true,
	}).Error)
	model.InitChannelCache()

	affinityValue := "opencode-session-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	setting := operation_setting.GetChannelAffinitySetting()
	originalRules := setting.Rules
	setting.Rules = []operation_setting.ChannelAffinityRule{{
		Name:              "opencode session test",
		ModelRegex:        []string{"^deepseek-v4-flash$"},
		PathRegex:         []string{"/v1/chat/completions"},
		KeySources:        []operation_setting.ChannelAffinityKeySource{{Type: "gjson", Path: "prompt_cache_key"}},
		TTLSeconds:        60,
		IncludeUsingGroup: true,
		IncludeRuleName:   true,
	}}
	t.Cleanup(func() {
		setting.Rules = originalRules
	})

	newRequest := func() *gin.Context {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
			`{"model":"deepseek-v4-flash","prompt_cache_key":"`+affinityValue+`","stream":true}`,
		))
		ctx.Request.Header.Set("Content-Type", "application/json")
		return ctx
	}

	firstCtx := newRequest()
	preferredID, found := GetPreferredChannelByAffinity(firstCtx, model.OpencodeModel, "default")
	require.False(t, found)
	require.Zero(t, preferredID)
	firstParam := &RetryParam{
		Ctx:                 firstCtx,
		TokenGroup:          "default",
		ModelName:           model.OpencodeModel,
		RequestPath:         "/v1/chat/completions",
		Retry:               common.GetPointer(0),
		OpencodePool:        true,
		AttemptedChannelIDs: make(map[int]struct{}),
	}
	selected, _, err := WaitForOpencodeChannel(firstParam, 0)
	require.NoError(t, err)
	require.NotNil(t, selected)
	firstChannelID := selected.Id
	firstCtx.Set("channel_id", firstChannelID)
	RecordChannelAffinity(firstCtx, firstChannelID)

	rule := setting.Rules[0]
	cacheKeySuffix := buildChannelAffinityCacheKeySuffix(rule, model.OpencodeModel, "default", affinityValue)
	_, found, err = getChannelAffinityCache().Get(cacheKeySuffix)
	require.NoError(t, err)
	require.True(t, found)
	stats := GetChannelAffinityCacheStats()
	require.GreaterOrEqual(t, stats.ByRuleName[rule.Name], 1)

	secondCtx := newRequest()
	preferredID, found = GetPreferredChannelByAffinity(secondCtx, model.OpencodeModel, "default")
	require.True(t, found)
	require.Equal(t, firstChannelID, preferredID)
	ClearCurrentChannelAffinitySelection(secondCtx)
	RecordChannelAffinity(secondCtx, channels[2].Id)
	cachedChannelID, found, err := getChannelAffinityCache().Get(cacheKeySuffix)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, firstChannelID, cachedChannelID)

	preferredID, found = GetPreferredChannelByAffinity(secondCtx, model.OpencodeModel, "default")
	require.True(t, found)
	require.Equal(t, firstChannelID, preferredID)
	MarkChannelAffinityUsed(secondCtx, "default", preferredID)

	secondParam := &RetryParam{
		Ctx:                 secondCtx,
		TokenGroup:          "default",
		ModelName:           model.OpencodeModel,
		RequestPath:         "/v1/chat/completions",
		Retry:               common.GetPointer(0),
		OpencodePool:        true,
		AttemptedChannelIDs: make(map[int]struct{}),
	}
	opencodeQueueMu.Lock()
	queueBeforeFastPath := len(opencodeQueue)
	opencodeQueueMu.Unlock()
	selected, _, fast, err := TryReserveOpencodeAffinityChannel(secondParam)
	require.NoError(t, err)
	require.True(t, fast)
	require.NotNil(t, selected)
	require.Equal(t, firstChannelID, selected.Id)
	require.True(t, secondParam.OpencodeRPMReserved)
	opencodeQueueMu.Lock()
	queueAfterFastPath := len(opencodeQueue)
	opencodeQueueMu.Unlock()
	require.Equal(t, queueBeforeFastPath, queueAfterFastPath)

	thirdCtx := newRequest()
	preferredID, found = GetPreferredChannelByAffinity(thirdCtx, model.OpencodeModel, "default")
	require.True(t, found)
	require.Equal(t, firstChannelID, preferredID)
	MarkChannelAffinityPending(thirdCtx, "default", preferredID)
	thirdParam := &RetryParam{
		Ctx:                 thirdCtx,
		TokenGroup:          "default",
		ModelName:           model.OpencodeModel,
		RequestPath:         "/v1/chat/completions",
		Retry:               common.GetPointer(0),
		OpencodePool:        true,
		AttemptedChannelIDs: make(map[int]struct{}),
	}
	queuedChannel, queuedGroup, err := selectQueuedAffinityCandidate(thirdParam)
	require.NoError(t, err)
	require.NotNil(t, queuedChannel)
	require.Equal(t, firstChannelID, queuedChannel.Id)
	require.Equal(t, "default", queuedGroup)
	reservedChannel, _, err := tryReserveOpencodeChannel(thirdParam)
	require.NoError(t, err)
	require.Nil(t, reservedChannel)
	require.Equal(t, firstChannelID, GetCurrentChannelAffinitySelection(thirdCtx))
	t.Logf("OpenCode affinity kept channel #%d across two requests; cache entry present", firstChannelID)
	require.Contains(t, []int{channels[0].Id, channels[1].Id, channels[2].Id}, firstChannelID)
}

func TestAffinityReservedRPMUsesTheSharedChannelLimit(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	previousRPMLimit := operation_setting.GetOpencodeRPMLimit()
	require.NoError(t, operation_setting.SetOpencodeRPMLimit("5"))
	t.Cleanup(func() {
		require.NoError(t, operation_setting.SetOpencodeRPMLimit(strconv.Itoa(previousRPMLimit)))
	})

	channel := &model.Channel{
		Type:   constant.ChannelTypeOpencode,
		Status: common.ChannelStatusEnabled,
		Key:    "opencode-hidden-rpm",
		Models: model.OpencodeModel,
		Group:  "default",
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     "default",
		Model:     model.OpencodeModel,
		ChannelId: channel.Id,
		Enabled:   true,
	}).Error)
	model.InitChannelCache()

	const firstProtectionKey = "channel-affinity-test-shared-rpm-first"
	const secondProtectionKey = "channel-affinity-test-shared-rpm-second"
	rememberChannelAffinityProtection(firstProtectionKey, channel.Id, 60)
	rememberChannelAffinityProtection(secondProtectionKey, channel.Id, 60)
	t.Cleanup(func() {
		forgetChannelAffinityProtection(firstProtectionKey)
		forgetChannelAffinityProtection(secondProtectionKey)
	})

	now := time.Now().Unix()
	for i := 0; i < 3; i++ {
		allowed, err := model.ReserveOpencodeConcurrency(channel.Id)
		require.NoError(t, err)
		require.True(t, allowed)
	}
	require.Equal(t, 2, GetChannelAffinityReservedRPM(channel.Id, now))
	param := &RetryParam{ModelName: model.OpencodeModel}
	filtered, err := filterCodeProviderCandidatesByAffinityReserve(param, []*model.Channel{channel})
	require.NoError(t, err)
	require.Empty(t, filtered)

	require.NoError(t, model.ReleaseOpencodeConcurrency(channel.Id))
	filtered, err = filterCodeProviderCandidatesByAffinityReserve(param, []*model.Channel{channel})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, 0, GetChannelAffinityReservedRPM(channel.Id, now+operation_setting.ChannelAffinityProtectionLeaseSeconds+1))
}

func TestQueuedAffinityReleasesIneligibleCurrentSelection(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	channel := &model.Channel{
		Type:   constant.ChannelTypeCommand,
		Status: common.ChannelStatusEnabled,
		Key:    "queued-affinity-no-group",
		Models: model.CommandFlashModel,
		Group:  "other",
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     "other",
		Model:     model.CommandFlashModel,
		ChannelId: channel.Id,
		Enabled:   false,
	}).Error)
	model.InitChannelCache()

	setting := operation_setting.GetChannelAffinitySetting()
	originalRules := setting.Rules
	setting.Rules = []operation_setting.ChannelAffinityRule{{
		Name:              "queued affinity eligibility test",
		TTLSeconds:        60,
		IncludeUsingGroup: true,
		IncludeRuleName:   true,
	}}
	t.Cleanup(func() { setting.Rules = originalRules })

	ctx := newCommandRoutingContext()
	setChannelAffinityContext(ctx, channelAffinityMeta{
		CacheKey:   channelAffinityCacheNamespace + ":queued-affinity-no-group",
		TTLSeconds: 60,
		RuleName:   "queued affinity eligibility test",
		ModelName:  model.CommandFlashModel,
	})
	MarkChannelAffinityPending(ctx, "default", channel.Id)

	selected, group, err := selectQueuedAffinityCandidate(&RetryParam{
		Ctx:                 ctx,
		TokenGroup:          "default",
		ModelName:           model.CommandFlashModel,
		RequestPath:         "/v1/chat/completions",
		AttemptedChannelIDs: map[int]struct{}{},
		OpencodePool:        true,
	})
	require.NoError(t, err)
	require.Nil(t, selected)
	require.Empty(t, group)
	require.Zero(t, GetCurrentChannelAffinitySelection(ctx))
	require.Zero(t, GetPendingChannelAffinitySelection(ctx))
}

func TestNewCodeProviderSelectionUsesLeastEffectiveLoad(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	channels := []*model.Channel{
		{Type: constant.ChannelTypeCommand, Status: common.ChannelStatusEnabled, Key: "balanced-command-a", Models: model.CommandFlashModel, Group: "default"},
		{Type: constant.ChannelTypeCommand, Status: common.ChannelStatusEnabled, Key: "balanced-command-b", Models: model.CommandFlashModel, Group: "default"},
		{Type: constant.ChannelTypeCommand, Status: common.ChannelStatusEnabled, Key: "balanced-command-c", Models: model.CommandFlashModel, Group: "default"},
	}
	for _, channel := range channels {
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group:     "default",
			Model:     model.CommandFlashModel,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
	}
	model.InitChannelCache()

	now := time.Now().Unix()
	for i := 0; i < 2; i++ {
		allowed, err := model.ReserveCommandConcurrency(channels[0].Id)
		require.NoError(t, err)
		require.True(t, allowed)
	}
	allowed, err := model.ReserveCommandConcurrency(channels[1].Id)
	require.NoError(t, err)
	require.True(t, allowed)
	const affinityKey = "channel-affinity-balanced-command-b"
	rememberChannelAffinityProtection(affinityKey, channels[1].Id, 60)
	t.Cleanup(func() { forgetChannelAffinityProtection(affinityKey) })

	selected, err := selectLeastLoadedCodeProviderCandidate(channels, now, false)
	require.NoError(t, err)
	require.Equal(t, channels[2].Id, selected.Id)
}

// 亲和请求实际占用并发位后必须释放自己的预留租约：预留(1→0) + 实际(0→1) 对应同一个
// 请求，不释放会让 least-loaded 选路把该渠道的负载看高一个名额，造成容量浪费。
func TestConvertChannelAffinityReservationToActual_ReleasesLeaseWhenSlotAcquired(t *testing.T) {
	db := useChannelAffinityRoutingDB(t)
	channel := &model.Channel{
		Type:   constant.ChannelTypeOpencode,
		Status: common.ChannelStatusEnabled,
		Key:    "opencode-affinity-convert",
		Models: model.OpencodeModel,
		Group:  "default",
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     "default",
		Model:     model.OpencodeModel,
		ChannelId: channel.Id,
		Enabled:   true,
	}).Error)
	model.InitChannelCache()

	cacheKey := channelAffinityCacheNamespace + ":opencode session trace:test-convert"
	rememberChannelAffinityProtection(cacheKey, channel.Id, 60)
	t.Cleanup(func() { forgetChannelAffinityProtection(cacheKey) })

	now := time.Now().Unix()
	require.Equal(t, 1, GetChannelAffinityReservedConcurrency(channel.Id, now))

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	setChannelAffinityContext(c, channelAffinityMeta{CacheKey: cacheKey, TTLSeconds: 60, RuleName: "opencode session trace"})
	MarkChannelAffinityUsed(c, "default", channel.Id)

	ConvertChannelAffinityReservationToActual(c)
	require.Equal(t, 0, GetChannelAffinityReservedConcurrency(channel.Id, now), "in-flight request must not keep holding its reservation lease")

	// 请求成功结束时 RecordChannelAffinity 重新武装租约，为下一轮请求继续预留。
	RecordChannelAffinity(c, channel.Id)
	require.Equal(t, 1, GetChannelAffinityReservedConcurrency(channel.Id, now), "successful request must re-arm the lease for its next turn")
}

// 没有亲和上下文的请求调用转换必须是安全 no-op，不影响其他请求的租约。
func TestConvertChannelAffinityReservationToActual_NoopWithoutAffinityMeta(t *testing.T) {
	useChannelAffinityRoutingDB(t)

	otherKey := channelAffinityCacheNamespace + ":opencode session trace:test-noop"
	rememberChannelAffinityProtection(otherKey, 999001, 60)
	t.Cleanup(func() { forgetChannelAffinityProtection(otherKey) })

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	ConvertChannelAffinityReservationToActual(c)
	ConvertChannelAffinityReservationToActual(nil)

	require.Equal(t, 1, GetChannelAffinityReservedConcurrency(999001, time.Now().Unix()), "unrelated lease must survive the no-op conversion")
}
