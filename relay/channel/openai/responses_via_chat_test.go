package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// commandThinkingChatSSE is an upstream chat SSE stream that first streams the
// chain of thought as delta.reasoning_content and then the visible answer.
const commandThinkingChatSSE = `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"secret-thought"}}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"hello"}}]}

data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}

data: [DONE]
`

// commandThinkingChatJSON is the non-streaming chat body equivalent.
const commandThinkingChatJSON = `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello","reasoning_content":"secret-thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`

// newCommandResponsesTestContext builds the shared responses-via-chat fixture and
// additionally sets the Command channel context keys that drive the hide logic.
func newCommandResponsesTestContext(t *testing.T, body string, isStream bool, hideThinking bool) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	c, recorder, resp, info := newResponsesChatTestContext(t, body, isStream)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeCommand)
	if hideThinking {
		common.SetContextKey(c, constant.ContextKeyCommandHideThinking, true)
	}
	return c, recorder, resp, info
}

type sseFrame struct {
	Event string
	Data  string
}

func parseSSEFrames(body string) []sseFrame {
	var frames []sseFrame
	event := ""
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			frames = append(frames, sseFrame{Event: event, Data: strings.TrimSpace(strings.TrimPrefix(line, "data:"))})
			event = ""
		}
	}
	return frames
}

// assertNoReasoningOutput asserts the Responses body carries no reasoning output
// item. (The top-level "reasoning" field is always serialized as null by the
// response object, so it cannot be used as a leak signal.)
func assertNoReasoningOutput(t *testing.T, body string) {
	t.Helper()
	output := gjson.Get(body, "output")
	require.True(t, output.IsArray())
	for _, item := range output.Array() {
		assert.NotEqual(t, "reasoning", item.Get("type").String())
	}
}

func reasoningEventTypes() []string {
	return []string{
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.reasoning_text.delta",
		"response.reasoning_text.done",
	}
}

// stripChatChunkReasoning must clear both reasoning_content and reasoning so the
// shared chat→Responses converter finds no reasoning to emit.
func TestStripChatChunkReasoningClearsBothFields(t *testing.T) {
	chunk := &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: ptrString("a"), Reasoning: ptrString("b"), Content: ptrString("hello")}},
		},
	}

	stripChatChunkReasoning(chunk)

	require.Nil(t, chunk.Choices[0].Delta.ReasoningContent)
	require.Nil(t, chunk.Choices[0].Delta.Reasoning)
	require.Equal(t, "", chunk.Choices[0].Delta.GetReasoningContent())
	require.Equal(t, "hello", chunk.Choices[0].Delta.GetContentString())
}

// When the Command channel hides thinking, the streaming chat→Responses handler
// must drop reasoning before conversion so no reasoning events are produced.
func TestOaiChatToResponsesStreamHandlerHidesCommandThinking(t *testing.T) {
	c, recorder, resp, info := newCommandResponsesTestContext(t, commandThinkingChatSSE, true, true)

	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Equal(t, 3, usage.TotalTokens)

	body := recorder.Body.String()
	frames := parseSSEFrames(body)
	require.NotEmpty(t, frames)

	for _, frame := range frames {
		assert.NotContains(t, reasoningEventTypes(), frame.Event, "unexpected reasoning event emitted: %q", frame.Event)
	}
	assert.NotContains(t, body, "secret-thought")

	var textDelta string
	var completed string
	for _, frame := range frames {
		switch frame.Event {
		case "response.output_text.delta":
			textDelta = gjson.Get(frame.Data, "delta").String()
		case "response.completed":
			completed = frame.Data
		}
	}
	assert.Equal(t, "hello", textDelta)
	require.NotEmpty(t, completed)

	output := gjson.Get(completed, "response.output")
	require.True(t, output.IsArray())
	require.Len(t, output.Array(), 1)
	assert.Equal(t, "message", gjson.Get(completed, "response.output.0.type").String())
	assert.Equal(t, "hello", gjson.Get(completed, "response.output.0.content.0.text").String())
}

// Control: without the hide marker the reasoning is converted as usual, so the
// reasoning events must still be present (the strip only fires for hide).
func TestOaiChatToResponsesStreamHandlerKeepsReasoningWithoutHide(t *testing.T) {
	c, recorder, resp, info := newCommandResponsesTestContext(t, commandThinkingChatSSE, true, false)

	usage, apiErr := OaiChatToResponsesStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	body := recorder.Body.String()
	frames := parseSSEFrames(body)

	var events []string
	var reasoningDelta string
	var completed string
	for _, frame := range frames {
		events = append(events, frame.Event)
		switch frame.Event {
		case "response.reasoning_summary_text.delta":
			reasoningDelta = gjson.Get(frame.Data, "delta").String()
		case "response.completed":
			completed = frame.Data
		}
	}
	assert.Contains(t, events, "response.reasoning_summary_text.delta")
	assert.Equal(t, "secret-thought", reasoningDelta)
	require.NotEmpty(t, completed)
	assert.Equal(t, "reasoning", gjson.Get(completed, "response.output.0.type").String())
	assert.Equal(t, "secret-thought", gjson.Get(completed, "response.output.0.content.0.text").String())
}

// The non-streaming chat→Responses handler must also drop reasoning when hiding.
// The converter emits the message item before the reasoning item, so a hidden
// response has only the message.
func TestOaiChatToResponsesHandlerHidesCommandThinking(t *testing.T) {
	c, recorder, resp, info := newCommandResponsesTestContext(t, commandThinkingChatJSON, false, true)

	usage, apiErr := OaiChatToResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	body := recorder.Body.String()
	output := gjson.Get(body, "output")
	require.True(t, output.IsArray())
	require.Len(t, output.Array(), 1)
	assert.Equal(t, "message", gjson.Get(body, "output.0.type").String())
	assert.Equal(t, "hello", gjson.Get(body, "output.0.content.0.text").String())
	assert.NotContains(t, body, "secret-thought")
	assertNoReasoningOutput(t, body)
}

// Control for the non-streaming handler: reasoning survives without hiding.
func TestOaiChatToResponsesHandlerKeepsReasoningWithoutHide(t *testing.T) {
	c, recorder, resp, info := newCommandResponsesTestContext(t, commandThinkingChatJSON, false, false)

	usage, apiErr := OaiChatToResponsesHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	body := recorder.Body.String()
	require.Len(t, gjson.Get(body, "output").Array(), 2)
	assert.Equal(t, "reasoning", gjson.Get(body, "output.1.type").String())
	assert.Equal(t, "secret-thought", gjson.Get(body, "output.1.content.0.text").String())
}

// The buffered handler (client asked non-streaming, upstream streamed anyway)
// shares the same converter and must hide reasoning the same way.
func TestOaiChatToResponsesBufferedStreamHandlerHidesCommandThinking(t *testing.T) {
	c, recorder, resp, info := newCommandResponsesTestContext(t, commandThinkingChatSSE, false, true)

	usage, apiErr := OaiChatToResponsesBufferedStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)

	body := recorder.Body.String()
	output := gjson.Get(body, "output")
	require.True(t, output.IsArray())
	require.Len(t, output.Array(), 1)
	assert.Equal(t, "message", gjson.Get(body, "output.0.type").String())
	assert.Equal(t, "hello", gjson.Get(body, "output.0.content.0.text").String())
	assert.NotContains(t, body, "secret-thought")
	assertNoReasoningOutput(t, body)
}

func ptrString(s string) *string {
	return &s
}
