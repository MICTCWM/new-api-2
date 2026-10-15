// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Captured upstream bodies: the reverse-proxy channel (case A), the Aliyun
// backend (case B) and the NVIDIA backend (case C).
const caseAUpstreamResponse = `{
  "choices": [{
    "finish_reason": "stop",
    "index": 0,
    "message": {"content": "ZEBRA-4291", "role": "assistant"}
  }],
  "created": 1791901844,
  "id": "267b6d4bcbbd9c31e623739f7e7bdab3",
  "model": "glm-5.3",
  "object": "chat.completion",
  "usage": {
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens": 0,
    "cached_tokens": 0,
    "completion_thinking_tokens": 0,
    "completion_tokens": 9,
    "completion_tokens_details": {
      "accepted_prediction_tokens": 0,
      "audio_tokens": 0,
      "cached_tokens": 0,
      "reasoning_tokens": 0,
      "rejected_prediction_tokens": 0
    },
    "credit": 12.97,
    "prompt_cache_hit_tokens": 0,
    "prompt_cache_miss_tokens": 81049,
    "prompt_cache_write_tokens": 0,
    "prompt_tokens": 81049,
    "prompt_tokens_details": {
      "accepted_prediction_tokens": 0,
      "audio_tokens": 0,
      "cached_tokens": 0,
      "reasoning_tokens": 0
    },
    "total_tokens": 81058
  }
}`

const caseBUpstreamResponse = `{
  "choices": [{
    "finish_reason": "stop",
    "index": 0,
    "logprobs": null,
    "message": {"content": "Hi there! How can I help you today?", "role": "assistant"}
  }],
  "created": 1791902120,
  "id": "b54ea1e8-4dad-473d-9519-e12c976339dc",
  "model": "glm-5.3",
  "object": "chat.completion",
  "usage": {
    "completion_tokens": 11,
    "completion_tokens_details": {"reasoning_tokens": 0},
    "prompt_tokens": 21,
    "prompt_tokens_details": {"cached_tokens": 0},
    "total_tokens": 32
  },
  "provider": "ali********"
}`

const caseCUpstreamResponse = `{
  "id": "b7cd6f2e-f765-41a5-8a25-2f82102daf1e",
  "object": "chat.completion",
  "created": 1791902236,
  "model": "glm-5.3",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "ZEBRA-4291",
      "reasoning_content": "The user wants me to find and reply with the secret code word..."
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "completion_tokens": 53,
    "completion_tokens_details": {"reasoning_tokens": 45},
    "prompt_tokens": 81056,
    "prompt_tokens_details": {"cached_tokens": 0},
    "total_tokens": 81109
  },
  "provider": "nvi************"
}`

func requireNoInternalKeys(t *testing.T, data string) {
	t.Helper()
	_, found := findInternalKeyPath(gjson.Parse(data), "", true)
	require.False(t, found, "an internal key survived: %s", data)
}

func TestSanitizeInternalResponseFieldsCaseA(t *testing.T) {
	out, changed := sanitizeInternalResponseFields(caseAUpstreamResponse)
	require.True(t, changed)

	requireNoInternalKeys(t, out)
	require.Equal(t, "ZEBRA-4291", gjson.Get(out, "choices.0.message.content").String())
	require.Equal(t, float64(9), gjson.Get(out, "usage.completion_tokens").Float())
	require.Equal(t, float64(81049), gjson.Get(out, "usage.prompt_tokens").Float())
	require.Equal(t, float64(81058), gjson.Get(out, "usage.total_tokens").Float())
	// Official schema fields survive, including the nested usage details.
	require.Equal(t, float64(0), gjson.Get(out, "usage.prompt_tokens_details.cached_tokens").Float())
	require.Equal(t, float64(0), gjson.Get(out, "usage.completion_tokens_details.reasoning_tokens").Float())
	require.Equal(t, float64(0), gjson.Get(out, "usage.cached_tokens").Float())
	require.Equal(t, "chat.completion", gjson.Get(out, "object").String())
	require.Equal(t, "glm-5.3", gjson.Get(out, "model").String())
}

func TestSanitizeInternalResponseFieldsCaseBAndC(t *testing.T) {
	for _, upstream := range []string{caseBUpstreamResponse, caseCUpstreamResponse} {
		out, changed := sanitizeInternalResponseFields(upstream)
		require.True(t, changed)
		require.False(t, gjson.Get(out, "provider").Exists())
		requireNoInternalKeys(t, out)
	}

	// Case C keeps the reasoning chain the client SDK may need.
	out, _ := sanitizeInternalResponseFields(caseCUpstreamResponse)
	require.Equal(t, "The user wants me to find and reply with the secret code word...",
		gjson.Get(out, "choices.0.message.reasoning_content").String())
	require.Equal(t, float64(45), gjson.Get(out, "usage.completion_tokens_details.reasoning_tokens").Float())

	outB, _ := sanitizeInternalResponseFields(caseBUpstreamResponse)
	require.True(t, gjson.Get(outB, "choices.0.logprobs").Exists())
	require.Equal(t, "b54ea1e8-4dad-473d-9519-e12c976339dc", gjson.Get(outB, "id").String())
	require.Equal(t, float64(0), gjson.Get(outB, "usage.prompt_tokens_details.cached_tokens").Float())
}

