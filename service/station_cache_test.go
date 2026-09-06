package service

import (
	"encoding/json"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 站内缓存测试。缓存使用 HybridCache 的内存模式（Redis 未启用时自动走内存热缓存），
// 测试不依赖真实 Redis。各子场景使用不同的 userId 隔离缓存键，避免相互污染。

const stationCacheTestModel = "deepseek-chat"

func stationCacheTestRelayInfo(userId int) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:          userId,
		OriginModelName: stationCacheTestModel,
	}
}

func stationCacheSeededTotal(t *testing.T, userId int, msgs []string) int {
	t.Helper()
	relay := stationCacheTestRelayInfo(userId)
	_ = lookupAndUpdateStationCache(relay, msgs)
	key := stationCacheKey(relay.UserId, stationCacheTestModel)
	entry, found, err := getStationCache().Get(key)
	require.True(t, err == nil && found, "缓存应已写入")
	require.NotEmpty(t, entry.CumulativeTokens)
	return entry.CumulativeTokens[len(entry.CumulativeTokens)-1]
}

func TestStationCachePrefixHit(t *testing.T) {
	userPrefix := 1000
	reqA := []string{"system 你是助手", "user 你好", "user 讲个故事"}
	reqB := []string{"system 你是助手", "user 你好", "user 换个话题"} // 与 reqA 前 2 条相同
	reqC := []string{"system 全新的开头"}                        // 与 reqA 首条不同

	t.Run("完全命中", func(t *testing.T) {
		userId := userPrefix + 1
		expected := stationCacheSeededTotal(t, userId, reqA)
		require.True(t, expected > 0)
		relay := stationCacheTestRelayInfo(userId)
		hit := lookupAndUpdateStationCache(relay, reqA)
		assert.Equal(t, expected, hit, "与缓存相同则完全命中，命中数等于缓存累计 token")
	})

	t.Run("部分命中前k条", func(t *testing.T) {
		userId := userPrefix + 2
		_ = stationCacheSeededTotal(t, userId, reqA)
		// 记录 reqA 前 2 条的累计 token 作为部分命中预期
		key := stationCacheKey(userId, stationCacheTestModel)
		entry, found, _ := getStationCache().Get(key)
		require.True(t, found)
		expectedPartial := entry.CumulativeTokens[1]
		relay := stationCacheTestRelayInfo(userId)
		hit := lookupAndUpdateStationCache(relay, reqB)
		assert.Equal(t, expectedPartial, hit, "前 2 条相同则命中前 2 条的累计 token")
		assert.True(t, hit > 0)
	})

	t.Run("无命中", func(t *testing.T) {
		userId := userPrefix + 3
		_ = stationCacheSeededTotal(t, userId, reqA)
		relay := stationCacheTestRelayInfo(userId)
		hit := lookupAndUpdateStationCache(relay, reqC)
		assert.Equal(t, 0, hit, "首条不同则无命中")
	})
}

func TestStationCacheWriteAndUpdate(t *testing.T) {
	userId := 2000
	relay := stationCacheTestRelayInfo(userId)
	reqA := []string{"user 第一问", "user 第二问"}
	reqB := []string{"user 完全不同的问题"}
	key := stationCacheKey(userId, stationCacheTestModel)

	_ = lookupAndUpdateStationCache(relay, reqA)
	// 第二次请求把最新缓存替换为 reqB（"最近一次请求"即最新缓存态）
	hitB := lookupAndUpdateStationCache(relay, reqB)
	assert.Equal(t, 0, hitB, "reqB 与 reqA 不相似，无命中")

	// 验证缓存确实被 reqB 取代
	entry, found, _ := getStationCache().Get(key)
	require.True(t, found, "缓存应已被 reqB 写入")
	require.Equal(t, len(reqB), len(entry.MessageHashes))
	assert.Equal(t, stationCacheHash(reqB[0]), entry.MessageHashes[0], "缓存首条哈希应来自 reqB")

	// 此时请求 reqA 命中 reqB 缓存应无命中
	hitA := lookupAndUpdateStationCache(relay, reqA)
	assert.Equal(t, 0, hitA, "缓存为 reqB 时请求 reqA 应无命中")
}

