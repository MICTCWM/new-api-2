package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripSimulatedCacheTTLFromJSON_Chat(t *testing.T) {
	input := []byte(`{
		"model":"kimi-k3",
		"prompt_cache_options":{"mode":"implicit","ttl":"1h"},
		"prompt_cache_retention":"24h",
		"temperature":0.7,
		"messages":[
			{"role":"system","content":"stable"},
			{"role":"user","content":[{"type":"text","text":"<hi>","cache_control":{"type":"ephemeral","ttl":"5m"}}],"cache_control":{"type":"ephemeral","ttl":"1h"}}
		]
	}`)

	out, changed, err := StripSimulatedCacheTTLFromJSON(input)
	require.NoError(t, err)
	require.True(t, changed)

	var body map[string]any
	require.NoError(t, json.Unmarshal(out, &body))
	assert.NotContains(t, body, "prompt_cache_options")
	assert.NotContains(t, body, "prompt_cache_retention")
	assert.Equal(t, "kimi-k3", body["model"])
	assert.Equal(t, 0.7, body["temperature"])

	messages := body["messages"].([]any)
	user := messages[1].(map[string]any)
	assert.NotContains(t, user, "cache_control")
	content := user["content"].([]any)
	part := content[0].(map[string]any)
	assert.NotContains(t, part, "cache_control")
	assert.Equal(t, "<hi>", part["text"])
}

func TestStripSimulatedCacheTTLFromJSON_Claude(t *testing.T) {
	input := []byte(`{
		"model":"claude-sonnet",
		"cache_control":{"type":"ephemeral","ttl":"1h"},
		"system":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"tools":[{"name":"lookup","description":"find","cache_control":{"type":"ephemeral","ttl":"5m"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]
	}`)

	out, changed, err := StripSimulatedCacheTTLFromJSON(input)
	require.NoError(t, err)
	require.True(t, changed)

	var body map[string]any
	require.NoError(t, json.Unmarshal(out, &body))
	assert.NotContains(t, body, "cache_control")
	assert.NotContains(t, body["system"].([]any)[0].(map[string]any), "cache_control")
	assert.NotContains(t, body["tools"].([]any)[0].(map[string]any), "cache_control")
	message := body["messages"].([]any)[0].(map[string]any)
	assert.NotContains(t, message, "cache_control")
	assert.NotContains(t, message["content"].([]any)[0].(map[string]any), "cache_control")
}

func TestStripSimulatedCacheTTLFromJSON_Noop(t *testing.T) {
	input := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`)
	out, changed, err := StripSimulatedCacheTTLFromJSON(input)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, input, out)
}

func TestStripSimulatedCacheTTLFromJSON_Invalid(t *testing.T) {
	_, _, err := StripSimulatedCacheTTLFromJSON([]byte(`{"messages":[`))
	require.Error(t, err)
}

func TestBuildSimulatedCachePassThroughBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	input := []byte(`{"model":"kimi-k3","prompt_cache_options":{"mode":"implicit","ttl":"1h"},"messages":[{"role":"user","content":"hi"}]}`)
	newContext := func(t *testing.T) (*gin.Context, *relaycommon.RelayInfo) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(input))
		storage, err := common.CreateBodyStorage(input)
		require.NoError(t, err)
		c.Set(common.KeyBodyStorage, storage)
		t.Cleanup(func() { common.CleanupBodyStorage(c) })
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
		return c, info
	}

	t.Run("enabled strips TTL", func(t *testing.T) {
		c, info := newContext(t)
		info.ChannelOtherSettings.SimulatedCacheCreationEnabled = true
		body, closer, err := BuildSimulatedCachePassThroughBody(c, info)
		require.NoError(t, err)
		if closer != nil {
			defer closer()
		}
		out, err := io.ReadAll(body)
		require.NoError(t, err)
		assert.NotContains(t, string(out), "prompt_cache_options")
		assert.Contains(t, string(out), `"model":"kimi-k3"`)
	})

	t.Run("disabled keeps original", func(t *testing.T) {
		c, info := newContext(t)
		body, closer, err := BuildSimulatedCachePassThroughBody(c, info)
		require.NoError(t, err)
		if closer != nil {
			defer closer()
		}
		out, err := io.ReadAll(body)
		require.NoError(t, err)
		assert.JSONEq(t, string(input), string(out))
	})
}
