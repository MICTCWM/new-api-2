package openai

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestOaiChatToResponsesStreamHandlerEmitsFailedTerminalOnMidStreamError guards
// the client contract that produced "stream disconnected before completion:
// stream closed before response.completed": once the downgrade stream has
// written events to the client (HTTP 200 committed), any mid-stream upstream
// error must still end with a terminal event. response.failed is emitted with
// the upstream error so codex/OpenAI SDK terminates cleanly and surfaces the
// real reason instead of a disconnect error.
func TestOaiChatToResponsesStreamHandlerEmitsFailedTerminalOnMidStreamError(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1710000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
		``,
		`data: {"error":{"message":"upstream exploded","type":"server_error","code":"internal_error"}}`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)

	usage, err := OaiChatToResponsesStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.NotNil(t, err)
	require.Equal(t, "upstream exploded", err.ToOpenAIError().Message)

	got := recorder.Body.String()
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, got, `event: response.created`)
	require.Contains(t, got, `event: response.output_text.delta`)
	require.Contains(t, got, `"delta":"partial"`)
	require.Contains(t, got, `event: response.failed`)
	require.Contains(t, got, `"type":"response.failed"`)
	require.Contains(t, got, `"status":"failed"`)
	require.Contains(t, got, `"upstream exploded"`)
	// The mid-stream failure must not claim a completed terminal event.
	require.NotContains(t, got, `event: response.completed`)
	requireOrderedSubstrings(t, got,
		`event: response.created`,
		`event: response.output_text.delta`,
		`event: response.output_text.done`,
		`event: response.output_item.done`,
		`event: response.failed`,
	)
}

// TestOaiChatToResponsesStreamHandlerEarlyErrorStaysHTTPError covers the other
// boundary: when the upstream error arrives before anything has been written to
// the client (HTTP 200 not yet committed), the handler must NOT start an SSE
// stream — returning the error lets the relay layer emit a proper non-200 error
// body, so no terminal event is needed.
func TestOaiChatToResponsesStreamHandlerEarlyErrorStaysHTTPError(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	body := strings.Join([]string{
		`data: {"error":{"message":"boom","type":"invalid_request_error","code":"bad_request"}}`,
		``,
	}, "\n")

	c, recorder, resp, info := newResponsesChatTestContext(t, body, true)

	usage, err := OaiChatToResponsesStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.NotNil(t, err)
	require.Equal(t, "boom", err.ToOpenAIError().Message)
	require.Empty(t, recorder.Body.String())
}
