package claude

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 第 2 轮出站注入测试：plan 直接播种到 RelayInfo（幂等复用路径），绕过渠道开关读取。

func seededSimPlan() *service.SimulatedCachePlan {
	return &service.SimulatedCachePlan{
		Applied:      true,
		BillingInput: 1000,
		HitTokens:    100,
		Creation5m:   60,
		Creation1h:   40,
		TTLClass:     "5m",
	}
}

func newClaudePlanTestContext(t *testing.T, format types.RelayFormat) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo, *ClaudeResponseInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta:             &relaycommon.ChannelMeta{},
		RelayFormat:             format,
		FinalRequestRelayFormat: format,
		ShouldIncludeUsage:      true,
	}
	claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}
	return c, recorder, info, claudeInfo
}

// dataFrames 从 SSE body 中按行提取所有 data: 负载（JSON 字符串）。
func dataFrames(body string) []string {
	var frames []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			frames = append(frames, strings.TrimPrefix(line, "data: "))
		}
	}
	return frames
}

func lastDataFrame(body string) string {
	frames := dataFrames(body)
	if len(frames) == 0 {
		return ""
	}
	return frames[len(frames)-1]
}

// lastUsageFrame 取末尾最后一个含 usage 的 JSON 帧（跳过 [DONE] 哨兵）。
func lastUsageFrame(body string) string {
	for i := len(dataFrames(body)) - 1; i >= 0; i-- {
		frame := dataFrames(body)[i]
		if gjson.Valid(frame) && gjson.Get(frame, "usage").Exists() {
			return frame
		}
	}
	return ""
}

