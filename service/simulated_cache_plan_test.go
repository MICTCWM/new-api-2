package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 第 2 轮：plan 同源规划 + 两个绝对赋值注入函数 + 展示=计费一致性。

func TestPlanSimulatedCacheCreationIdempotentReusesPlan(t *testing.T) {
	simCleanupCache(t)
	relay := simTestRelay(7101, 8101, types.RelayFormatOpenAI, simOpenAIMessage("alpha "+repeatWord("x", 200)))

	plan1 := planSimulatedCacheCreationChecked(relay, simEnabledAlways, 3000, 0, 0, 0, 0)
	require.NotNil(t, plan1)
	require.True(t, plan1.Applied)

	// 同一 RelayInfo 第二次规划：直接复用旧 plan，不二次推进缓存段。
	plan2 := planSimulatedCacheCreationChecked(relay, simEnabledAlways, 3000, 0, 0, 0, 0)
	require.NotNil(t, plan2)
	assert.Same(t, plan1, plan2)
	assert.Equal(t, plan1.HitTokens, plan2.HitTokens)
	assert.Equal(t, plan1.CreationTotal(), plan2.CreationTotal())
	assert.Equal(t, plan1.Creation5m, plan2.Creation5m)
	assert.Equal(t, plan1.Creation1h, plan2.Creation1h)
}

func TestPlanSimulatedCacheCreationDisabledReturnsNil(t *testing.T) {
	simCleanupCache(t)
	relay := simTestRelay(7102, 8102, types.RelayFormatOpenAI, simOpenAIMessage("beta "+repeatWord("x", 200)))
	plan := planSimulatedCacheCreationChecked(relay, simEnabledNever, 3000, 0, 0, 0, 0)
	assert.Nil(t, plan)
	assert.Nil(t, relay.SimulatedCachePlan)
}

func TestPlanSimulatedCacheCreationRealUpstreamValueWins(t *testing.T) {
	simCleanupCache(t)
	cases := []struct {
		name                            string
		cached, creationTotal, c5m, c1h int
	}{
		{"cached", 5, 0, 0, 0},
		{"creation_total", 0, 5, 0, 0},
		{"creation_5m", 0, 0, 5, 0},
		{"creation_1h", 0, 0, 0, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relay := simTestRelay(7103, 8103, types.RelayFormatOpenAI, simOpenAIMessage("gamma "+repeatWord("x", 200)))
			plan := planSimulatedCacheCreationChecked(relay, simEnabledAlways, 3000, tc.cached, tc.creationTotal, tc.c5m, tc.c1h)
			assert.Nil(t, plan)
		})
	}
}

func TestPlanSimulatedCacheCreationBillingInputBoundary(t *testing.T) {
	simCleanupCache(t)
	relay := simTestRelay(7104, 8104, types.RelayFormatOpenAI, simOpenAIMessage("delta "+repeatWord("z", 300)))
	assert.Nil(t, planSimulatedCacheCreationChecked(relay, simEnabledAlways, 256, 0, 0, 0, 0))

	relay2 := simTestRelay(7105, 8105, types.RelayFormatOpenAI, simOpenAIMessage("epsilon "+repeatWord("z", 300)))
	plan := planSimulatedCacheCreationChecked(relay2, simEnabledAlways, 257, 0, 0, 0, 0)
	require.NotNil(t, plan)
	assert.Equal(t, 257, plan.BillingInput)
}

func TestApplyPlanToClaudeUsageNumbers(t *testing.T) {
	plan := &SimulatedCachePlan{BillingInput: 1000, HitTokens: 100, Creation5m: 60, Creation1h: 40, TTLClass: "5m"}
	usage := &dto.ClaudeUsage{OutputTokens: 5}

	ApplyPlanToClaudeUsage(plan, usage)

	assert.Equal(t, 800, usage.InputTokens)
	assert.Equal(t, 100, usage.CacheReadInputTokens)
	assert.Equal(t, 100, usage.CacheCreationInputTokens)
	require.NotNil(t, usage.CacheCreation)
	assert.Equal(t, 60, usage.CacheCreation.Ephemeral5mInputTokens)
	assert.Equal(t, 40, usage.CacheCreation.Ephemeral1hInputTokens)
	assert.Equal(t, 5, usage.OutputTokens, "output tokens 不被改动")
}

func TestApplyPlanToUsageOpenAIKeepsPrompt(t *testing.T) {
	plan := &SimulatedCachePlan{BillingInput: 1000, HitTokens: 100, Creation5m: 60, Creation1h: 40}
	usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 5}

	ApplyPlanToUsage(plan, usage)

	assert.Equal(t, 1000, usage.PromptTokens, "openai 语义不动 PromptTokens")
	assert.Equal(t, 100, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.CacheWriteTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 60, usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 40, usage.ClaudeCacheCreation1hTokens)
	assert.Equal(t, 1005, usage.TotalTokens)
	assert.Equal(t, dto.BillingUsageSemanticOpenAI, usage.UsageSemantic)
}

