package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 模拟创建缓存测试。缓存走 HybridCache 内存模式（未启用 Redis），各子场景使用独立的
// channelId/userId 隔离键，避免相互污染。

const simTestModel = "deepseek-chat"

func simTestRelay(userId, channelId int, format types.RelayFormat, req dto.Request) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:                  userId,
		ChannelMeta:             &relaycommon.ChannelMeta{ChannelId: channelId},
		OriginModelName:         simTestModel,
		FinalRequestRelayFormat: format,
		Request:                 req,
		StartTime:               time.Now(),
	}
}

func simEnabledAlways(*relaycommon.RelayInfo) bool { return true }

func simEnabledNever(*relaycommon.RelayInfo) bool { return false }

func simOpenAIMessage(content string) *dto.GeneralOpenAIRequest {
	return &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{{Role: "user", Content: content}},
	}
}

func simCleanupCache(t *testing.T) {
	t.Helper()
	_ = getSimulatedCacheCreationCache().Purge()
	t.Cleanup(func() { _ = getSimulatedCacheCreationCache().Purge() })
}

func simBillingPriceData() hosttypes.PriceData {
	return hosttypes.PriceData{
		ModelRatio:           1,
		CompletionRatio:      1,
		CacheRatio:           0.1,
		CacheCreationRatio:   1.25,
		CacheCreation5mRatio: 1.25,
		CacheCreation1hRatio: 2,
		GroupRatioInfo:       hosttypes.GroupRatioInfo{GroupRatio: 1},
	}
}

// ---------- 开关 / 短路 ----------

func TestSimulatedCacheCreationDisabledSkipsInjection(t *testing.T) {
	simCleanupCache(t)
	relay := simTestRelay(7001, 8001, types.RelayFormatOpenAI, simOpenAIMessage("hello world "+repeatWord("x", 200)))
	usage := &dto.Usage{PromptTokens: 2000, CompletionTokens: 10, TotalTokens: 2010}

	extra, out, applied := applySimulatedCacheCreationChecked(relay, usage, []string{}, simEnabledNever)
	assert.False(t, applied)
	assert.Equal(t, 2000, out.PromptTokens)
	assert.Equal(t, 0, out.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 0, out.PromptTokensDetails.CacheWriteTokens)
	assert.Empty(t, extra)
}

func TestSimulatedCacheCreationRealUpstreamValueWins(t *testing.T) {
	simCleanupCache(t)
	cases := []struct {
		name   string
		mutate func(u *dto.Usage)
	}{
		{"cached_tokens", func(u *dto.Usage) { u.PromptTokensDetails.CachedTokens = 5 }},
		{"cached_creation_tokens", func(u *dto.Usage) { u.PromptTokensDetails.CachedCreationTokens = 5 }},
		{"cache_write_tokens", func(u *dto.Usage) { u.PromptTokensDetails.CacheWriteTokens = 5 }},
		{"claude_creation_5m", func(u *dto.Usage) { u.ClaudeCacheCreation5mTokens = 5 }},
		{"claude_creation_1h", func(u *dto.Usage) { u.ClaudeCacheCreation1hTokens = 5 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relay := simTestRelay(7002, 8002, types.RelayFormatOpenAI, simOpenAIMessage("hello world "+repeatWord("x", 200)))
			usage := &dto.Usage{PromptTokens: 2000, CompletionTokens: 10}
			tc.mutate(usage)
			before := *usage
			_, out, applied := applySimulatedCacheCreationChecked(relay, usage, []string{}, simEnabledAlways)
			assert.False(t, applied, "真实上游 cache 值存在时整层跳过")
			assert.Equal(t, 2000, out.PromptTokens)
			assert.Equal(t, before.PromptTokensDetails, out.PromptTokensDetails, "上游 cache 字段保持原值不被覆盖")
			assert.Equal(t, before.ClaudeCacheCreation5mTokens, out.ClaudeCacheCreation5mTokens)
			assert.Equal(t, before.ClaudeCacheCreation1hTokens, out.ClaudeCacheCreation1hTokens)
		})
	}
}

