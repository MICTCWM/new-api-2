package helper

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 1h 缓存创建倍率两条路径：管理员按模型配置的值优先；未配置（或 <=0）时回退 5m × 6/3.75。

func seedSim1hRatios(t *testing.T, createCache1h string) {
	t.Helper()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"sim-1h-ratio-model":1}`))
	require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(`{"sim-1h-ratio-model":1.25}`))
	require.NoError(t, ratio_setting.UpdateCreateCacheRatio1hByJSONString(createCache1h))
}

func TestModelPriceHelper1hCacheCreationRatioConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedModelRatios := ratio_setting.ModelRatio2JSONString()
	savedCreateCache := ratio_setting.CreateCacheRatio2JSONString()
	savedCreateCache1h := ratio_setting.CreateCacheRatio1h2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedModelRatios))
		require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(savedCreateCache))
		require.NoError(t, ratio_setting.UpdateCreateCacheRatio1hByJSONString(savedCreateCache1h))
	})

	seedSim1hRatios(t, `{"sim-1h-ratio-model":3.0}`)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "sim-1h-ratio-model",
		UserGroup:       "default",
		UsingGroup:      "default",
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, 1.25, priceData.CacheCreation5mRatio)
	require.Equal(t, 3.0, priceData.CacheCreation1hRatio, "配置存在且 >0 时取配置值")
}

func TestModelPriceHelper1hCacheCreationRatioFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedModelRatios := ratio_setting.ModelRatio2JSONString()
	savedCreateCache := ratio_setting.CreateCacheRatio2JSONString()
	savedCreateCache1h := ratio_setting.CreateCacheRatio1h2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedModelRatios))
		require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(savedCreateCache))
		require.NoError(t, ratio_setting.UpdateCreateCacheRatio1hByJSONString(savedCreateCache1h))
	})

	// 未配置 1h 倍率 → 回退 5m × 6/3.75 = 1.25 × 1.6 = 2.0
	seedSim1hRatios(t, `{}`)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "sim-1h-ratio-model",
		UserGroup:       "default",
		UsingGroup:      "default",
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, 1.25, priceData.CacheCreation5mRatio)
	require.Equal(t, 1.25*claudeCacheCreation1hMultiplier, priceData.CacheCreation1hRatio, "未配置时回退 6/3.75 倍率")

	// 配置值 <= 0 同样走兜底。
	require.NoError(t, ratio_setting.UpdateCreateCacheRatio1hByJSONString(`{"sim-1h-ratio-model":0}`))
	priceData, err = ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, 1.25*claudeCacheCreation1hMultiplier, priceData.CacheCreation1hRatio, "配置值 0 时回退兜底")
}
