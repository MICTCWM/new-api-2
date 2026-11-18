package common

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceResponsesUsageRawJSON(t *testing.T) {
	body := []byte(`{"id":"resp_1","usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":10,"text_tokens":80},"output_tokens":20,"total_tokens":120},"x-unknown":{"a":1}}`)
	usage := &dto.Usage{
		InputTokens:  100,
		OutputTokens: 20,
		TotalTokens:  120,
		InputTokensDetails: &dto.InputTokenDetails{
			CachedTokens:         60,
			CacheWriteTokens:     40,
			CachedCreationTokens: 40,
			TextTokens:           80,
		},
	}
	out, err := ReplaceResponsesUsageRawJSON(body, usage)
	require.NoError(t, err)

	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &root))
	assert.Contains(t, string(root["x-unknown"]), `"a":1`)

	var got struct {
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
				TextTokens       int `json:"text_tokens"`
			} `json:"input_tokens_details"`
			PromptTokens json.RawMessage `json:"prompt_tokens"`
		} `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, 100, got.Usage.InputTokens)
	assert.Equal(t, 20, got.Usage.OutputTokens)
	assert.Equal(t, 120, got.Usage.TotalTokens)
	assert.Equal(t, 60, got.Usage.InputTokensDetails.CachedTokens)
	assert.Equal(t, 40, got.Usage.InputTokensDetails.CacheWriteTokens)
	assert.Equal(t, 80, got.Usage.InputTokensDetails.TextTokens)
	assert.Empty(t, got.Usage.PromptTokens, "Responses usage must not gain Chat prompt_tokens")
}

func TestReplaceResponsesEventUsageRawJSON(t *testing.T) {
	data := `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":10,"cache_write_tokens":0},"output_tokens":20,"total_tokens":120},"output":[]},"sequence_number":4}`
	usage := &dto.Usage{
		InputTokens:  100,
		OutputTokens: 20,
		TotalTokens:  120,
		InputTokensDetails: &dto.InputTokenDetails{
			CachedTokens:     70,
			CacheWriteTokens: 30,
		},
	}
	out, err := ReplaceResponsesEventUsageRawJSON(data, usage)
	require.NoError(t, err)

	var event struct {
		Type     string `json:"type"`
		Sequence int    `json:"sequence_number"`
		Response struct {
			ID    string `json:"id"`
			Usage struct {
				InputTokens        int `json:"input_tokens"`
				InputTokensDetails struct {
					CachedTokens     int `json:"cached_tokens"`
					CacheWriteTokens int `json:"cache_write_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &event))
	assert.Equal(t, "response.completed", event.Type)
	assert.Equal(t, 4, event.Sequence)
	assert.Equal(t, "resp_1", event.Response.ID)
	assert.Equal(t, 70, event.Response.Usage.InputTokensDetails.CachedTokens)
	assert.Equal(t, 30, event.Response.Usage.InputTokensDetails.CacheWriteTokens)
}

func TestReplaceResponsesUsageRawJSON_NoUsageKeepsBody(t *testing.T) {
	body := []byte(`{"id":"resp_1"}`)
	out, err := ReplaceResponsesUsageRawJSON(body, &dto.Usage{InputTokens: 1})
	require.NoError(t, err)
	assert.JSONEq(t, string(body), string(out))
}

func TestReplaceResponsesUsageRawJSONCleanDropsChatFields(t *testing.T) {
	body := []byte(`{"id":"x","usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":4},"billing_usage":{"source":"oai_chat"}}}`)
	usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
	usage.InputTokens = 10
	usage.OutputTokens = 2
	usage.InputTokensDetails = &dto.InputTokenDetails{CachedTokens: 3, CacheWriteTokens: 4}
	got, err := ReplaceResponsesUsageRawJSONClean(body, usage)
	require.NoError(t, err)
	require.NotContains(t, string(got), "prompt_tokens")
	require.NotContains(t, string(got), "billing_usage")
	var obj map[string]any
	require.NoError(t, json.Unmarshal(got, &obj))
	u := obj["usage"].(map[string]any)
	require.Equal(t, float64(10), u["input_tokens"])
	details := u["input_tokens_details"].(map[string]any)
	require.Equal(t, float64(3), details["cached_tokens"])
	require.Equal(t, float64(4), details["cache_write_tokens"])
}
