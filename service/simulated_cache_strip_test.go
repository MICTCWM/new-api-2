package service

import (
	"encoding/json"
	"testing"

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