func TestSimulatedCacheCreationSkipsEmptyAndOversized(t *testing.T) {
	t.Run("空消息", func(t *testing.T) {
		simCleanupCache(t)
		relay := simTestRelay(7003, 8003, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{
			Messages: []dto.Message{{Role: "user", Content: ""}},
		})
		usage := &dto.Usage{PromptTokens: 2000}
		_, out, applied := applySimulatedCacheCreationChecked(relay, usage, []string{}, simEnabledAlways)
		assert.False(t, applied)
		assert.Equal(t, 2000, out.PromptTokens)
	})

	t.Run("无消息", func(t *testing.T) {
		simCleanupCache(t)
		relay := simTestRelay(7004, 8004, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{})
		usage := &dto.Usage{PromptTokens: 2000}
		_, out, applied := applySimulatedCacheCreationChecked(relay, usage, []string{}, simEnabledAlways)
		assert.False(t, applied)
		assert.Equal(t, 2000, out.PromptTokens)
	})

	t.Run("超长 prompt 跳过", func(t *testing.T) {
		simCleanupCache(t)
		oversized := repeatWord("y", simulatedCacheCreationMaxBytes+1)
		relay := simTestRelay(7005, 8005, types.RelayFormatOpenAI, simOpenAIMessage(oversized))
		usage := &dto.Usage{PromptTokens: 5000}
		_, out, applied := applySimulatedCacheCreationChecked(relay, usage, []string{}, simEnabledAlways)
		assert.False(t, applied)
		assert.Equal(t, 5000, out.PromptTokens)
	})
}

// ---------- 256 边界（对齐 Kimi 自动缓存 >256 token） ----------

func TestSimulatedCacheCreationBillingInputBoundary(t *testing.T) {
	simCleanupCache(t)

	t.Run("256 不创建", func(t *testing.T) {
		relay := simTestRelay(7006, 8006, types.RelayFormatOpenAI, simOpenAIMessage("hello "+repeatWord("z", 300)))
		usage := &dto.Usage{PromptTokens: 256, CompletionTokens: 0}
		_, out, applied := applySimulatedCacheCreationChecked(relay, usage, []string{}, simEnabledAlways)
		assert.False(t, applied, "billingInput == 256 不触发")
		assert.Equal(t, 256, out.PromptTokens)
		assert.Equal(t, 0, out.PromptTokensDetails.CachedTokens)
	})

	t.Run("257 创建", func(t *testing.T) {
		relay := simTestRelay(7007, 8007, types.RelayFormatOpenAI, simOpenAIMessage("hello "+repeatWord("z", 300)))
		usage := &dto.Usage{PromptTokens: 257, CompletionTokens: 0}
		extra, out, applied := applySimulatedCacheCreationChecked(relay, usage, []string{}, simEnabledAlways)
		assert.True(t, applied, "billingInput == 257 触发")
		assert.Greater(t, out.PromptTokensDetails.CacheWriteTokens, 0)
		assert.NotEmpty(t, extra)
	})
}

// ---------- 前缀切分：A → A+B 只计新增 ----------