func TestStationCacheHitClamp(t *testing.T) {
	userId := 3000
	relay := stationCacheTestRelayInfo(userId)
	msgs := []string{"user 一段输入", "user 另一段输入", "user 第三段"}

	inputTotal := 0
	for _, m := range msgs {
		inputTotal += CountTextToken(m, stationCacheTestModel)
	}
	require.True(t, inputTotal > 0)

	// 预置一个累计 token 被异常放大的缓存记录
	hashes := make([]string, len(msgs))
	for i, m := range msgs {
		hashes[i] = stationCacheHash(m)
	}
	inflated := make([]int, len(msgs))
	for i := range msgs {
		inflated[i] = 1_000_000_000 + i
	}
	_ = getStationCache().SetWithTTL(stationCacheKey(userId, stationCacheTestModel), StationCacheEntry{
		MessageHashes:    hashes,
		CumulativeTokens: inflated,
	}, time.Hour)

	hit := lookupAndUpdateStationCache(relay, msgs)
	assert.LessOrEqual(t, hit, inputTotal, "命中数必须 clamp 在本批全部输入 token 之内")
	assert.GreaterOrEqual(t, hit, 0, "命中数不能为负")
	assert.Equal(t, inputTotal, hit, "缓存累计异常大时应 clamp 到输入总数")
}

func TestStationCacheMergeCachedTokens(t *testing.T) {
	// 命中大于上游已报缓存 → 使用命中
	assert.Equal(t, 5, mergeStationCacheCached(3, 5), "hit > upstream 应取 hit")
	// 上游已报缓存大于命中 → 保持上游，不覆盖
	assert.Equal(t, 8, mergeStationCacheCached(8, 5), "upstream > hit 应保持 upstream")
	// 相等
	assert.Equal(t, 5, mergeStationCacheCached(5, 5), "相等时取其一")
	// 均为 0
	assert.Equal(t, 0, mergeStationCacheCached(0, 0))
}

func TestStationCacheOpenAIMessageNormalization(t *testing.T) {
	t.Run("字符串content", func(t *testing.T) {
		m := dto.Message{Role: "user", Content: "hello world"}
		assert.Equal(t, "user\nhello world", openaiMessageToText(m))
	})

	t.Run("数组content仅取文本", func(t *testing.T) {
		m := dto.Message{
			Role: "user",
			Content: []any{
				map[string]any{"type": "text", "text": "part one"},
				map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "http://example.com/a.png"},
				},
				map[string]any{"type": "text", "text": " part two"},
			},
		}
		// 图片等媒体内容不进缓存匹配文本
		assert.Equal(t, "user\npart one\n part two", openaiMessageToText(m))
	})

	t.Run("带name与tool_calls", func(t *testing.T) {
		m := dto.Message{
			Role:      "assistant",
			Name:      strPtr("assistant-name"),
			Content:   "调用一下工具",
			ToolCalls: json.RawMessage(`[{"function":{"name":"search","arguments":"{\"q\":\"1\"}"}}]`),
		}
		assert.Equal(t, "assistant\n调用一下工具\nname:assistant-name\ntool:search\n{\"q\":\"1\"}", openaiMessageToText(m))
	})
}

func TestStationCacheClaudeMessageNormalization(t *testing.T) {
	t.Run("字符串content", func(t *testing.T) {
		m := dto.ClaudeMessage{Role: "user", Content: "claude text"}
		assert.Equal(t, "user\nclaude text", claudeMessageToText(m))
	})

	t.Run("数组content取文本与tool", func(t *testing.T) {
		m := dto.ClaudeMessage{
			Role: "assistant",
			Content: []any{
				map[string]any{"type": "text", "text": "我来查一下"},
				map[string]any{"type": "tool_use", "name": "web_search", "input": map[string]any{"q": "1"}},
			},
		}
		// 文本 + 工具名都会拼入该条消息的规范化文本
		out := claudeMessageToText(m)
		assert.Contains(t, out, "我来查一下")
		assert.Contains(t, out, "tool:web_search")
	})
}