func TestApplyPlanToUsageAnthropicDeductsPrompt(t *testing.T) {
	plan := &SimulatedCachePlan{BillingInput: 1000, HitTokens: 100, Creation5m: 60, Creation1h: 40}
	usage := &dto.Usage{PromptTokens: 800, CompletionTokens: 5, UsageSemantic: dto.BillingUsageSemanticAnthropic}

	ApplyPlanToUsage(plan, usage)

	assert.Equal(t, 800, usage.PromptTokens, "anthropic 语义扣减后 PromptTokens = 1000 − 100 − 100")
	assert.Equal(t, 100, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 60, usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 40, usage.ClaudeCacheCreation1hTokens)
	assert.Equal(t, 805, usage.TotalTokens)
}

// 展示=计费：响应侧按 plan 注入的字段，必须与结算 calculateTextQuotaSummary 读出的
// CacheTokens / CacheCreationTokens / 5m / 1h 完全相等。
func TestPlanDisplayEqualsBillingSummary(t *testing.T) {
	simCleanupCache(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	content := repeatWord("zeta", 40)
	relay := simTestRelay(7106, 8106, types.RelayFormatOpenAI, simOpenAIMessage(content))
	relay.PriceData = simBillingPriceData()
	relay.StartTime = time.Now()

	plan := planSimulatedCacheCreationChecked(relay, simEnabledAlways, 3000, 0, 0, 0, 0)
	require.NotNil(t, plan)

	// 响应侧（openai 语义）。
	respUsage := &dto.Usage{PromptTokens: 3000, CompletionTokens: 10}
	ApplyPlanToUsage(plan, respUsage)

	// 结算侧：复用同一 plan，不再推进段。
	billingUsage := &dto.Usage{PromptTokens: 3000, CompletionTokens: 10}
	_, out, applied := applySimulatedCacheCreationChecked(relay, billingUsage, nil, simEnabledAlways)
	require.True(t, applied)

	summary := calculateTextQuotaSummary(ctx, relay, out)

	assert.Equal(t, plan.HitTokens, summary.CacheTokens, "计费命中 == plan hit")
	assert.Equal(t, plan.CreationTotal(), summary.CacheCreationTokens, "计费创建总量 == plan creation")
	assert.Equal(t, plan.Creation5m, summary.CacheCreationTokens5m)
	assert.Equal(t, plan.Creation1h, summary.CacheCreationTokens1h)

	// 响应展示与计费同源。
	assert.Equal(t, respUsage.PromptTokensDetails.CachedTokens, summary.CacheTokens)
	assert.Equal(t, respUsage.PromptTokensDetails.CacheWriteTokens, summary.CacheCreationTokens)
	assert.Equal(t, respUsage.ClaudeCacheCreation5mTokens, summary.CacheCreationTokens5m)
	assert.Equal(t, respUsage.ClaudeCacheCreation1hTokens, summary.CacheCreationTokens1h)
}

// 结算侧已有 plan 时直接复用，不再二次规划/推进段：第二次结算拿到同样的数字。
func TestSettlementReusesPlanWithoutSecondPush(t *testing.T) {
	simCleanupCache(t)
	relay := simTestRelay(7107, 8107, types.RelayFormatOpenAI, simOpenAIMessage("eta "+repeatWord("theta", 30)))
	plan := planSimulatedCacheCreationChecked(relay, simEnabledAlways, 3000, 0, 0, 0, 0)
	require.NotNil(t, plan)

	usage1 := &dto.Usage{PromptTokens: 3000, CompletionTokens: 10}
	_, out1, applied1 := applySimulatedCacheCreationChecked(relay, usage1, nil, simEnabledAlways)
	require.True(t, applied1)

	usage2 := &dto.Usage{PromptTokens: 3000, CompletionTokens: 10}
	_, out2, applied2 := applySimulatedCacheCreationChecked(relay, usage2, nil, simEnabledAlways)
	require.True(t, applied2)

	assert.Equal(t, out1.PromptTokensDetails.CachedTokens, out2.PromptTokensDetails.CachedTokens)
	assert.Equal(t, out1.PromptTokensDetails.CacheWriteTokens, out2.PromptTokensDetails.CacheWriteTokens)
	assert.Equal(t, out1.ClaudeCacheCreation5mTokens, out2.ClaudeCacheCreation5mTokens)
	assert.Equal(t, out1.ClaudeCacheCreation1hTokens, out2.ClaudeCacheCreation1hTokens)
}