func TestSimulatedCacheCreationPrefixSplit(t *testing.T) {
	simCleanupCache(t)
	contentA := repeatWord("alpha", 40)
	suffixB := repeatWord("beta", 20)

	relayA := simTestRelay(7008, 8008, types.RelayFormatOpenAI, simOpenAIMessage(contentA))
	textA, ok := simulatedCachePrefixText(relayA)
	require.True(t, ok)
	usageA := &dto.Usage{PromptTokens: 3000, CompletionTokens: 0}
	_, _, appliedA := applySimulatedCacheCreationChecked(relayA, usageA, []string{}, simEnabledAlways)
	require.True(t, appliedA)

	relayB := simTestRelay(7008, 8008, types.RelayFormatOpenAI, simOpenAIMessage(contentA+" "+suffixB))
	textB, ok := simulatedCachePrefixText(relayB)
	require.True(t, ok)
	usageB := &dto.Usage{PromptTokens: 3000, CompletionTokens: 0}
	_, outB, appliedB := applySimulatedCacheCreationChecked(relayB, usageB, []string{}, simEnabledAlways)
	require.True(t, appliedB)

	expectedHit := CountTextToken(textA, simTestModel)
	expectedCreation := CountTextToken(textB[len(textA):], simTestModel)
	require.Greater(t, expectedHit, 0)
	require.Greater(t, expectedCreation, 0)

	assert.Equal(t, expectedHit, outB.PromptTokensDetails.CachedTokens, "命中 = 前缀 A 的累计 token")
	assert.Equal(t, expectedCreation, outB.PromptTokensDetails.CacheWriteTokens, "创建只计新增后缀 B")
	assert.Equal(t, expectedCreation, outB.PromptTokensDetails.CachedCreationTokens)
	// openai 语义不动 PromptTokens
	assert.Equal(t, 3000, outB.PromptTokens)
}

// ---------- TTL 过期与命中续期 ----------

func TestSimulatedCacheCreationTTLExpiryAndRenewal(t *testing.T) {
	simCleanupCache(t)
	base := time.Now()
	savedNow := simulatedCacheCreationNow
	simulatedCacheCreationNow = func() time.Time { return base }
	t.Cleanup(func() { simulatedCacheCreationNow = savedNow })

	content := repeatWord("gamma", 40)
	// 每次请求使用独立 RelayInfo：plan 按请求生命周期幂等，跨请求的段续期由引擎完成。
	newRelay := func() *relaycommon.RelayInfo {
		return simTestRelay(7009, 8009, types.RelayFormatOpenAI, simOpenAIMessage(content))
	}

	// 首次建链（5m 档）。
	usage := &dto.Usage{PromptTokens: 3000}
	_, _, applied := applySimulatedCacheCreationChecked(newRelay(), usage, []string{}, simEnabledAlways)
	require.True(t, applied)

	key := simulatedCacheKey(8009, 7009, simTestModel)
	entry, found, err := getSimulatedCacheCreationCache().Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, entry.Segments, 1)
	originalExpires := entry.Segments[0].ExpiresAt

	// 4 分钟后再次请求：命中且续期，过期时间被刷新到 now+5m。
	simulatedCacheCreationNow = func() time.Time { return base.Add(4 * time.Minute) }
	usage2 := &dto.Usage{PromptTokens: 3000}
	_, out2, applied2 := applySimulatedCacheCreationChecked(newRelay(), usage2, []string{}, simEnabledAlways)
	require.True(t, applied2)
	assert.Greater(t, out2.PromptTokensDetails.CachedTokens, 0, "未过期应命中")

	entry, found, err = getSimulatedCacheCreationCache().Get(key)
	require.NoError(t, err)
	require.True(t, found)
	assert.Greater(t, entry.Segments[0].ExpiresAt, originalExpires, "命中后续期，过期时间刷新")

	// 距首次 6 分钟（已过原始 5m），但距续期 2 分钟，仍在续期窗口内 → 继续命中。
	simulatedCacheCreationNow = func() time.Time { return base.Add(6 * time.Minute) }
	usage3 := &dto.Usage{PromptTokens: 3000}
	_, out3, applied3 := applySimulatedCacheCreationChecked(newRelay(), usage3, []string{}, simEnabledAlways)
	require.True(t, applied3)
	assert.Greater(t, out3.PromptTokensDetails.CachedTokens, 0, "续期窗口内继续命中")

	// 推进到第二次续期之后：5m 段过期，命中归零，整条重建。
	simulatedCacheCreationNow = func() time.Time { return base.Add(4 * time.Minute).Add(8 * time.Minute) }
	usage4 := &dto.Usage{PromptTokens: 3000}
	_, out4, applied4 := applySimulatedCacheCreationChecked(newRelay(), usage4, []string{}, simEnabledAlways)
	require.True(t, applied4)
	assert.Equal(t, 0, out4.PromptTokensDetails.CachedTokens, "过期后命中归零")
	assert.Greater(t, out4.PromptTokensDetails.CacheWriteTokens, 0, "过期后重新创建")
}

