package commandgo

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildTestEnvelope(t *testing.T, req *dto.GeneralOpenAIRequest) map[string]any {
	t.Helper()
	envelope, err := BuildEnvelope(req, Model, "123e4567-e89b-12d3-a456-426614174000", `C:\Users\dev\projects\app`)
	require.NoError(t, err)
	return envelope
}

func TestBuildEnvelopeSystemAndChat(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model: Model,
		Messages: []dto.Message{
			{Role: "system", Content: "you are helpful"},
			{Role: "user", Content: "hello"},
		},
	}
	envelope := buildTestEnvelope(t, req)

	params := envelope["params"].(map[string]any)
	system := params["system"].([]any)
	require.Len(t, system, 1)
	assert.Equal(t, map[string]any{"type": "text", "text": "you are helpful"}, system[0])

	messages := params["messages"].([]any)
	require.Len(t, messages, 1)
	user := messages[0].(map[string]any)
	assert.Equal(t, "user", user["role"])
	assert.Equal(t, []any{map[string]any{"type": "text", "text": "hello"}}, user["content"])

	cfg := envelope["config"].(map[string]any)
	assert.Equal(t, `C:\Users\dev\projects\app`, cfg["workingDir"])
	assert.Equal(t, devicePlatform, cfg["environment"])
	assert.Equal(t, false, cfg["isGitRepo"])
	assert.Equal(t, "standard", envelope["permissionMode"])
	assert.Equal(t, "agent", envelope["mode"])
	assert.Equal(t, "123e4567-e89b-12d3-a456-426614174000", envelope["threadId"])
	assert.Equal(t, true, params["stream"])
}

func TestBuildEnvelopeNoSystemUsesSpacePlaceholder(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{{Role: "user", Content: "hello"}},
	}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	assert.Equal(t, []any{map[string]any{"type": "text", "text": " "}}, params["system"])
}

func TestBuildEnvelopeDeveloperMergedIntoSeparateBlocks(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "system", Content: "one"},
			{Role: "developer", Content: "two"},
			{Role: "user", Content: "hi"},
		},
	}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	system := params["system"].([]any)
	require.Len(t, system, 2)
	assert.Equal(t, "one\n", system[0].(map[string]any)["text"])
	assert.Equal(t, "two", system[1].(map[string]any)["text"])
}

func TestBuildEnvelopeAssistantHistoryOrderAndToolResult(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "user", Content: "run it"},
			{
				Role:             "assistant",
				Content:          "running",
				ReasoningContent: stringPtr("thinking"),
				ToolCalls:        []byte(`[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]`),
			},
			{Role: "tool", ToolCallId: "call_1", Content: "file.txt"},
		},
	}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	messages := params["messages"].([]any)
	require.Len(t, messages, 3)

	assistant := messages[1].(map[string]any)
	parts := assistant["content"].([]any)
	require.Len(t, parts, 3)
	assert.Equal(t, map[string]any{"type": "reasoning", "text": "thinking"}, parts[0])
	assert.Equal(t, map[string]any{"type": "text", "text": "running"}, parts[1])
	toolCall := parts[2].(map[string]any)
	assert.Equal(t, "tool-call", toolCall["type"])
	assert.Equal(t, "call_1", toolCall["toolCallId"])
	assert.Equal(t, "shell", toolCall["toolName"])
	assert.Equal(t, map[string]any{"cmd": "ls"}, toolCall["input"])

	tool := messages[2].(map[string]any)
	toolParts := tool["content"].([]any)
	require.Len(t, toolParts, 1)
	result := toolParts[0].(map[string]any)
	assert.Equal(t, "tool-result", result["type"])
	assert.Equal(t, "call_1", result["toolCallId"])
	assert.Equal(t, "shell", result["toolName"])
	assert.Equal(t, map[string]any{"type": "text", "value": "file.txt"}, result["output"])
}

func TestBuildEnvelopeImageContent(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{{
			Role: "user",
			Content: []any{
				map[string]any{"type": "text", "text": "look"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
			},
		}},
	}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	user := params["messages"].([]any)[0].(map[string]any)
	parts := user["content"].([]any)
	require.Len(t, parts, 2)
	assert.Equal(t, map[string]any{"type": "text", "text": "look"}, parts[0])
	assert.Equal(t, map[string]any{"type": "image", "image": "data:image/png;base64,AAAA", "mimeType": "image/png"}, parts[1])
}

func TestBuildEnvelopeToolsWireShape(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{{Role: "user", Content: "hi"}},
		Tools: []dto.ToolCallRequest{{
			Type: "function",
			Function: dto.FunctionRequest{
				Name:        "get_weather",
				Description: "weather",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
			},
		}},
		ParallelTooCalls: boolPtr(true),
	}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	tools := params["tools"].([]any)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "get_weather", tool["name"])
	assert.Equal(t, "weather", tool["description"])
	assert.NotContains(t, tool, "type")
	assert.Equal(t, "object", tool["input_schema"].(map[string]any)["type"])
	assert.Equal(t, true, params["parallel_tool_calls"])
}

