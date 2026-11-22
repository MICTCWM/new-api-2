package commandgo

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestTranslator() *ndjsonTranslator {
	return &ndjsonTranslator{id: "chatcmpl-test", model: Model, created: 1}
}

func TestParseLineTextReasoningToolAndUsage(t *testing.T) {
	translator := newTestTranslator()
	lines := []string{
		`{"type":"start"}`,
		`{"type":"reasoning-delta","text":"thinking"}`,
		`{"type":"text-delta","text":"Hello"}`,
		`{"type":"tool-call","toolCallId":"call_1","toolName":"shell","input":{"cmd":"ls"}}`,
		`{"type":"finish","finishReason":"tool-calls","rawFinishReason":"tool-calls","totalUsage":{"inputTokens":10,"outputTokens":5,"cachedInputTokens":2}}`,
	}
	var chunks []*dto.ChatCompletionsStreamResponse
	for _, line := range lines {
		chunks = append(chunks, translator.parseLine(line)...)
	}
	require.Len(t, chunks, 3)

	first := chunks[0]
	assert.Equal(t, "assistant", first.Choices[0].Delta.Role)
	require.NotNil(t, first.Choices[0].Delta.ReasoningContent)
	assert.Equal(t, "thinking", *first.Choices[0].Delta.ReasoningContent)

	second := chunks[1]
	assert.Equal(t, "", second.Choices[0].Delta.Role)
	require.NotNil(t, second.Choices[0].Delta.Content)
	assert.Equal(t, "Hello", *second.Choices[0].Delta.Content)

	third := chunks[2]
	require.Len(t, third.Choices[0].Delta.ToolCalls, 1)
	call := third.Choices[0].Delta.ToolCalls[0]
	assert.Equal(t, "call_1", call.ID)
	assert.Equal(t, "shell", call.Function.Name)
	assert.Equal(t, `{"cmd":"ls"}`, call.Function.Arguments)
	require.NotNil(t, call.Index)
	assert.Equal(t, 0, *call.Index)

	assert.True(t, translator.sawFinish)
	assert.Equal(t, "tool_calls", translator.finishReason)
	require.NotNil(t, translator.usage)
	assert.Equal(t, 10, translator.usage.PromptTokens)
	assert.Equal(t, 5, translator.usage.CompletionTokens)
	assert.Equal(t, 15, translator.usage.TotalTokens)
	assert.Equal(t, 2, translator.usage.PromptTokensDetails.CachedTokens)

	finishChunk := translator.makeFinishChunk(toOpenAIFinishReason(translator.finishReason))
	require.NotNil(t, finishChunk.Choices[0].FinishReason)
	assert.Equal(t, "tool_calls", *finishChunk.Choices[0].FinishReason)

	usageChunk := translator.makeUsageChunk(translator.usage)
	assert.Empty(t, usageChunk.Choices)
	require.NotNil(t, usageChunk.Usage)
}

func TestParseLineReasoningDeltaFallsBackToDelta(t *testing.T) {
	translator := newTestTranslator()
	chunks := translator.parseLine(`{"type":"reasoning-delta","delta":"thinking via delta"}`)
	require.Len(t, chunks, 1)
	require.NotNil(t, chunks[0].Choices[0].Delta.ReasoningContent)
	assert.Equal(t, "thinking via delta", *chunks[0].Choices[0].Delta.ReasoningContent)
	assert.Equal(t, "thinking via delta", translator.reasoning.String())
}

func TestFinishStepIsNotTerminal(t *testing.T) {
	translator := newTestTranslator()
	translator.parseLine(`{"type":"finish-step","finishReason":"stop"}`)
	assert.False(t, translator.sawFinish, "finish-step must not mark the stream complete")
	assert.Contains(t, translator.incompleteDetail(), "no finish event")

	translator.parseLine(`{"type":"finish","finishReason":"stop"}`)
	assert.True(t, translator.sawFinish)
	assert.Empty(t, translator.incompleteDetail())
}

