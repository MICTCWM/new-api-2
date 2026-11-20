package openai

import (
	"io"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func fakeSSEBody(body string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(body))
}

func TestOaiChatBufferedStreamHandlerAggregatesToolsAndUsage(t *testing.T) {
	c, recorder, resp, info := newOpenAIPlanTestContext(t, "", true, types.RelayFormatOpenAI)
	info.FakeNonStream = true
	info.IsStream = true
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Body = fakeSSEBody(`data: {"id":"chatcmpl_fake","object":"chat.completion.chunk","created":1730000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"he"}}]}

data: {"id":"chatcmpl_fake","object":"chat.completion.chunk","created":1730000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"llo","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}

data: {"id":"chatcmpl_fake","object":"chat.completion.chunk","created":1730000000,"model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":321,"completion_tokens":12,"total_tokens":333,"prompt_tokens_details":{"cached_tokens":100,"cached_creation_tokens":200},"claude_cache_creation_1_h_tokens":200}}

data: [DONE]

`)

	usage, apiErr := OaiChatBufferedStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	body := recorder.Body.Bytes()
	assert.Equal(t, "hello", gjson.GetBytes(body, "choices.0.message.content").String())
	assert.Equal(t, "lookup", gjson.GetBytes(body, "choices.0.message.tool_calls.0.function.name").String())
	assert.Equal(t, `{"q":"x"}`, gjson.GetBytes(body, "choices.0.message.tool_calls.0.function.arguments").String())
	assert.Equal(t, int64(321), gjson.GetBytes(body, "usage.prompt_tokens").Int())
	assert.Equal(t, int64(12), gjson.GetBytes(body, "usage.completion_tokens").Int())
	assert.Equal(t, int64(200), gjson.GetBytes(body, "usage.claude_cache_creation_1_h_tokens").Int())
	assert.Equal(t, 200, usage.ClaudeCacheCreation1hTokens)
}

func TestOaiResponsesBufferedStreamHandlerUsesCompletedResponse(t *testing.T) {
	c, recorder, resp, info := newOpenAIPlanTestContext(t, "", true, types.RelayFormatOpenAI)
	info.FakeNonStream = true
	info.RelayMode = relayconstant.RelayModeResponses
	info.IsStream = true
	resp.Header.Set("Content-Type", "text/event-stream")

	completed := &dto.OpenAIResponsesResponse{
		ID:     "resp_fake",
		Object: "response",
		Status: []byte(`"completed"`),
		Model:  "gpt-test",
		Output: []dto.ResponsesOutput{{
			Type:   "message",
			ID:     "msg_1",
			Status: "completed",
			Role:   "assistant",
			Content: []dto.ResponsesOutputContent{{
				Type: "output_text",
				Text: "hello",
			}},
		}},
		Usage: &dto.Usage{InputTokens: 500, OutputTokens: 20, TotalTokens: 520},
	}
	body, err := common.Marshal(completed)
	require.NoError(t, err)
	resp.Body = fakeSSEBody(`data: {"type":"response.created","response":{"id":"resp_fake","object":"response","status":"in_progress"}}

data: {"type":"response.completed","response":` + string(body) + `}

data: [DONE]

`)

	usage, apiErr := OaiResponsesBufferedStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 500, usage.PromptTokens)
	assert.Equal(t, 20, usage.CompletionTokens)

	out := recorder.Body.Bytes()
	assert.Equal(t, "resp_fake", gjson.GetBytes(out, "id").String())
	assert.Equal(t, "hello", gjson.GetBytes(out, "output.0.content.0.text").String())
	assert.Equal(t, int64(500), gjson.GetBytes(out, "usage.input_tokens").Int())
}