func TestStationCacheExtractMessages(t *testing.T) {
	t.Run("OpenAI chat", func(t *testing.T) {
		relay := stationCacheTestRelayInfo(4001)
		req := &dto.GeneralOpenAIRequest{
			Messages: []dto.Message{
				{Role: "system", Content: "sys"},
				{Role: "user", Content: "你好"},
			},
		}
		relay.Request = req
		msgs, ok := extractStationCacheMessages(relay)
		require.True(t, ok)
		assert.Equal(t, []string{"system\nsys", "user\n你好"}, msgs)
	})

	t.Run("Claude", func(t *testing.T) {
		relay := stationCacheTestRelayInfo(4002)
		relay.Request = &dto.ClaudeRequest{
			Messages: []dto.ClaudeMessage{
				{Role: "user", Content: "claude hello"},
			},
		}
		msgs, ok := extractStationCacheMessages(relay)
		require.True(t, ok)
		assert.Equal(t, []string{"user\nclaude hello"}, msgs)
	})

	t.Run("Gemini", func(t *testing.T) {
		relay := stationCacheTestRelayInfo(4003)
		relay.Request = &dto.GeminiChatRequest{
			Contents: []dto.GeminiChatContent{
				{Role: "user", Parts: []dto.GeminiPart{{Text: "gemini hi"}}},
				{Role: "model", Parts: []dto.GeminiPart{{Text: "gemini bye"}}},
			},
		}
		msgs, ok := extractStationCacheMessages(relay)
		require.True(t, ok)
		assert.Equal(t, []string{"user\ngemini hi", "model\ngemini bye"}, msgs)
	})

	t.Run("Responses", func(t *testing.T) {
		relay := stationCacheTestRelayInfo(4004)
		relay.Request = &dto.OpenAIResponsesRequest{
			Input: json.RawMessage(`[
				{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"yo"}]}
			]`),
		}
		msgs, ok := extractStationCacheMessages(relay)
		require.True(t, ok)
		assert.Equal(t, []string{"user\nhi", "assistant\nyo"}, msgs)
	})

	t.Run("不支持格式返回false", func(t *testing.T) {
		relay := stationCacheTestRelayInfo(4005)
		relay.Request = &dto.RerankRequest{}
		_, ok := extractStationCacheMessages(relay)
		assert.False(t, ok)
	})

	t.Run("空消息返回false", func(t *testing.T) {
		relay := stationCacheTestRelayInfo(4006)
		relay.Request = &dto.GeneralOpenAIRequest{
			Messages: []dto.Message{{Role: "user", Content: ""}},
		}
		_, ok := extractStationCacheMessages(relay)
		assert.False(t, ok)
	})
}

func TestStationCacheEnabledFromJSON(t *testing.T) {
	assert.False(t, stationCacheEnabledFromJSON(""), "空字符串视为未启用")
	assert.True(t, stationCacheEnabledFromJSON(`{"station_cache_enabled":true}`), "显式开启")
	assert.False(t, stationCacheEnabledFromJSON(`{"station_cache_enabled":false}`), "显式关闭")
	assert.False(t, stationCacheEnabledFromJSON(`{"other_setting":1}`), "缺省为关闭")
	assert.False(t, stationCacheEnabledFromJSON(`{"station_cache_enabled":true`), "非法 JSON 视为未启用")
	assert.False(t, stationCacheEnabledFromJSON("not-json"), "非 JSON 视为未启用")
}

func strPtr(s string) *string { return &s }

// stationCacheEnabledAlways 测试用：渠道站内缓存开关恒开启。
func stationCacheEnabledAlways(*relaycommon.RelayInfo) bool { return true }

func TestStationCacheShouldApplyShortCircuit(t *testing.T) {
	t.Run("nil relayInfo", func(t *testing.T) {
		assert.False(t, shouldApplyStationCache(nil))
	})
	t.Run("无Request", func(t *testing.T) {
		relay := stationCacheTestRelayInfo(6001)
		relay.FinalRequestRelayFormat = types.RelayFormatOpenAI
		assert.False(t, shouldApplyStationCache(relay), "Request 为 nil 应短路")
	})
	t.Run("非文本格式", func(t *testing.T) {
		for _, format := range []types.RelayFormat{
			types.RelayFormatEmbedding,
			types.RelayFormatRerank,
			types.RelayFormatOpenAIImage,
			types.RelayFormatOpenAIAudio,
		} {
			relay := stationCacheTestRelayInfo(6002)
			relay.FinalRequestRelayFormat = format
			relay.Request = &dto.RerankRequest{}
			assert.False(t, shouldApplyStationCache(relay), "%s 应短路", format)
		}
	})
	t.Run("文本格式", func(t *testing.T) {
		relay := stationCacheTestRelayInfo(6003)
		relay.FinalRequestRelayFormat = types.RelayFormatOpenAI
		relay.Request = &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "hi"}}}
		assert.True(t, shouldApplyStationCache(relay))
	})
	t.Run("Claude文本格式不短路", func(t *testing.T) {
		relay := stationCacheTestRelayInfo(6004)
		relay.FinalRequestRelayFormat = types.RelayFormatClaude
		relay.Request = &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Role: "user", Content: "hi"}}}
		// Claude 属于文本格式，不在此处短路；是否注入由 applyStationCacheChecked 内的 Claude 语义判断负责。
		assert.True(t, shouldApplyStationCache(relay))
	})
}