func TestFinishWithoutReasonIsNormalCompletion(t *testing.T) {
	translator := newTestTranslator()
	translator.parseLine(`{"type":"finish"}`)
	assert.True(t, translator.sawFinish)
	assert.Empty(t, translator.incompleteDetail())

	translator = newTestTranslator()
	translator.parseLine(`{"type":"finish","finishReason":"other"}`)
	assert.True(t, translator.sawFinish)
	assert.Empty(t, translator.incompleteDetail())

	translator = newTestTranslator()
	translator.parseLine(`{"type":"finish","finishReason":"network-error"}`)
	assert.Equal(t, "upstream_error", translator.finishReason)
	assert.NotEmpty(t, translator.incompleteDetail())
}

func TestTranslatorRestoresClientToolNameFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	request := &dto.GeneralOpenAIRequest{
		Tools: []dto.ToolCallRequest{{
			Type:     "function",
			Function: dto.FunctionRequest{Name: "bash_output"},
		}},
	}
	reverse := clientToolNameReverseMap(request)
	require.Equal(t, "bash_output", reverse["shell_output"])
	c.Set(toolNameReverseContextKey, reverse)

	translator := newTranslator(c, Model)
	chunks := translator.parseLine(`{"type":"tool-call","toolCallId":"c1","toolName":"shell_output","input":{}}`)
	require.Len(t, chunks, 1)
	assert.Equal(t, "bash_output", chunks[0].Choices[0].Delta.ToolCalls[0].Function.Name)
}

func TestClientToolNameReverseMapFirstDeclarationWins(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Tools: []dto.ToolCallRequest{
			{Type: "function", Function: dto.FunctionRequest{Name: "bash_output"}},
			{Type: "function", Function: dto.FunctionRequest{Name: "task_output"}},
		},
	}
	reverse := clientToolNameReverseMap(request)
	assert.Equal(t, "bash_output", reverse["shell_output"])
}

func TestClientToolNameReverseMapIncludesToolChoiceOnly(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		ToolChoice: map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "read_multiple_files"},
		},
	}
	reverse := clientToolNameReverseMap(request)
	require.Equal(t, "read_multiple_files", reverse["read_file"])
}

func TestParseLineStringToolArguments(t *testing.T) {
	translator := newTestTranslator()
	chunks := translator.parseLine(`{"type":"tool-call","toolCallId":"c","toolName":"f","input":"{\"x\":1}"}`)
	require.Len(t, chunks, 1)
	assert.Equal(t, `{"x":1}`, chunks[0].Choices[0].Delta.ToolCalls[0].Function.Arguments)
}

func TestParseLineUsageZeroOutputNormalizesInput(t *testing.T) {
	translator := newTestTranslator()
	translator.parseLine(`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":100,"outputTokens":0,"cachedInputTokens":80}}`)
	require.NotNil(t, translator.usage)
	assert.Equal(t, 0, translator.usage.PromptTokens)
	assert.Equal(t, 0, translator.usage.CompletionTokens)
	assert.Equal(t, 0, translator.usage.PromptTokensDetails.CachedTokens)
}

func TestParseLineMapsLengthFinishReason(t *testing.T) {
	translator := newTestTranslator()
	translator.parseLine(`{"type":"finish","finishReason":"max_output_tokens","rawFinishReason":"max_output_tokens"}`)
	assert.Equal(t, "length", translator.finishReason)
	assert.Equal(t, "length", toOpenAIFinishReason(translator.finishReason))
}

func TestParseLinePauseTurnBecomesLength(t *testing.T) {
	translator := newTestTranslator()
	translator.parseLine(`{"type":"finish","finishReason":"pause_turn"}`)
	assert.Equal(t, "pause_turn", translator.finishReason)
	assert.Equal(t, "length", toOpenAIFinishReason(translator.finishReason))
}

func TestParseLineErrorEventMapsStatus(t *testing.T) {
	translator := newTestTranslator()
	translator.parseLine(`{"type":"error","error":{"message":"<429> slow down","code":"RATE_LIMITED","statusCode":429,"isRetryable":true}}`)
	require.NotNil(t, translator.upstreamErr)
	assert.Equal(t, 429, translator.upstreamErr.StatusCode)
	assert.Equal(t, "rate_limit_error", translator.upstreamErr.ToOpenAIError().Type)
}

