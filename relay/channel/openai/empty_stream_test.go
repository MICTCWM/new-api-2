package openai

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withStreamingTimeout(t *testing.T) {
	t.Helper()
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
}

func TestOaiResponsesStreamHandlerEmptyBeforeCommitReturns502(t *testing.T) {
	withStreamingTimeout(t)
	body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_empty\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0,\"total_tokens\":5}}}\n\ndata: [DONE]\n\n"
	c, recorder, resp, info := newOpenAIPlanTestContext(t, body, true, types.RelayFormatOpenAIResponses)

	usage, err := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.False(t, types.IsSkipRetryError(err))
	assert.False(t, c.Writer.Written())
	assert.Empty(t, recorder.Body.String(), "the terminal response.completed event must not be forwarded before the empty check")
}

func TestOaiResponsesStreamHandlerEmptyAfterCommitSuppressesCompleted(t *testing.T) {
	withStreamingTimeout(t)
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_empty\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_empty\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0,\"total_tokens\":5}}}\n\n" +
		"data: [DONE]\n\n"
	c, recorder, resp, info := newOpenAIPlanTestContext(t, body, true, types.RelayFormatOpenAIResponses)

	usage, err := OaiResponsesStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.True(t, types.IsSkipRetryError(err))

	got := recorder.Body.String()
	assert.Contains(t, got, "event: error")
	assert.Contains(t, got, `"error"`)
	assert.NotContains(t, got, `"type":"response.completed"`)
	assert.NotContains(t, got, "[DONE]")
}

func TestOaiStreamHandlerEmptyUpstreamBeforeCommitReturns502(t *testing.T) {
	withStreamingTimeout(t)
	body := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	c, recorder, resp, info := newOpenAIPlanTestContext(t, body, true, types.RelayFormatOpenAI)

	usage, err := OaiStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.False(t, types.IsSkipRetryError(err))
	assert.Empty(t, recorder.Body.String())
	assert.False(t, c.Writer.Written())
}

func TestOaiStreamHandlerEmptyUpstreamAfterCommitSendsSSEError(t *testing.T) {
	withStreamingTimeout(t)
	body := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	c, recorder, resp, info := newOpenAIPlanTestContext(t, body, true, types.RelayFormatOpenAI)

	usage, err := OaiStreamHandler(c, info, resp)
	require.Nil(t, usage)
	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	assert.True(t, types.IsSkipRetryError(err))

	got := recorder.Body.String()
	assert.Contains(t, got, `"error"`)
	assert.NotContains(t, got, "[DONE]")
	assert.NotContains(t, got, `"usage"`)
	assert.NotContains(t, got, `"finish_reason":"stop"`)
}
