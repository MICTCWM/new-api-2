package claude

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeBufferedStreamHandlerAggregatesContentAndUsage(t *testing.T) {
	c, recorder, info, _ := newClaudePlanTestContext(t, types.RelayFormatClaude)
	info.FakeNonStream = true
	info.IsStream = true
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(`event: message_start
data: {"type":"message_start","message":{"id":"msg_fake","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":400,"cache_creation_input_tokens":100,"cache_read_input_tokens":50,"output_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":100}}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":25}}

event: message_stop
data: {"type":"message_stop"}

`)),
	}

	usage, apiErr := ClaudeBufferedStreamHandler(c, resp, info)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 400, usage.PromptTokens)
	assert.Equal(t, 25, usage.CompletionTokens)
	assert.Equal(t, 100, usage.ClaudeCacheCreation1hTokens)

	body := recorder.Body.Bytes()
	assert.Equal(t, "hello", gjson.GetBytes(body, "content.0.text").String())
	assert.Equal(t, "lookup", gjson.GetBytes(body, "content.1.name").String())
	assert.Equal(t, `{"q":"x"}`, gjson.GetBytes(body, "content.1.input").Raw)
	assert.Equal(t, int64(400), gjson.GetBytes(body, "usage.input_tokens").Int())
	assert.Equal(t, int64(100), gjson.GetBytes(body, "usage.cache_creation_input_tokens").Int())
	assert.Equal(t, int64(100), gjson.GetBytes(body, "usage.cache_creation.ephemeral_1h_input_tokens").Int())
	assert.Equal(t, int64(25), gjson.GetBytes(body, "usage.output_tokens").Int())
}

func TestClaudeBufferedStreamHandlerEstimatesWhenUsageMissing(t *testing.T) {
	c, recorder, info, _ := newClaudePlanTestContext(t, types.RelayFormatClaude)
	info.FakeNonStream = true
	info.IsStream = true
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(`data: {"type":"message_start","message":{"id":"msg_no_usage","type":"message","role":"assistant","model":"claude-test","content":[]}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello world"}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

data: {"type":"message_stop"}

`)),
	}
	usage, apiErr := ClaudeBufferedStreamHandler(c, resp, info)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Greater(t, usage.CompletionTokens, 0)
	assert.NotEmpty(t, recorder.Body.Bytes())
}
