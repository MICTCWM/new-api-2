package claude

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHandleClaudeResponseDataNativeAppliesUpstreamMshHeader(t *testing.T) {
	c, recorder, info, claudeInfo := newClaudePlanTestContext(t, types.RelayFormatClaude)
	info.MshCacheWriteHeadersPresent = true
	info.MshCacheWrite1hTokens = 3584

	err := HandleClaudeResponseData(c, info, claudeInfo, nil, []byte(claudeNativeResponseBody))
	require.Nil(t, err)

	body := recorder.Body.String()
	require.NotEmpty(t, body)
	assert.EqualValues(t, 3584, gjson.Get(body, "usage.cache_creation_input_tokens").Int())
	assert.EqualValues(t, 3584, gjson.Get(body, "usage.cache_creation.ephemeral_1h_input_tokens").Int())
	assert.EqualValues(t, 3584, gjson.Get(body, "usage.claude_cache_creation_1_h_tokens").Int())
	assert.EqualValues(t, 0, gjson.Get(body, "usage.cache_creation.ephemeral_5m_input_tokens").Int())
	assert.Equal(t, "hello", gjson.Get(body, "content.0.text").String())

	assert.Equal(t, 3584, claudeInfo.Usage.ClaudeCacheCreation1hTokens)
	assert.Equal(t, 3584, claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens)
}