// TestStationCacheApplyNoChannelReadOnShortCircuit 验证短路路径不会读取渠道开关。
func TestStationCacheApplyNoChannelReadOnShortCircuit(t *testing.T) {
	t.Run("非文本格式不读取渠道开关", func(t *testing.T) {
		called := false
		enabled := func(*relaycommon.RelayInfo) bool { called = true; return true }
		relay := stationCacheTestRelayInfo(6101)
		relay.FinalRequestRelayFormat = types.RelayFormatEmbedding
		relay.Request = &dto.RerankRequest{}
		_, _ = applyStationCacheChecked(relay, nil, nil, enabled)
		assert.False(t, called, "非文本格式应短路，不读取渠道开关")
	})
	t.Run("无Request不读取渠道开关", func(t *testing.T) {
		called := false
		enabled := func(*relaycommon.RelayInfo) bool { called = true; return true }
		relay := stationCacheTestRelayInfo(6102)
		relay.FinalRequestRelayFormat = types.RelayFormatOpenAI
		_, _ = applyStationCacheChecked(relay, nil, nil, enabled)
		assert.False(t, called, "Request 为 nil 应短路，不读取渠道开关")
	})
	t.Run("文本格式读取渠道开关", func(t *testing.T) {
		called := false
		enabled := func(*relaycommon.RelayInfo) bool { called = true; return true }
		relay := stationCacheTestRelayInfo(6103)
		relay.FinalRequestRelayFormat = types.RelayFormatOpenAI
		relay.Request = &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "hi"}}}
		_, _ = applyStationCacheChecked(relay, nil, nil, enabled)
		assert.True(t, called, "文本格式应读取渠道开关")
	})
}

// TestStationCacheClaudeSkipsInjection 验证 Claude 语义渠道不注入站内缓存命中。
func TestStationCacheClaudeSkipsInjection(t *testing.T) {
	userId := 6201
	relay := stationCacheTestRelayInfo(userId)
	relay.FinalRequestRelayFormat = types.RelayFormatClaude
	relay.Request = &dto.ClaudeRequest{
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "claude cached content"},
		},
	}
	// 先预置缓存记录，若注入逻辑被错误移除，第二次请求命中即可暴露问题。
	msgs, ok := extractStationCacheMessages(relay)
	require.True(t, ok)
	_ = lookupAndUpdateStationCache(relay, msgs)

	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}
	extra, out := applyStationCacheChecked(relay, usage, []string{}, stationCacheEnabledAlways)
	assert.Equal(t, 0, out.PromptTokensDetails.CachedTokens, "Claude 语义渠道不应注入站内缓存命中")
	for _, line := range extra {
		assert.NotContains(t, line, "站内缓存命中", "Claude 语义渠道不应追加命中日志")
	}
	assert.Equal(t, 100, out.PromptTokens, "usage 原样返回")
}

// TestStationCacheClampToPromptTokens 验证合并前命中值 clamp 到 usage.PromptTokens。
func TestStationCacheClampToPromptTokens(t *testing.T) {
	userId := 6301
	relay := stationCacheTestRelayInfo(userId)
	relay.FinalRequestRelayFormat = types.RelayFormatOpenAI
	relay.Request = &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "你好"},
		},
	}
	msgs, ok := extractStationCacheMessages(relay)
	require.True(t, ok)
	// 预置一条累计 token 远超实际输入的消息记录，模拟本地估算的命中值偏大。
	hashes := make([]string, len(msgs))
	for i, m := range msgs {
		hashes[i] = stationCacheHash(m)
	}
	_ = getStationCache().SetWithTTL(stationCacheKey(userId, stationCacheTestModel), StationCacheEntry{
		MessageHashes:    hashes,
		CumulativeTokens: []int{1_000_000},
	}, time.Hour)

	// 上游报告 PromptTokens 远小于本地估算命中。
	usage := &dto.Usage{PromptTokens: 3, CompletionTokens: 0, TotalTokens: 3}
	_, out := applyStationCacheChecked(relay, usage, []string{}, stationCacheEnabledAlways)
	assert.LessOrEqual(t, out.PromptTokensDetails.CachedTokens, 3, "合并后的 CachedTokens 不能超过 usage.PromptTokens")
	assert.GreaterOrEqual(t, out.PromptTokensDetails.CachedTokens, 0, "CachedTokens 不能为负")
}
