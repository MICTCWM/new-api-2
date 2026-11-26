package oaichat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIChatRequestToClaudeMessagesNormalizesToolInputSchema(t *testing.T) {
	tests := []struct {
		name       string
		parameters any
		wantSchema map[string]any
	}{
		{
			name:       "omitted parameters",
			parameters: nil,
			wantSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			name: "missing type and properties",
			parameters: map[string]any{
				"additionalProperties": false,
			},
			wantSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		{
			name: "non-string type",
			parameters: map[string]any{
				"type":       123,
				"properties": map[string]any{},
			},
			wantSchema: map[string]any{
				"type":       123,
				"properties": map[string]any{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxTokens := uint(1024)
			got, err := OpenAIChatRequestToClaudeMessages(context.Background(), nil, dto.GeneralOpenAIRequest{
				Model:     "claude-test",
				MaxTokens: &maxTokens,
				Messages: []dto.Message{
					{Role: "user", Content: "Call the tool."},
				},
				Tools: []dto.ToolCallRequest{
					{
						Type: "function",
						Function: dto.FunctionRequest{
							Name:        "get_current_time",
							Description: "Get the current time",
							Parameters:  tt.parameters,
						},
					},
				},
			})

			require.NoError(t, err)
			tools, ok := got.Tools.([]any)
			require.True(t, ok)
			require.Len(t, tools, 1)
			tool, ok := tools[0].(*dto.Tool)
			require.True(t, ok)
			assert.Equal(t, "get_current_time", tool.Name)
			assert.Equal(t, tt.wantSchema, tool.InputSchema)
		})
	}
}

func TestOpenAIChatRequestToClaudeMessagesNormalizesDeveloperRole(t *testing.T) {
	maxTokens := uint(1024)
	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), nil, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "developer", Content: "DEV_MARKER"},
			{Role: "user", Content: "hello"},
		},
	})
	require.NoError(t, err)
	system, ok := got.System.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, system, 1)
	assert.Equal(t, "DEV_MARKER", system[0].GetText())
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "user", got.Messages[0].Role)
}

func TestOpenAIChatRequestToClaudeMessagesKeepsMessageBoundaries(t *testing.T) {
	maxTokens := uint(1024)
	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), nil, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "system", Content: "SYS_MARKER"},
			{Role: "user", Content: "U1"},
			{Role: "user", Content: "U2"},
			{Role: "assistant", Content: ""},
			{Role: "assistant", Content: "A2"},
			{Role: "user", Content: "U3"},
		},
	})
	require.NoError(t, err)
	require.Len(t, got.Messages, 4)
	assert.Equal(t, "user", got.Messages[0].Role)
	assert.Equal(t, "U1", got.Messages[0].Content)
	assert.Equal(t, "user", got.Messages[1].Role)
	assert.Equal(t, "U2", got.Messages[1].Content)
	assert.Equal(t, "assistant", got.Messages[2].Role)
	assert.Equal(t, "A2", got.Messages[2].Content)
	assert.Equal(t, "user", got.Messages[3].Role)
	assert.Equal(t, "U3", got.Messages[3].Content)
}

func TestOpenAIChatRequestToClaudeMessagesPreservesAssistantTextWithToolCalls(t *testing.T) {
	maxTokens := uint(1024)
	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), nil, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "user", Content: "use a tool"},
			{
				Role:      "assistant",
				Content:   "ASSIST_TEXT",
				ToolCalls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}}]`),
			},
			{Role: "tool", ToolCallId: "call_1", Content: "sunny"},
		},
	})
	require.NoError(t, err)
	require.Len(t, got.Messages, 3)
	blocks, ok := got.Messages[1].Content.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, blocks, 2)
	assert.Equal(t, "text", blocks[0].Type)
	assert.Equal(t, "ASSIST_TEXT", blocks[0].GetText())
	assert.Equal(t, "tool_use", blocks[1].Type)
	assert.Equal(t, "call_1", blocks[1].Id)
	toolBlocks, ok := got.Messages[2].Content.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, toolBlocks, 1)
	assert.Equal(t, "tool_result", toolBlocks[0].Type)
	assert.Equal(t, "call_1", toolBlocks[0].ToolUseId)
	assert.Equal(t, "sunny", toolBlocks[0].Content)
}

func TestOpenAIChatRequestToClaudeMessagesCombinesConsecutiveToolResults(t *testing.T) {
	maxTokens := uint(1024)
	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), nil, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "user", Content: "call two tools"},
			{
				Role:    "assistant",
				Content: "calling",
				ToolCalls: json.RawMessage(`[
					{"id":"call_1","type":"function","function":{"name":"one","arguments":"{}"}},
					{"id":"call_2","type":"function","function":{"name":"two","arguments":"{}"}}
				]`),
			},
			{Role: "tool", ToolCallId: "call_1", Content: "result one"},
			{Role: "tool", ToolCallId: "call_2", Content: []any{
				map[string]any{"type": "text", "text": "result two"},
			}},
		},
	})
	require.NoError(t, err)
	require.Len(t, got.Messages, 3)
	assistantBlocks, ok := got.Messages[1].Content.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	var toolUses int
	for _, block := range assistantBlocks {
		if block.Type == "tool_use" {
			toolUses++
		}
	}
	assert.Equal(t, 2, toolUses)
	toolBlocks, ok := got.Messages[2].Content.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, toolBlocks, 2)
	assert.Equal(t, "call_1", toolBlocks[0].ToolUseId)
	assert.Equal(t, "call_2", toolBlocks[1].ToolUseId)
	secondContent, ok := toolBlocks[1].Content.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	assert.Equal(t, "result two", secondContent[0].GetText())
}

func TestOpenAIChatRequestToClaudeMessagesCollectsDynamicSystemTools(t *testing.T) {
	maxTokens := uint(1024)
	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), nil, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "user", Content: "calc"},
			{Role: "system", Tools: []dto.ToolCallRequest{
				{Type: "function", Function: dto.FunctionRequest{Name: "calculate", Parameters: map[string]any{"type": "object"}}},
			}},
		},
	})
	require.NoError(t, err)
	tools, ok := got.Tools.([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(*dto.Tool)
	require.True(t, ok)
	assert.Equal(t, "calculate", tool.Name)
}

func TestOpenAIChatRequestToClaudeMessagesRejectsSystemContentWithDynamicTools(t *testing.T) {
	maxTokens := uint(1024)
	_, err := OpenAIChatRequestToClaudeMessages(context.Background(), nil, dto.GeneralOpenAIRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.Message{
			{Role: "system", Content: "conflict", Tools: []dto.ToolCallRequest{
				{Type: "function", Function: dto.FunctionRequest{Name: "calculate", Parameters: map[string]any{"type": "object"}}},
			}},
			{Role: "user", Content: "calc"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot contain both content and tools")
}