// ---------- 有界：单键 32 段上限 ----------

func TestSimulatedCacheCreationSegmentCapDiscardsEntry(t *testing.T) {
	simCleanupCache(t)
	content := repeatWord("delta", 40)
	relay := simTestRelay(7010, 8010, types.RelayFormatOpenAI, simOpenAIMessage(content))
	text, ok := simulatedCachePrefixText(relay)
	require.True(t, ok)

	// 预置 33 段（把全文切成 33 段），下次追加即超 32 段上限。
	segments := make([]SimulatedCacheSegment, 0, 33)
	step := len(text) / 33
	prev := 0
	for i := 1; i <= 33; i++ {
		end := step * i
		if end > len(text) {
			end = len(text)
		}
		chunk := text[prev:end]
		segments = append(segments, SimulatedCacheSegment{
			Chars:     len(chunk),
			Tokens:    CountTextToken(text[:end], simTestModel),
			Hash:      simulatedCacheHashText(chunk),
			TTLClass:  simulatedCacheCreationTTLClass5m,
			ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
		})
		prev = end
	}
	key := simulatedCacheKey(8010, 7010, simTestModel)
	require.NoError(t, getSimulatedCacheCreationCache().SetWithTTL(key, SimulatedCacheEntry{Segments: segments, TotalChars: len(text)}, time.Hour))

	outcome := lookupAndUpdateSimulatedCache(relay, text, simulatedCacheCreationTTLClass5m)
	assert.Equal(t, 0, outcome.HitTokens, "超上限整条丢弃，命中归零")
	assert.Greater(t, outcome.CreationTotal, 0)

	entry, found, err := getSimulatedCacheCreationCache().Get(key)
	require.NoError(t, err)
	require.True(t, found)
	assert.Len(t, entry.Segments, 1, "丢弃后按单段重建")
}

func TestSimulatedCacheCreationLRUCapacity(t *testing.T) {
	simCleanupCache(t)
	mainCap, _ := getSimulatedCacheCreationCache().Capacity()
	assert.Equal(t, simulatedCacheCreationMemoryCapacity, mainCap, "内存 LRU 容量上限 50_000")
}

// ---------- TTL 档位识别 ----------

