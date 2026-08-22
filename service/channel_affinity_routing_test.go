package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
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
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.Ability{},
		&model.CommandChannelUsage{},
		&model.CommandPoolUsage{},
		&model.SpeedProtectionState{},
	))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
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
