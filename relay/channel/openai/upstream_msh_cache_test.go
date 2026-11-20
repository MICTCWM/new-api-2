package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenaiHandlerNonStreamAppliesUpstreamMshCacheHeader(t *testing.T) {
	c, recorder, resp, info := newOpenAIPlanTestContext(t, openaiNonStreamBody, false, types.RelayFormatOpenAI)
	info.MshCacheWriteHeadersPresent = true
	info.MshCacheWrite1hTokens = 1000

	usage, err := OpenaiHandler(c, info, resp)
	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 1000, usage.ClaudeCacheCreation1hTokens)
	assert.Equal(t, 1000, usage.PromptTokensDetails.CachedCreationTokens)

	body := recorder.Body.Bytes()
	assert.Equal(t, int64(1000), gjson.GetBytes(body, "usage.claude_cache_creation_1_h_tokens").Int())
	assert.Equal(t, int64(1000), gjson.GetBytes(body, "usage.prompt_tokens_details.cached_creation_tokens").Int())
	// 非 usage 字段不应被整份重编码破坏。
	assert.Equal(t, "hello", gjson.GetBytes(body, "choices.0.message.content").String())
	assert.Equal(t, "oai_001", gjson.GetBytes(body, "vendor_meta.trace_id").String())
}