func TestSimulatedCacheCreationTTLClassDetection(t *testing.T) {
	t.Run("Claude 顶层 cache_control 1h", func(t *testing.T) {
		relay := simTestRelay(7011, 8011, types.RelayFormatClaude, &dto.ClaudeRequest{
			CacheControl: json.RawMessage(`{"ttl":"1h"}`),
			Messages:     []dto.ClaudeMessage{{Role: "user", Content: "hi"}},
		})
		assert.Equal(t, "1h", detectSimulatedCacheTTLClass(relay))
	})

	t.Run("Claude 消息块 cache_control 1h", func(t *testing.T) {
		relay := simTestRelay(7012, 8012, types.RelayFormatClaude, &dto.ClaudeRequest{
			Messages: []dto.ClaudeMessage{{
				Role:    "user",
				Content: []any{map[string]any{"type": "text", "text": "hi", "cache_control": map[string]any{"ttl": "1h"}}},
			}},
		})
		assert.Equal(t, "1h", detectSimulatedCacheTTLClass(relay))
	})

	t.Run("OpenAI message cache_control 1h", func(t *testing.T) {
		relay := simTestRelay(7013, 8013, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{
			Messages: []dto.Message{{Role: "user", Content: "hi", CacheControl: json.RawMessage(`{"ttl":"1h"}`)}},
		})
		assert.Equal(t, "1h", detectSimulatedCacheTTLClass(relay))
	})

	t.Run("OpenAI prompt_cache_options 12h 归 1h", func(t *testing.T) {
		relay := simTestRelay(7014, 8014, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{
			Messages:           []dto.Message{{Role: "user", Content: "hi"}},
			PromptCacheOptions: json.RawMessage(`{"ttl":"12h"}`),
		})
		assert.Equal(t, "1h", detectSimulatedCacheTTLClass(relay))
	})

	t.Run("OpenAI prompt_cache_retention 24h 归 1h", func(t *testing.T) {
		relay := simTestRelay(7015, 8015, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{
			Messages:             []dto.Message{{Role: "user", Content: "hi"}},
			PromptCacheRetention: json.RawMessage(`"24h"`),
		})
		assert.Equal(t, "1h", detectSimulatedCacheTTLClass(relay))
	})

	t.Run("Responses prompt_cache_options 24h 归 1h", func(t *testing.T) {
		relay := simTestRelay(7016, 8016, types.RelayFormatOpenAIResponses, &dto.OpenAIResponsesRequest{
			Input:              json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]`),
			PromptCacheOptions: json.RawMessage(`{"ttl":"24h"}`),
		})
		assert.Equal(t, "1h", detectSimulatedCacheTTLClass(relay))
	})

	t.Run("缺省 5m", func(t *testing.T) {
		relay := simTestRelay(7017, 8017, types.RelayFormatOpenAI, simOpenAIMessage("hi"))
		assert.Equal(t, "5m", detectSimulatedCacheTTLClass(relay))
	})

	t.Run("非法 raw JSON 按 5m 不 panic", func(t *testing.T) {
		relay := simTestRelay(7018, 8018, types.RelayFormatOpenAI, &dto.GeneralOpenAIRequest{
			Messages:             []dto.Message{{Role: "user", Content: "hi"}},
			PromptCacheRetention: json.RawMessage(`{not-json`),
		})
		assert.Equal(t, "5m", detectSimulatedCacheTTLClass(relay))
	})
}

// ---------- 双语义字段注入 ----------

func TestSimulatedCacheCreationAnthropicSemanticDeductsPrompt(t *testing.T) {
	simCleanupCache(t)
	contentA := repeatWord("eta", 40)
	suffixB := repeatWord("zeta", 20)

	relayA := simTestRelay(7019, 8019, types.RelayFormatClaude, &dto.ClaudeRequest{
		Messages: []dto.ClaudeMessage{{Role: "user", Content: contentA}},
	})
	usageA := &dto.Usage{PromptTokens: 3000, CompletionTokens: 10}
	_, _, appliedA := applySimulatedCacheCreationChecked(relayA, usageA, []string{}, simEnabledAlways)
	require.True(t, appliedA)

	relayB := simTestRelay(7019, 8019, types.RelayFormatClaude, &dto.ClaudeRequest{
		Messages: []dto.ClaudeMessage{{Role: "user", Content: contentA + " " + suffixB}},
	})
	usageB := &dto.Usage{PromptTokens: 3000, CompletionTokens: 10}
	_, outB, appliedB := applySimulatedCacheCreationChecked(relayB, usageB, []string{}, simEnabledAlways)
	require.True(t, appliedB)

	// anthropic 语义：hit+creation 必须从 PromptTokens 扣掉，避免重复计费。
	expectedRemaining := 3000 - outB.PromptTokensDetails.CachedTokens - outB.PromptTokensDetails.CachedCreationTokens
	assert.Equal(t, expectedRemaining, outB.PromptTokens)
	assert.Greater(t, outB.PromptTokensDetails.CachedTokens, 0)
	assert.Greater(t, outB.PromptTokensDetails.CachedCreationTokens, 0)
	assert.Equal(t, outB.PromptTokens+outB.CompletionTokens, outB.TotalTokens)
}

func TestSimulatedCacheCreationOpenAISemanticKeepsPrompt(t *testing.T) {
	simCleanupCache(t)
	contentA := repeatWord("eta", 40)
	suffixB := repeatWord("theta", 20)

	relayA := simTestRelay(7020, 8020, types.RelayFormatOpenAI, simOpenAIMessage(contentA))
	usageA := &dto.Usage{PromptTokens: 3000, CompletionTokens: 10}
	_, _, appliedA := applySimulatedCacheCreationChecked(relayA, usageA, []string{}, simEnabledAlways)
	require.True(t, appliedA)

	relayB := simTestRelay(7020, 8020, types.RelayFormatOpenAI, simOpenAIMessage(contentA+" "+suffixB))
	usageB := &dto.Usage{PromptTokens: 3000, CompletionTokens: 10}
	_, outB, appliedB := applySimulatedCacheCreationChecked(relayB, usageB, []string{}, simEnabledAlways)
	require.True(t, appliedB)

	assert.Equal(t, 3000, outB.PromptTokens, "openai 语义不动 PromptTokens")
	assert.Greater(t, outB.PromptTokensDetails.CachedTokens, 0)
	assert.Greater(t, outB.PromptTokensDetails.CacheWriteTokens, 0)
	assert.Equal(t, outB.PromptTokensDetails.CachedCreationTokens, outB.PromptTokensDetails.CacheWriteTokens)
	assert.Equal(t, dto.BillingUsageSemanticOpenAI, outB.UsageSemantic, "注入后标记 openai 语义，避免被误判 legacy")
}

// ---------- stream / non-stream usage 形态等价 ----------

func TestSimulatedCacheCreationStreamEquivalence(t *testing.T) {
	simCleanupCache(t)
	content := repeatWord("iota", 40)

	build := func(isStream bool, userId, channelId int) *dto.Usage {
		relay := simTestRelay(userId, channelId, types.RelayFormatOpenAI, simOpenAIMessage(content))
		relay.IsStream = isStream
		usage := &dto.Usage{PromptTokens: 3000, CompletionTokens: 10}
		_, out, applied := applySimulatedCacheCreationChecked(relay, usage, []string{}, simEnabledAlways)
		require.True(t, applied)
		return out
	}

	nonStream := build(false, 7021, 8021)
	stream := build(true, 7022, 8022)
	assert.Equal(t, nonStream.PromptTokens, stream.PromptTokens)
	assert.Equal(t, nonStream.PromptTokensDetails.CachedTokens, stream.PromptTokensDetails.CachedTokens)
	assert.Equal(t, nonStream.PromptTokensDetails.CacheWriteTokens, stream.PromptTokensDetails.CacheWriteTokens)
	assert.Equal(t, nonStream.PromptTokensDetails.CachedCreationTokens, stream.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, nonStream.TotalTokens, stream.TotalTokens)
}

// ---------- 并发 -race ----------

func TestSimulatedCacheCreationConcurrent(t *testing.T) {
	simCleanupCache(t)
	contentA := repeatWord("kappa", 30)
	suffixB := repeatWord("lambda", 15)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			relay := simTestRelay(7022, 8022, types.RelayFormatOpenAI, simOpenAIMessage(contentA))
			if idx%2 == 0 {
				relay = simTestRelay(7022, 8022, types.RelayFormatOpenAI, simOpenAIMessage(contentA+" "+suffixB))
			}
			usage := &dto.Usage{PromptTokens: 5000}
			_, _, _ = applySimulatedCacheCreationChecked(relay, usage, []string{}, simEnabledAlways)
		}(i)
	}
	wg.Wait()

	entry, found, err := getSimulatedCacheCreationCache().Get(simulatedCacheKey(8022, 7022, simTestModel))
	require.NoError(t, err)
	require.True(t, found)
	assert.LessOrEqual(t, len(entry.Segments), simulatedCacheCreationMaxSegments)
}

// ---------- calculateTextQuotaSummary 实算总额（与解析式比对） ----------

func TestSimulatedQuotaSummaryAnthropicAnalytical(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	relay := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatClaude,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         simTestModel,
		PriceData:               simBillingPriceData(),
		StartTime:               time.Now(),
	}

	// 模拟钩子注入后的 anthropic usage：prompt 900（已扣 hit/creation），read 100，creation 70（5m 40 / 1h 30）。
	usage := &dto.Usage{
		PromptTokens:     900,
		CompletionTokens: 0,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         100,
			CachedCreationTokens: 70,
		},
		ClaudeCacheCreation5mTokens: 40,
		ClaudeCacheCreation1hTokens: 30,
	}

	summary := calculateTextQuotaSummary(ctx, relay, usage)

	// anthropic: input 900×1 + read 100×0.1 + 5m 40×1.25 + 1h 30×2 = 900 + 10 + 50 + 60 = 1020
	require.Equal(t, 1020, summary.Quota, "anthropic 解析总额应为 1020")
	require.Equal(t, 100, summary.CacheTokens)
	require.Equal(t, 70, summary.CacheCreationTokens)
	require.Equal(t, 40, summary.CacheCreationTokens5m)
	require.Equal(t, 30, summary.CacheCreationTokens1h)
}

func TestSimulatedQuotaSummaryOpenAIAnalyticalSplit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	relay := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		OriginModelName:         simTestModel,
		PriceData:               simBillingPriceData(),
		StartTime:               time.Now(),
	}

	// openai 非 legacy：prompt 1000，cached 100，creation 70（5m 40 / 1h 30）。
	usage := &dto.Usage{
		PromptTokens:     1000,
		CompletionTokens: 0,
		UsageSemantic:    dto.BillingUsageSemanticOpenAI,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:     100,
			CacheWriteTokens: 70,
		},
		ClaudeCacheCreation5mTokens: 40,
		ClaudeCacheCreation1hTokens: 30,
	}

	summary := calculateTextQuotaSummary(ctx, relay, usage)

	// openai: base (1000-100-70)×1 + cached 100×0.1 + 5m 40×1.25 + 1h 30×2 = 830 + 10 + 50 + 60 = 950
	require.Equal(t, 950, summary.Quota, "openai 拆分解析总额应为 950")
	require.Equal(t, 70, summary.CacheCreationTokens)
	require.Equal(t, 40, summary.CacheCreationTokens5m)
	require.Equal(t, 30, summary.CacheCreationTokens1h)
}

func TestSimulatedQuotaSummaryOpenAI5mOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	relay := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		OriginModelName:         simTestModel,
		PriceData:               simBillingPriceData(),
		StartTime:               time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     1000,
		CompletionTokens: 0,
		UsageSemantic:    dto.BillingUsageSemanticOpenAI,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:     100,
			CacheWriteTokens: 50,
		},
		ClaudeCacheCreation5mTokens: 50,
	}

	summary := calculateTextQuotaSummary(ctx, relay, usage)

	// base (1000-100-50) + 100×0.1 + 50×1.25 = 850 + 10 + 62.5 = 922.5 → 923
	require.Equal(t, 923, summary.Quota)
}

func TestSimulatedQuotaSummaryLegacyClaudeDerivedUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	relay := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		OriginModelName:         simTestModel,
		PriceData:               simBillingPriceData(),
		StartTime:               time.Now(),
	}

	// legacy Claude-derived：无 UsageSource/UsageSemantic，带 5m 字段。保持既有通用倍率行为。
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 0,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 30,
		},
		ClaudeCacheCreation5mTokens: 20,
	}

	summary := calculateTextQuotaSummary(ctx, relay, usage)
	// legacy 分支：creation 20 全走通用 CacheCreationRatio 1.25；cached 30 不扣 base。
	// 100 + 30×0.1 + 20×1.25 = 128
	require.Equal(t, 128, summary.Quota)
}

// ---------- hasBillableUsage：纯缓存请求不被零计费 ----------

func TestSimulatedHasBillableUsageCacheOnly(t *testing.T) {
	summary := textQuotaSummary{
		TotalTokens:         0,
		CacheTokens:         100,
		CacheCreationTokens: 0,
	}
	assert.True(t, summary.hasBillableUsage())

	summary2 := textQuotaSummary{TotalTokens: 0, CacheCreationTokens: 50}
	assert.True(t, summary2.hasBillableUsage())

	summary3 := textQuotaSummary{TotalTokens: 0}
	assert.False(t, summary3.hasBillableUsage())
}

func repeatWord(w string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = w
	}
	return strings.Join(parts, " ")
}
