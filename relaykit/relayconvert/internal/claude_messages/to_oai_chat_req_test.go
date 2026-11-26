package claudemessages

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(value string) *string { return &value }

func TestClaudeMessagesRequestToOpenAIChatPreservesAssistantTextWithToolUse(t *testing.T) {
	maxTokens := uint(1024)
	got, err := ClaudeMessagesRequestToOpenAIChat(dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "text", Text: strPtr("I will check the weather.")},
				{Type: "tool_use", Id: "toolu_1", Name: "get_weather", Input: map[string]any{"city": "Beijing"}},
			}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "toolu_1", Content: "sunny"},
			}},
		},
	}, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 3)
	require.Equal(t, "assistant", got.Messages[1].Role)
	require.Len(t, got.Messages[1].ParseContent(), 1)
	assert.Equal(t, "I will check the weather.", got.Messages[1].ParseContent()[0].Text)
	require.Len(t, got.Messages[1].ParseToolCalls(), 1)
	assert.Equal(t, "get_weather", got.Messages[1].ParseToolCalls()[0].Function.Name)
	assert.Equal(t, "tool", got.Messages[2].Role)
	assert.Equal(t, "sunny", got.Messages[2].StringContent())
}

func TestClaudeMessagesRequestToOpenAIChatPreservesSystemBlockBoundaries(t *testing.T) {
	maxTokens := uint(1024)
	got, err := ClaudeMessagesRequestToOpenAIChat(dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		System: []dto.ClaudeMediaMessage{
			{Type: "text", Text: strPtr("first instruction")},
			{Type: "text", Text: strPtr("second instruction")},
		},
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
	}, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 2)
	assert.Equal(t, "system", got.Messages[0].Role)
	assert.Equal(t, "first instruction\n\nsecond instruction", got.Messages[0].StringContent())
}

func TestClaudeMessagesRequestToOpenAIChatMapsThinkingAndImageURL(t *testing.T) {
	maxTokens := uint(1024)
	got, err := ClaudeMessagesRequestToOpenAIChat(dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "text", Text: strPtr("describe")},
				{Type: "image", Source: &dto.ClaudeMessageSource{Type: "url", Url: "https://example.com/cat.png"}},
			}},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "thinking", Thinking: strPtr("reasoning text")},
				{Type: "text", Text: strPtr("answer")},
			}},
		},
	}, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 2)
	userParts := got.Messages[0].ParseContent()
	require.Len(t, userParts, 2)
	assert.Equal(t, "https://example.com/cat.png", userParts[1].GetImageMedia().Url)
	require.NotNil(t, got.Messages[1].ReasoningContent)
	assert.Equal(t, "reasoning text", *got.Messages[1].ReasoningContent)
}

func TestClaudeMessagesRequestToOpenAIChatFlattensTextToolResult(t *testing.T) {
	maxTokens := uint(1024)
	got, err := ClaudeMessagesRequestToOpenAIChat(dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_use", Id: "toolu_1", Name: "lookup", Input: map[string]any{}},
			}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "toolu_1", Content: []dto.ClaudeMediaMessage{
					{Type: "text", Text: strPtr("line one")},
					{Type: "text", Text: strPtr("line two")},
				}},
			}},
		},
	}, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 3)
	assert.Equal(t, "line one\nline two", got.Messages[2].StringContent())
	assert.NotContains(t, got.Messages[2].StringContent(), `"type":"text"`)
}

func TestClaudeMessagesRequestToOpenAIChatDocumentBecomesFilePart(t *testing.T) {
	maxTokens := uint(1024)
	got, err := ClaudeMessagesRequestToOpenAIChat(dto.ClaudeRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "document", Source: &dto.ClaudeMessageSource{Type: "base64", MediaType: "application/pdf", Data: kitutil.GetPointer("JVBERi0=")}},
			}},
		},
	}, nil)
	require.NoError(t, err)
	require.Len(t, got.Messages, 1)
	parts := got.Messages[0].ParseContent()
	require.Len(t, parts, 1)
	assert.Equal(t, "file", parts[0].Type)
	assert.NotNil(t, parts[0].File)
}
