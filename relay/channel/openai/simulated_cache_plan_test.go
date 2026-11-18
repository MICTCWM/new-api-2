package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 第 2 轮出站注入测试：plan 直接播种到 RelayInfo（幂等复用路径），绕过渠道开关读取。

func seededOpenAISimPlan() *service.SimulatedCachePlan {
	return &service.SimulatedCachePlan{
		Applied:      true,
		BillingInput: 1000,
		HitTokens:    100,
		Creation5m:   60,
		Creation1h:   40,
		TTLClass:     "5m",
	}
}

func newOpenAIPlanTestContext(t *testing.T, body string, isStream bool, format types.RelayFormat) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(common.RequestIdKey, "sim-plan-test")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: "gpt-test"},
		IsStream:           isStream,
		RelayFormat:        format,
		ShouldIncludeUsage: true,
		DisablePing:        true,
	}
	return c, recorder, resp, info
}

// dataFrames 从 SSE body 中按行提取所有 data: 负载。
func openaiPlanDataFrames(body string) []string {
	var frames []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			frames = append(frames, strings.TrimPrefix(line, "data: "))
		}
	}
	return frames
}

// lastUsageFrame 取末尾最后一个含 usage 的 JSON 帧（跳过 [DONE] 哨兵）。
func openaiPlanLastUsageFrame(body string) string {
	frames := openaiPlanDataFrames(body)
	for i := len(frames) - 1; i >= 0; i-- {
		if gjson.Valid(frames[i]) && gjson.Get(frames[i], "usage").Exists() {
			return frames[i]
		}
	}
	return ""
}

const openaiNonStreamBody = `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"vendor_meta":{"trace_id":"oai_001"},"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010}}`

// A. OpenAI 上游非流式（OpenAI 格式）：prompt_tokens 保持总输入不变，cached=hit，
// cache_write=creation，5m/1h 拆分；除 usage 外字段原样保留。
func TestOpenaiHandlerNonStreamInjectsPlan(t *testing.T) {
	c, recorder, resp, info := newOpenAIPlanTestContext(t, openaiNonStreamBody, false, types.RelayFormatOpenAI)
	info.SimulatedCachePlan = seededOpenAISimPlan()

	usage, err := OpenaiHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	body := recorder.Body.String()
	require.NotEmpty(t, body)

	assert.EqualValues(t, 1000, gjson.Get(body, "usage.prompt_tokens").Int())
	assert.EqualValues(t, 10, gjson.Get(body, "usage.completion_tokens").Int())
	assert.EqualValues(t, 1010, gjson.Get(body, "usage.total_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(body, "usage.prompt_tokens_details.cached_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(body, "usage.prompt_tokens_details.cache_write_tokens").Int())
	assert.EqualValues(t, 60, gjson.Get(body, "usage.claude_cache_creation_5_m_tokens").Int())
	assert.EqualValues(t, 40, gjson.Get(body, "usage.claude_cache_creation_1_h_tokens").Int())

	// 未知字段保留。
	assert.Equal(t, "chatcmpl_1", gjson.Get(body, "id").String())
	assert.Equal(t, "oai_001", gjson.Get(body, "vendor_meta.trace_id").String())

	// 返回给结算的 usage 同源。
	assert.Equal(t, 1000, usage.PromptTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.CacheWriteTokens)
	assert.Equal(t, 60, usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 40, usage.ClaudeCacheCreation1hTokens)
}

// B. plan 为 nil：非流式响应字节与基线逐字节一致。
func TestOpenaiHandlerNonStreamPlanNilKeepsBytes(t *testing.T) {
	c, recorder, resp, info := newOpenAIPlanTestContext(t, openaiNonStreamBody, false, types.RelayFormatOpenAI)

	usage, err := OpenaiHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, openaiNonStreamBody, recorder.Body.String())
}