func TestParseLinePaymentRequiredBecomesRateLimited(t *testing.T) {
	translator := newTestTranslator()
	translator.parseLine(`{"type":"error","error":{"message":"quota exhausted","statusCode":402}}`)
	require.NotNil(t, translator.upstreamErr)
	// Downstream status stays 429, but the raw upstream status is preserved so
	// pool-account classification still sees 402 (no_quota).
	assert.Equal(t, 429, translator.upstreamErr.StatusCode)
	assert.Equal(t, 402, translator.upstreamErr.SourceStatusCode)
	assert.Equal(t, "rate_limit_error", translator.upstreamErr.ToOpenAIError().Type)
}

func TestStatusFromMessageFormats(t *testing.T) {
	cases := map[string]int{
		"<429> slow down":                 429,
		"429 Too Many Requests":           429,
		"status 402":                      402,
		"status code: 401":                401,
		"HTTP 401 Unauthorized":           401,
		"random message without a status": 0,
		"version 4.1.2":                   0,
	}
	for message, want := range cases {
		assert.Equal(t, want, statusFromMessage(message), message)
	}
}

func newStreamTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c, recorder
}

func newStreamTestInfo(includeUsage bool) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		IsStream:           true,
		ShouldIncludeUsage: includeUsage,
		OriginModelName:    Model,
		ChannelMeta:        &relaycommon.ChannelMeta{UpstreamModelName: Model},
	}
}

func ndjsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func ndjsonLines(lines ...string) string {
	sep := string([]byte{10})
	return strings.Join(lines, sep) + sep
}

func TestHandleStreamMarksFailureAfterOutput(t *testing.T) {
	body := ndjsonLines(
		`{"type":"text-delta","text":"Hello"}`,
		`{"type":"error","error":{"message":"quota exhausted","statusCode":402}}`,
	)
	c, recorder := newStreamTestContext()
	a := &Adaptor{}

	usage, err := a.handleStream(c, ndjsonResponse(body), newStreamTestInfo(false))
	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), `"error"`)

	streamErr, ok := common.GetContextKeyType[*types.NewAPIError](c, constant.ContextKeyCommandGoStreamFailure)
	require.True(t, ok)
	require.NotNil(t, streamErr)
	assert.Equal(t, 402, streamErr.SourceStatusCode)
}

func TestHandleStreamMarksTruncationAfterOutput(t *testing.T) {
	body := ndjsonLines(`{"type":"text-delta","text":"partial"}`)
	c, recorder := newStreamTestContext()
	a := &Adaptor{}

	usage, err := a.handleStream(c, ndjsonResponse(body), newStreamTestInfo(false))
	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), "truncated")

	streamErr, ok := common.GetContextKeyType[*types.NewAPIError](c, constant.ContextKeyCommandGoStreamFailure)
	require.True(t, ok)
	require.NotNil(t, streamErr)
}

func TestHandleStreamUsageFrameHonorsIncludeUsage(t *testing.T) {
	body := ndjsonLines(
		`{"type":"text-delta","text":"Hello"}`,
		`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":10,"outputTokens":5}}`,
	)

	c, recorder := newStreamTestContext()
	_, err := (&Adaptor{}).handleStream(c, ndjsonResponse(body), newStreamTestInfo(false))
	require.Nil(t, err)
	assert.NotContains(t, recorder.Body.String(), `"usage":{"prompt_tokens"`)

	c, recorder = newStreamTestContext()
	_, err = (&Adaptor{}).handleStream(c, ndjsonResponse(body), newStreamTestInfo(true))
	require.Nil(t, err)
	assert.Contains(t, recorder.Body.String(), `"usage":{"prompt_tokens"`)
}

func TestParseLineIgnoresDoneAndBlank(t *testing.T) {
	translator := newTestTranslator()
	assert.Empty(t, translator.parseLine("[DONE]"))
	assert.Empty(t, translator.parseLine("   "))
	assert.Empty(t, translator.parseLine(": keep-alive"))
}

func TestHandleStreamEmptyOutputBeforeStart(t *testing.T) {
	body := ndjsonLines(`{"type":"finish","finishReason":"stop"}`)
	c, _ := newStreamTestContext()

	usage, err := (&Adaptor{}).handleStream(c, ndjsonResponse(body), newStreamTestInfo(false))
	require.Nil(t, usage)
	require.NotNil(t, err)
	assert.Equal(t, "Empty response from upstream (zero output tokens)", err.Error())
}