func TestBuildEnvelopeMaxTokensCap(t *testing.T) {
	tooMany := uint(500000)
	req := &dto.GeneralOpenAIRequest{MaxTokens: &tooMany}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	assert.Equal(t, uint64(maxEnvelopeTokens), params["max_tokens"])

	req = &dto.GeneralOpenAIRequest{}
	envelope = buildTestEnvelope(t, req)
	params = envelope["params"].(map[string]any)
	assert.Equal(t, uint64(defaultEnvelopeTokens), params["max_tokens"])
}

func TestBuildEnvelopeRejectsResponseFormat(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		ResponseFormat: &dto.ResponseFormat{Type: "json_object"},
	}
	_, err := BuildEnvelope(req, Model, "", `C:\Users\dev\projects\app`)
	require.Error(t, err)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode)
	assert.Equal(t, "invalid_request_error", apiErr.ToOpenAIError().Type)
}

func TestBuildEnvelopeAllowsTextResponseFormat(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{ResponseFormat: &dto.ResponseFormat{Type: "text"}}
	_, err := BuildEnvelope(req, Model, "", `C:\Users\dev\projects\app`)
	require.NoError(t, err)
}

func stringPtr(v string) *string { return &v }
func boolPtr(v bool) *bool       { return &v }

func TestBuildEnvelopeAlwaysSendsTools(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "hi"}}}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	tools, ok := params["tools"].([]any)
	require.True(t, ok, "tools must be present as an array, got %T", params["tools"])
	assert.Empty(t, tools)

	raw, err := common.Marshal(envelope)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"tools":[]`)
}

func TestBuildEnvelopeGitConfigFields(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "hi"}}}
	envelope := buildTestEnvelope(t, req)

	raw, err := common.Marshal(envelope)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, common.Unmarshal(raw, &decoded))
	cfg, ok := decoded["config"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "", cfg["currentBranch"])
	assert.Equal(t, "", cfg["mainBranch"])
	assert.Equal(t, "", cfg["gitStatus"])
	commits, ok := cfg["recentCommits"].([]any)
	require.True(t, ok, "recentCommits must be an array, got %T", cfg["recentCommits"])
	assert.Empty(t, commits)
	structure, ok := cfg["structure"].([]any)
	require.True(t, ok, "structure must be an array, got %T", cfg["structure"])
	assert.Empty(t, structure)

	// Empty slices must serialize as [] rather than null so upstream validation
	// accepting only arrays does not reject the envelope.
	assert.Contains(t, string(raw), `"recentCommits":[]`)
	assert.Contains(t, string(raw), `"structure":[]`)
}

func TestBuildEnvelopeToolNameAliases(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Messages: []dto.Message{
			{Role: "user", Content: "run"},
			{
				Role:      "assistant",
				ToolCalls: []byte(`[{"id":"call_1","type":"function","function":{"name":"bash_output","arguments":"{}"}}]`),
			},
			{Role: "tool", ToolCallId: "call_1", Content: "ok"},
		},
		Tools: []dto.ToolCallRequest{{
			Type:     "function",
			Function: dto.FunctionRequest{Name: "tool_search"},
		}},
		ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "read_multiple_files"}},
	}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)

	tools := params["tools"].([]any)
	assert.Equal(t, "search_tools", tools[0].(map[string]any)["name"])

	choice := params["tool_choice"].(map[string]any)
	assert.Equal(t, "read_file", choice["name"])

	messages := params["messages"].([]any)
	assistantParts := messages[1].(map[string]any)["content"].([]any)
	assert.Equal(t, "shell_output", assistantParts[0].(map[string]any)["toolName"])

	toolResult := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	assert.Equal(t, "shell_output", toolResult["toolName"])
}

func TestBuildEnvelopeNegativeMaxTokensFallsBackToDefault(t *testing.T) {
	negative := uint(0)
	// uint cannot be negative; the guard is still exercised via an explicit 0
	// (covered) plus a huge value to confirm clamping.
	req := &dto.GeneralOpenAIRequest{MaxTokens: &negative}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	assert.Equal(t, uint64(defaultEnvelopeTokens), params["max_tokens"])
}

func TestBuildEnvelopePromptCacheKeyAddsMarker(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		PromptCacheKey: "cache-key",
		Messages: []dto.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "hi"},
		},
	}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	system := params["system"].([]any)
	block := system[len(system)-1].(map[string]any)
	assert.Equal(t, map[string]any{"type": "ephemeral"}, block["cache_control"])
}

func TestBuildEnvelopePreservesExplicitCacheControl(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		PromptCacheKey: "cache-key",
		Messages: []dto.Message{{
			Role: "system",
			Content: []any{
				map[string]any{"type": "text", "text": "system", "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}},
			},
		}},
	}
	envelope := buildTestEnvelope(t, req)
	params := envelope["params"].(map[string]any)
	block := params["system"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{"type": "ephemeral", "ttl": "1h"}, block["cache_control"])
}