func TestSanitizePrunesObjectsTheStripLeftEmpty(t *testing.T) {
	out, changed := sanitizeInternalResponseFields(`{"model":"m","internal":{"credit":1,"provider":"x"},"usage":{"credit":2}}`)
	require.True(t, changed)
	require.Equal(t, `{"model":"m"}`, out)
}

func TestSanitizeSplicesChoicesEmptiedByTheStrip(t *testing.T) {
	out, changed := sanitizeInternalResponseFields(`{"choices":[{"message":{"credit":1}},{"message":{"content":"keep"}}]}`)
	require.True(t, changed)
	require.Equal(t, `{"choices":[{"message":{"content":"keep"}}]}`, out)

	out, changed = sanitizeInternalResponseFields(`{"choices":[{"credit":1}]}`)
	require.True(t, changed)
	// An emptied array is kept as [] rather than dropped: arrays are never pruned.
	require.Equal(t, `{"choices":[]}`, out)
}

func TestSanitizeKeepsPreExistingEmptyObjectsAndUnknownFields(t *testing.T) {
	out, changed := sanitizeInternalResponseFields(`{"a":{},"meta":{"request_tag":"keep-me"},"usage":{"credit":1}}`)
	require.True(t, changed)
	require.Equal(t, `{"a":{},"meta":{"request_tag":"keep-me"}}`, out)
	require.Equal(t, "keep-me", gjson.Get(out, "meta.request_tag").String())
}

func TestSanitizeLeavesCleanPayloadByteIdentical(t *testing.T) {
	clean := `{"id":"1","choices":[{"message":{"content":"has the word \"credit\" inside","role":"assistant"}}],"usage":{"total_tokens":1}}`
	out, changed := sanitizeInternalResponseFields(clean)
	require.False(t, changed)
	require.Equal(t, clean, out)
}

func TestSanitizeHandlesQuotedPathKeys(t *testing.T) {
	out, changed := sanitizeInternalResponseFields(`{"u.s":{"credit":1},"a#b":{"provider":"x","keep":true}}`)
	require.True(t, changed)
	require.False(t, gjson.Get(out, `u\.s`).Exists())
	require.Equal(t, true, gjson.Get(out, `a\#b.keep`).Bool())
	require.False(t, gjson.Get(out, `a\#b.provider`).Exists())
}

func TestSanitizeKeepsClaudeCacheCountersForClaudeBodies(t *testing.T) {
	claude := `{"model":"glm-5.3","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"cache_creation_input_tokens":8,"cache_read_input_tokens":99,"credit":1.5,"provider":"x"}}`
	out, changed := sanitizeInternalResponseFields(claude)
	require.True(t, changed)
	require.Equal(t, float64(8), gjson.Get(out, "usage.cache_creation_input_tokens").Float())
	require.Equal(t, float64(99), gjson.Get(out, "usage.cache_read_input_tokens").Float())
	require.False(t, gjson.Get(out, "usage.credit").Exists())
	require.False(t, gjson.Get(out, "usage.provider").Exists())
}

func TestSanitizeAppliesToResponsesProtocol(t *testing.T) {
	responses := `{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":5,"output_tokens":1,"prompt_cache_miss_tokens":4},"provider":"x"}`
	out, changed := sanitizeInternalResponseFields(responses)
	require.True(t, changed)
	require.False(t, gjson.Get(out, "provider").Exists())
	require.False(t, gjson.Get(out, "usage.prompt_cache_miss_tokens").Exists())
	require.Equal(t, "hi", gjson.Get(out, "output.0.content.0.text").String())
}

func TestRewriteResponseBodyStripsInternalFieldsWithoutRewriter(t *testing.T) {
	c := ginJSONContext(t, "/v1/chat/completions", "{}")
	out := RewriteResponseBody(c, []byte(caseBUpstreamResponse))
	require.False(t, gjson.GetBytes(out, "provider").Exists())
	require.Equal(t, "Hi there! How can I help you today?", gjson.GetBytes(out, "choices.0.message.content").String())
}

func TestRewriteStreamChunkStripsUsageChunkWithoutRewriter(t *testing.T) {
	c := ginJSONContext(t, "/v1/chat/completions", "{}")
	chunk := `{"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":81049,"completion_tokens":9,"total_tokens":81058,"credit":12.97,"completion_thinking_tokens":0}}`
	frames, handled := RewriteStreamChunk(c, chunk)
	require.True(t, handled)
	require.Len(t, frames, 1)
	require.False(t, gjson.Get(frames[0].JSON, "usage.credit").Exists())
	require.False(t, gjson.Get(frames[0].JSON, "usage.completion_thinking_tokens").Exists())
	require.Equal(t, float64(81049), gjson.Get(frames[0].JSON, "usage.prompt_tokens").Float())

	// A clean chunk keeps the caller's original pass-through path.
	frames, handled = RewriteStreamChunk(c, `{"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}`)
	require.False(t, handled)
	require.Nil(t, frames)
}

func TestRewriteStreamChunkIgnoresNonJSONPayloadAfterStrip(t *testing.T) {
	c := ginJSONContext(t, "/v1/chat/completions", "{}")
	frames, handled := RewriteStreamChunk(c, "[DONE]")
	require.False(t, handled)
	require.Nil(t, frames)
}
