package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// newSendStreamDataContext builds the minimal streaming context: a gin test
// context whose writer captures the emitted SSE frames, carrying the channel
// type and (optionally) the Command hide-thinking marker.
func newSendStreamDataContext(t *testing.T, channelType int, hideThinking bool) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeyChannelType, channelType)
	if hideThinking {
		common.SetContextKey(c, constant.ContextKeyCommandHideThinking, true)
	}
	return c, recorder
}

// emittedJSON returns the single JSON payload written by the stream helper.
func emittedJSON(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	payload := strings.TrimSpace(recorder.Body.String())
	payload = strings.TrimSpace(strings.TrimPrefix(payload, "data:"))
	require.True(t, gjson.Valid(payload), "expected a valid JSON payload, got %q", payload)
	return payload
}

func thinkingOnlyChunk() string {
	return `{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"secret-thought"}}]}`
}

// When the Command channel hides thinking, thinkToContent must be skipped so the
// reasoning stays in its own delta field and the outbound hook can strip it.
// Folding it into content as <think> text would leak the chain of thought.
func TestSendStreamDataSkipsThinkToContentWhenHiding(t *testing.T) {
	c, recorder := newSendStreamDataContext(t, constant.ChannelTypeCommand, true)
	info := &relaycommon.RelayInfo{
		ThinkingContentInfo: relaycommon.ThinkingContentInfo{IsFirstThinkingContent: true},
	}

	require.NoError(t, sendStreamData(c, info, thinkingOnlyChunk(), false, true))

	out := emittedJSON(t, recorder)
	assert.False(t, gjson.Get(out, "choices.0.delta.reasoning_content").Exists())
	assert.False(t, gjson.Get(out, "choices.0.delta.reasoning").Exists())
	assert.Equal(t, "", gjson.Get(out, "choices.0.delta.content").String())
	assert.NotContains(t, out, "secret-thought")
	// The conversion path was skipped entirely, so the thinking state is untouched.
	assert.True(t, info.ThinkingContentInfo.IsFirstThinkingContent)
	assert.False(t, info.ThinkingContentInfo.HasSentThinkingContent)
}

// The hide guard only fires for the Command channel with the marker; a Command
// request without it keeps the existing thinkToContent behavior exactly.
func TestSendStreamDataKeepsThinkToContentWithoutHideMarker(t *testing.T) {
	c, recorder := newSendStreamDataContext(t, constant.ChannelTypeCommand, false)
	info := &relaycommon.RelayInfo{
		ThinkingContentInfo: relaycommon.ThinkingContentInfo{IsFirstThinkingContent: true},
	}

	require.NoError(t, sendStreamData(c, info, thinkingOnlyChunk(), false, true))

	out := emittedJSON(t, recorder)
	assert.Equal(t, "<think>\nsecret-thought", gjson.Get(out, "choices.0.delta.content").String())
	assert.False(t, gjson.Get(out, "choices.0.delta.reasoning_content").Exists())
	assert.False(t, info.ThinkingContentInfo.IsFirstThinkingContent)
	assert.True(t, info.ThinkingContentInfo.HasSentThinkingContent)
}

// forceFormat=true routes through ObjectData; hiding must still leave the
// reasoning for the outbound hook to strip rather than folding it into content.
func TestSendStreamDataHidesThinkingWithForceFormat(t *testing.T) {
	c, recorder := newSendStreamDataContext(t, constant.ChannelTypeCommand, true)
	info := &relaycommon.RelayInfo{
		ThinkingContentInfo: relaycommon.ThinkingContentInfo{IsFirstThinkingContent: true},
	}

	require.NoError(t, sendStreamData(c, info, thinkingOnlyChunk(), true, true))

	out := emittedJSON(t, recorder)
	assert.False(t, gjson.Get(out, "choices.0.delta.reasoning_content").Exists())
	assert.Equal(t, "", gjson.Get(out, "choices.0.delta.content").String())
	assert.NotContains(t, out, "secret-thought")
	assert.True(t, info.ThinkingContentInfo.IsFirstThinkingContent)
}