// C. 真实上游已有缓存值：真实值优先，plan 不生效，响应保持上游原值。
func TestOpenaiHandlerNonStreamRealUpstreamKept(t *testing.T) {
	body := `{"id":"chatcmpl_2","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,"prompt_tokens_details":{"cached_tokens":500,"cache_write_tokens":300}}}`
	c, recorder, resp, info := newOpenAIPlanTestContext(t, body, false, types.RelayFormatOpenAI)

	usage, err := OpenaiHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, body, recorder.Body.String())
	assert.Equal(t, 500, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 300, usage.PromptTokensDetails.CacheWriteTokens)
}

const openaiStreamChunk1 = `{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`
const openaiStreamChunk2 = `{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010}}`

func openaiStreamBody() string {
	return strings.Join([]string{
		"data: " + openaiStreamChunk1,
		"data: " + openaiStreamChunk2,
		"data: [DONE]",
		"",
	}, "\n")
}

// D. OpenAI 上游流式：最后一帧自带 usage，规划生效后重写该帧 usage。
func TestOaiStreamHandlerLastFrameInjectsPlan(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, resp, info := newOpenAIPlanTestContext(t, openaiStreamBody(), true, types.RelayFormatOpenAI)
	info.SimulatedCachePlan = seededOpenAISimPlan()

	usage, err := OaiStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	body := recorder.Body.String()
	finalData := openaiPlanLastUsageFrame(body)
	require.NotEmpty(t, finalData)

	assert.EqualValues(t, 1000, gjson.Get(finalData, "usage.prompt_tokens").Int())
	assert.EqualValues(t, 10, gjson.Get(finalData, "usage.completion_tokens").Int())
	assert.EqualValues(t, 1010, gjson.Get(finalData, "usage.total_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(finalData, "usage.prompt_tokens_details.cached_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(finalData, "usage.prompt_tokens_details.cache_write_tokens").Int())
	assert.EqualValues(t, 60, gjson.Get(finalData, "usage.claude_cache_creation_5_m_tokens").Int())
	assert.EqualValues(t, 40, gjson.Get(finalData, "usage.claude_cache_creation_1_h_tokens").Int())
	assert.Contains(t, body, "data: [DONE]")

	assert.Equal(t, 1000, usage.PromptTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.CacheWriteTokens)
}

// E. 流式 plan 为 nil：帧字节与基线一致。
func TestOaiStreamHandlerPlanNilKeepsBytes(t *testing.T) {
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, resp, info := newOpenAIPlanTestContext(t, openaiStreamBody(), true, types.RelayFormatOpenAI)

	usage, err := OaiStreamHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	// 逐帧比较（框架会在帧间补空行，不逐字节比较整段 SSE）。
	assert.Equal(t, openaiPlanDataFrames(openaiStreamBody()), openaiPlanDataFrames(recorder.Body.String()))
}

// F. OpenAI 上游 → Claude 格式（非流式）：转换前已按 openai 语义注入 usage，
// 转换器派生 anthropic 字段：input = prompt − cached − creation，read=hit，creation=总量。
func TestOpenaiHandlerToClaudeFormatInjectsPlan(t *testing.T) {
	c, recorder, resp, info := newOpenAIPlanTestContext(t, openaiNonStreamBody, false, types.RelayFormatClaude)
	info.SimulatedCachePlan = seededOpenAISimPlan()

	usage, err := OpenaiHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)

	body := recorder.Body.String()
	require.NotEmpty(t, body)

	assert.EqualValues(t, 800, gjson.Get(body, "usage.input_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(body, "usage.cache_read_input_tokens").Int())
	assert.EqualValues(t, 100, gjson.Get(body, "usage.cache_creation_input_tokens").Int())
	assert.EqualValues(t, 60, gjson.Get(body, "usage.cache_creation.ephemeral_5m_input_tokens").Int())
	assert.EqualValues(t, 40, gjson.Get(body, "usage.cache_creation.ephemeral_1h_input_tokens").Int())
	assert.EqualValues(t, 10, gjson.Get(body, "usage.output_tokens").Int())

	// 结算侧 usage 仍是 openai 语义（prompt 保持总量）。
	assert.Equal(t, 1000, usage.PromptTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 100, usage.PromptTokensDetails.CacheWriteTokens)
}