const claudeNativeResponseBody = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"hello"}],"vendor_meta":{"trace_id":"trace_001"},"usage":{"input_tokens":1000,"output_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`

// A. Claude 原生非流式：四个字段断言，5m+1h == 总量，除 usage 外字段保持原样。
func TestHandleClaudeResponseDataNativeInjectsPlan(t *testing.T) {
	c, recorder, info, claudeInfo := newClaudePlanTestContext(t, types.RelayFormatClaude)
	info.SimulatedCachePlan = seededSimPlan()

	err := HandleClaudeResponseData(c, info, claudeInfo, nil, []byte(claudeNativeResponseBody))
	require.Nil(t, err)

	body := recorder.Body.String()
	require.NotEmpty(t, body)

	assert.EqualValues(t, 800, gjson.Get(body, "usage.input_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(body, "usage.cache_read_input_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(body, "usage.cache_creation_input_tokens").Int())
	assert.EqualValues(t, 60, gjson.Get(body, "usage.cache_creation.ephemeral_5m_input_tokens").Int())
	assert.EqualValues(t, 40, gjson.Get(body, "usage.cache_creation.ephemeral_1h_input_tokens").Int())
	assert.EqualValues(t, 10, gjson.Get(body, "usage.output_tokens").Int())
	assert.Equal(t, 60+40, int(gjson.Get(body, "usage.cache_creation_input_tokens").Int()))

	// 除 usage 外的字段原样保留。
	assert.Equal(t, "msg_1", gjson.Get(body, "id").String())
	assert.Equal(t, "message", gjson.Get(body, "type").String())
	assert.Equal(t, "trace_001", gjson.Get(body, "vendor_meta.trace_id").String())
	assert.Equal(t, "hello", gjson.Get(body, "content.0.text").String())

	// 返回给结算的 usage 同源。
	assert.Equal(t, 800, claudeInfo.Usage.PromptTokens)
	assert.Equal(t, 100, claudeInfo.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 60, claudeInfo.Usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 40, claudeInfo.Usage.ClaudeCacheCreation1hTokens)
}

// 开关关闭 / plan 为 nil：非流式响应字节与基线逐字节一致。
func TestHandleClaudeResponseDataNativePlanNilKeepsBytes(t *testing.T) {
	c, recorder, info, claudeInfo := newClaudePlanTestContext(t, types.RelayFormatClaude)

	err := HandleClaudeResponseData(c, info, claudeInfo, nil, []byte(claudeNativeResponseBody))
	require.Nil(t, err)
	assert.Equal(t, claudeNativeResponseBody, recorder.Body.String())
}

// C. Chat 非流式（Claude 上游）：prompt_tokens 为输入总量，cached/hit、cache_write/creation、拆分正确。
func TestHandleClaudeResponseDataChatInjectsPlan(t *testing.T) {
	c, recorder, info, claudeInfo := newClaudePlanTestContext(t, types.RelayFormatOpenAI)
	info.SimulatedCachePlan = seededSimPlan()

	err := HandleClaudeResponseData(c, info, claudeInfo, nil, []byte(claudeNativeResponseBody))
	require.Nil(t, err)

	body := recorder.Body.String()
	require.NotEmpty(t, body)

	// total input = remaining 800 + hit 100 + creation 100 = 1000。
	assert.EqualValues(t, 1000, gjson.Get(body, "usage.prompt_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(body, "usage.prompt_tokens_details.cached_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(body, "usage.prompt_tokens_details.cache_write_tokens").Int())
	assert.EqualValues(t, 60, gjson.Get(body, "usage.claude_cache_creation_5_m_tokens").Int())
	assert.EqualValues(t, 40, gjson.Get(body, "usage.claude_cache_creation_1_h_tokens").Int())
	assert.EqualValues(t, 1010, gjson.Get(body, "usage.total_tokens").Int())
}

const claudeMessageStartEvent = `{"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`

// B. Claude 原生 SSE message_start：注入 message.usage 并同步 claudeInfo.Usage；
// 随后 message_delta 不得把 5m/1h 归零；事件序列与 event 名称不变。
func TestHandleStreamResponseDataMessageStartInjectsPlanAndDeltaKeepsCache(t *testing.T) {
	c, recorder, info, claudeInfo := newClaudePlanTestContext(t, types.RelayFormatClaude)
	info.SimulatedCachePlan = seededSimPlan()

	err := HandleStreamResponseData(c, info, claudeInfo, claudeMessageStartEvent)
	require.Nil(t, err)

	body := recorder.Body.String()
	require.Contains(t, body, "event: message_start")
	frames := dataFrames(body)
	require.Len(t, frames, 1)
	startData := frames[0]
	assert.EqualValues(t, 800, gjson.Get(startData, "message.usage.input_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(startData, "message.usage.cache_read_input_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(startData, "message.usage.cache_creation_input_tokens").Int())
	assert.EqualValues(t, 60, gjson.Get(startData, "message.usage.cache_creation.ephemeral_5m_input_tokens").Int())
	assert.EqualValues(t, 40, gjson.Get(startData, "message.usage.cache_creation.ephemeral_1h_input_tokens").Int())

	// claudeInfo.Usage 已同步注入值。
	assert.Equal(t, 800, claudeInfo.Usage.PromptTokens)
	assert.Equal(t, 100, claudeInfo.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 60, claudeInfo.Usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 40, claudeInfo.Usage.ClaudeCacheCreation1hTokens)

	// message_delta 只带 output_tokens：patch 应从 claudeInfo.Usage 补齐 cache 字段，不归零。
	deltaEvent := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`
	err = HandleStreamResponseData(c, info, claudeInfo, deltaEvent)
	require.Nil(t, err)

	deltaBody := recorder.Body.String()
	assert.Contains(t, deltaBody, "event: message_delta")
	// 最后一帧（message_delta）的 cache 字段保持注入值，不归零。
	deltaData := lastDataFrame(deltaBody)
	assert.EqualValues(t, 100, gjson.Get(deltaData, "usage.cache_read_input_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(deltaData, "usage.cache_creation_input_tokens").Int())
	assert.EqualValues(t, 60, gjson.Get(deltaData, "usage.cache_creation.ephemeral_5m_input_tokens").Int())
	assert.EqualValues(t, 40, gjson.Get(deltaData, "usage.cache_creation.ephemeral_1h_input_tokens").Int())
}

// B2. message_start 无 plan：事件原样透传，字节不变。
func TestHandleStreamResponseDataMessageStartPlanNilKeepsBytes(t *testing.T) {
	c, recorder, info, claudeInfo := newClaudePlanTestContext(t, types.RelayFormatClaude)

	err := HandleStreamResponseData(c, info, claudeInfo, claudeMessageStartEvent)
	require.Nil(t, err)
	assert.Contains(t, recorder.Body.String(), "data: "+claudeMessageStartEvent)
}

// C2. Chat 流式（Claude 上游）：message_start 后补发的 final usage 帧携带注入字段。
func TestHandleStreamFinalResponseChatUsageFrameInjectsPlan(t *testing.T) {
	c, recorder, info, claudeInfo := newClaudePlanTestContext(t, types.RelayFormatOpenAI)
	info.SimulatedCachePlan = seededSimPlan()

	err := HandleStreamResponseData(c, info, claudeInfo, claudeMessageStartEvent)
	require.Nil(t, err)

	HandleStreamFinalResponse(c, info, claudeInfo)

	body := recorder.Body.String()
	require.Contains(t, body, "chat.completion.chunk")
	finalData := lastUsageFrame(body)
	assert.EqualValues(t, 1000, gjson.Get(finalData, "usage.prompt_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(finalData, "usage.prompt_tokens_details.cached_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(finalData, "usage.prompt_tokens_details.cache_write_tokens").Int())
	assert.EqualValues(t, 60, gjson.Get(finalData, "usage.claude_cache_creation_5_m_tokens").Int())
	assert.EqualValues(t, 40, gjson.Get(finalData, "usage.claude_cache_creation_1_h_tokens").Int())
}
