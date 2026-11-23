package helper

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupEmptyStreamTest(t *testing.T, body io.Reader) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(body),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{},
		RelayFormat: types.RelayFormatOpenAI,
	}

	return c, recorder, resp, info
}

func TestStreamScannerHandler_EmptyUpstreamReturns502(t *testing.T) {
	t.Parallel()

	c, recorder, resp, info := setupEmptyStreamTest(t, strings.NewReader(""))

	err := StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		t.Fatalf("data handler must not be called for an empty body: %q", data)
	})

	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.True(t, info.StreamStatus.IsEmptyResponse())
	assert.False(t, c.Writer.Written())
	assert.Empty(t, c.Writer.Header().Get("Content-Type"))
	assert.Empty(t, recorder.Body.String())
}

func TestStreamScannerHandler_EmptyAfterCommittedSendsSSEError(t *testing.T) {
	t.Parallel()

	body := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: [DONE]\n\n"
	c, recorder, resp, info := setupEmptyStreamTest(t, strings.NewReader(body))

	err := StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		require.NoError(t, StringData(c, data))
	})

	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.True(t, types.IsSkipRetryError(err), "committed stream cannot be retried")
	assert.True(t, info.StreamStatus.IsEmptyResponse())
	assert.True(t, c.Writer.Written())
	assert.Contains(t, recorder.Body.String(), `"error"`)
	assert.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestStreamScannerHandler_ReasoningOnlyIsNotEmpty(t *testing.T) {
	t.Parallel()

	body := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\ndata: [DONE]\n\n"
	c, _, resp, info := setupEmptyStreamTest(t, strings.NewReader(body))

	err := StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		require.NoError(t, StringData(c, data))
	})

	require.Nil(t, err)
	assert.True(t, info.StreamStatus.HasReasoningDelta())
	assert.False(t, info.StreamStatus.HasContentDelta())
	assert.False(t, info.StreamStatus.IsEmptyResponse())
}

func TestClassifyStreamPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		payload       string
		wantContent   bool
		wantReasoning bool
	}{
		{
			name:        "openai content delta",
			payload:     `{"choices":[{"delta":{"content":"hello"}}]}`,
			wantContent: true,
		},
		{
			name:          "openai reasoning delta",
			payload:       `{"choices":[{"delta":{"reasoning_content":"plan"}}]}`,
			wantReasoning: true,
		},
		{
			name:        "openai tool call delta",
			payload:     `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"lookup","arguments":"{}"}}]}}]}`,
			wantContent: true,
		},
		{
			name:    "openai role only",
			payload: `{"choices":[{"delta":{"role":"assistant"}}]}`,
		},
		{
			name:    "openai finish only",
			payload: `{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		},
		{
			name:    "usage only",
			payload: `{"usage":{"prompt_tokens":10,"completion_tokens":0}}`,
		},
		{
			name:        "claude text delta",
			payload:     `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`,
			wantContent: true,
		},
		{
			name:          "claude thinking delta",
			payload:       `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"plan"}}`,
			wantReasoning: true,
		},
		{
			name:        "claude tool use start",
			payload:     `{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool_1","name":"lookup"}}`,
			wantContent: true,
		},
		{
			name:        "responses text delta",
			payload:     `{"type":"response.output_text.delta","delta":"hello"}`,
			wantContent: true,
		},
		{
			name:          "responses reasoning delta",
			payload:       `{"type":"response.reasoning_summary_text.delta","delta":"plan"}`,
			wantReasoning: true,
		},
		{
			name:        "responses completed with output",
			payload:     `{"type":"response.completed","response":{"output":[{"type":"message"}]}}`,
			wantContent: true,
		},
		{
			name:        "gemini text part",
			payload:     `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`,
			wantContent: true,
		},
		{
			name:        "gemini inline image part",
			payload:     `{"candidates":[{"content":{"parts":[{"inline_data":{"mime_type":"image/png","data":"iVBORw0KGgo="}}]}}]}`,
			wantContent: true,
		},
		{
			name:        "gemini function call part",
			payload:     `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"city":"Paris"}}}]}}]}`,
			wantContent: true,
		},
		{
			name:        "non json payload is opaque output",
			payload:     "not-json",
			wantContent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content, reasoning := classifyStreamPayload(tt.payload)
			assert.Equal(t, tt.wantContent, content)
			assert.Equal(t, tt.wantReasoning, reasoning)
		})
	}
}
