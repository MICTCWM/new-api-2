package commandgo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

const (
	maxEnvelopeTokens     = 200000
	defaultEnvelopeTokens = 64000
)

// UnsupportedResponseFormatError mirrors unsupportedResponseFormatError in the
// prototype: the agent protocol has no structured-output field, so any
// constraint other than the implicit {"type":"text"} is rejected with 400
// rather than silently ignored.
func UnsupportedResponseFormatError(responseFormat *dto.ResponseFormat, param string) *types.NewAPIError {
	if responseFormat == nil {
		return nil
	}
	formatType := strings.TrimSpace(responseFormat.Type)
	if formatType == "" || formatType == "text" {
		return nil
	}
	return types.WithOpenAIError(types.OpenAIError{
		Message: fmt.Sprintf("%s is not supported: the upstream agent protocol has no structured-output field, so %q constraints would be silently ignored. Constrain the output format in the prompt instead", param, formatType),
		Type:    "invalid_request_error",
		Param:   param,
		Code:    types.ErrorCodeInvalidRequest,
	}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

// BuildEnvelope converts an OpenAI chat completions request into the CLI agent
// envelope accepted by POST /alpha/generate.
func BuildEnvelope(request *dto.GeneralOpenAIRequest, model, threadID, workingDir string) (map[string]any, error) {
	if request == nil {
		return nil, fmt.Errorf("command go: request is nil")
	}
	if err := UnsupportedResponseFormatError(request.ResponseFormat, "response_format"); err != nil {
		return nil, err
	}

	systemBlocks, chatMessages := buildMessages(request.Messages)
	applyPromptCacheMarker(systemBlocks, chatMessages, request.PromptCacheKey)

	params := map[string]any{
		"model":      model,
		"messages":   chatMessages,
		"max_tokens": envelopeMaxTokens(request),
		"stream":     true,
	}
	if len(systemBlocks) > 0 {
		params["system"] = systemBlocks
	} else {
		// The upstream injects a large default system prompt when params.system
		// is absent; a single space block bypasses it.
		params["system"] = []any{map[string]any{"type": "text", "text": " "}}
	}
	if request.Temperature != nil {
		params["temperature"] = *request.Temperature
	}
	if effort := strings.TrimSpace(request.ReasoningEffort); effort != "" {
		params["reasoning_effort"] = effort
	}
	// The CLI always sends tools, using an empty array when none are declared;
	// an absent key is observably different on the wire.
	params["tools"] = buildTools(request.Tools)
	if choice := buildToolChoice(request.ToolChoice); choice != nil {
		params["tool_choice"] = choice
	}
	if request.ParallelTooCalls != nil {
		params["parallel_tool_calls"] = *request.ParallelTooCalls
	}

	cfg := map[string]any{
		"workingDir":    workingDir,
		"date":          time.Now().UTC().Format("2006-01-02"),
		"environment":   devicePlatform,
		"structure":     []any{},
		"isGitRepo":     false,
		"currentBranch": "",
		"mainBranch":    "",
		"gitStatus":     "",
		"recentCommits": []any{},
	}
	envelope := map[string]any{
		"config":         cfg,
		"memory":         nil,
		"taste":          nil,
		"skills":         nil,
		"permissionMode": "standard",
		"mode":           "agent",
		"params":         params,
	}
	if isUUID(threadID) {
		envelope["threadId"] = threadID
	}
	return envelope, nil
}

func envelopeMaxTokens(request *dto.GeneralOpenAIRequest) uint64 {
	// Negative values are invalid; treat them like an absent value and fall
	// back to the default before clamping.
	var tokens int64
	if request.MaxTokens != nil && *request.MaxTokens > 0 {
		tokens = int64(*request.MaxTokens)
	} else if request.MaxCompletionTokens != nil && *request.MaxCompletionTokens > 0 {
		tokens = int64(*request.MaxCompletionTokens)
	}
	if tokens <= 0 {
		tokens = defaultEnvelopeTokens
	}
	if tokens > maxEnvelopeTokens {
		tokens = maxEnvelopeTokens
	}
	return uint64(tokens)
}

// applyPromptCacheMarker places a cache breakpoint on the last system block
// when the client supplied an OpenAI prompt_cache_key and no explicit
// cache_control marker exists, matching the CLI's prefix-cache semantics.
func applyPromptCacheMarker(systemBlocks, chatMessages []any, promptCacheKey string) {
	if strings.TrimSpace(promptCacheKey) == "" || len(systemBlocks) == 0 {
		return
	}
	for _, raw := range systemBlocks {
		if block, ok := raw.(map[string]any); ok && block["cache_control"] != nil {
			return
		}
	}
	for _, raw := range chatMessages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, partRaw := range parts {
			if part, ok := partRaw.(map[string]any); ok && part["cache_control"] != nil {
				return
			}
		}
	}
	if last, ok := systemBlocks[len(systemBlocks)-1].(map[string]any); ok {
		last["cache_control"] = map[string]any{"type": "ephemeral"}
	}
}

func buildMessages(messages []dto.Message) ([]any, []any) {
	systemBlocks := make([]any, 0)
	chat := make([]any, 0, len(messages))
	for i := range messages {
		msg := &messages[i]
		role := strings.TrimSpace(msg.Role)
		if role == "system" || role == "developer" {
			appendSystemBlocks(&systemBlocks, msg.Content)
		}
	}
	for i := range systemBlocks {
		if i < len(systemBlocks)-1 {
			block := systemBlocks[i].(map[string]any)
			block["text"] = block["text"].(string) + "\n"
		}
	}

	toolNames := make(map[string]string)
	for i := range messages {
		msg := &messages[i]
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range parseToolCalls(msg.ToolCalls) {
			id := stringField(tc, "id")
			if id == "" {
				continue
			}
			if fn, ok := tc["function"].(map[string]any); ok {
				toolNames[id] = stringField(fn, "name")
			}
		}
	}

	for i := range messages {
		msg := &messages[i]
		role := strings.TrimSpace(msg.Role)
		if role == "system" || role == "developer" {
			continue
		}
		switch role {
		case "user":
			chat = append(chat, map[string]any{"role": "user", "content": buildUserContent(msg.Content)})
		case "assistant":
			chat = append(chat, map[string]any{"role": "assistant", "content": buildAssistantContent(msg)})
		case "tool":
			name := toolNames[msg.ToolCallId]
			if name == "" && msg.Name != nil {
				name = *msg.Name
			}
			name = toWireToolName(strings.TrimSpace(name))
			chat = append(chat, map[string]any{
				"role": "tool",
				"content": []any{map[string]any{
					"type":       "tool-result",
					"toolCallId": msg.ToolCallId,
					"toolName":   name,
					"output":     map[string]any{"type": "text", "value": toWireToolOutputValue(msg.Content)},
				}},
			})
		default:
			// Unknown roles are normalized to user so upstream validation accepts
			// the conversation.
			chat = append(chat, map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": toWireString(msg.Content)}},
			})
		}
	}
	return systemBlocks, chat
}

func appendSystemBlocks(dst *[]any, content any) {
	switch v := normalizeContent(content).(type) {
	case nil:
		return
	case string:
		if v != "" {
			*dst = append(*dst, map[string]any{"type": "text", "text": v})
		}
	case []any:
		for _, raw := range v {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			text := stringField(part, "text")
			if text == "" {
				text = stringField(part, "content")
			}
			cacheControl, hasCache := part["cache_control"]
			if text == "" && !hasCache {
				continue
			}
			block := map[string]any{"type": "text", "text": text}
			if hasCache && cacheControl != nil {
				block["cache_control"] = cacheControl
			}
			*dst = append(*dst, block)
		}
	default:
		*dst = append(*dst, map[string]any{"type": "text", "text": toWireString(content)})
	}
}

func buildUserContent(content any) []any {
	switch v := normalizeContent(content).(type) {
	case nil:
		return []any{}
	case string:
		return []any{map[string]any{"type": "text", "text": v}}
	case []any:
		parts := make([]any, 0, len(v))
		for _, raw := range v {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if stringField(part, "type") == "image_url" {
				parts = append(parts, convertImagePart(part))
				continue
			}
			parts = append(parts, part)
		}
		return parts
	default:
		return []any{map[string]any{"type": "text", "text": toWireString(content)}}
	}
}

// convertImagePart maps OpenAI image_url content to the CLI image block.
func convertImagePart(part map[string]any) map[string]any {
	url := ""
	switch v := part["image_url"].(type) {
	case string:
		url = v
	case map[string]any:
		url = stringField(v, "url")
	}
	image := map[string]any{"type": "image", "image": url}
	if strings.HasPrefix(url, "data:") {
		rest := strings.TrimPrefix(url, "data:")
		if idx := strings.Index(rest, ";"); idx > 0 {
			image["mimeType"] = rest[:idx]
		}
	}
	return image
}

func buildAssistantContent(msg *dto.Message) []any {
	parts := make([]any, 0)
	reasoning := ""
	if msg.ReasoningContent != nil {
		reasoning = *msg.ReasoningContent
	} else if msg.Reasoning != nil {
		reasoning = *msg.Reasoning
	}
	if reasoning != "" {
		parts = append(parts, map[string]any{"type": "reasoning", "text": reasoning})
	}
	switch v := normalizeContent(msg.Content).(type) {
	case string:
		if v != "" {
			parts = append(parts, map[string]any{"type": "text", "text": v})
		}
	case []any:
		for _, raw := range v {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch stringField(part, "type") {
			case "text":
				parts = append(parts, part)
			case "reasoning":
				if reasoning == "" {
					parts = append(parts, part)
				}
			}
		}
	}
	for _, tc := range parseToolCalls(msg.ToolCalls) {
		fn, _ := tc["function"].(map[string]any)
		parts = append(parts, map[string]any{
			"type":       "tool-call",
			"toolCallId": stringField(tc, "id"),
			"toolName":   toWireToolName(strings.TrimSpace(stringField(fn, "name"))),
			"input":      parseToolArguments(fn["arguments"]),
		})
	}
	return parts
}

func buildTools(tools []dto.ToolCallRequest) []any {
	out := make([]any, 0, len(tools))
	for i := range tools {
		tool := &tools[i]
		name := toWireToolName(strings.TrimSpace(tool.Function.Name))
		description := tool.Function.Description
		params := tool.Function.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":         name,
			"description":  description,
			"input_schema": normalizeContent(params),
		})
	}
	return out
}

func buildToolChoice(choice any) any {
	if choice == nil {
		return nil
	}
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		case "required":
			return map[string]any{"type": "any"}
		default:
			return map[string]any{"type": "auto"}
		}
	case map[string]any:
		if stringField(v, "type") == "function" {
			fn, _ := v["function"].(map[string]any)
			return map[string]any{"type": "tool", "name": toWireToolName(strings.TrimSpace(stringField(fn, "name")))}
		}
		return v
	default:
		return normalizeContent(choice)
	}
}

// parseToolCalls normalizes the raw OpenAI tool_calls array.
func parseToolCalls(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var calls []map[string]any
	if err := common.Unmarshal(raw, &calls); err != nil {
		return nil
	}
	return calls
}

func parseToolArguments(value any) any {
	switch v := value.(type) {
	case nil:
		return map[string]any{}
	case string:
		var parsed any
		if err := common.UnmarshalJsonStr(v, &parsed); err != nil {
			return map[string]any{}
		}
		return parsed
	default:
		return normalizeContent(v)
	}
}

// normalizeContent converts arbitrary content into plain string / []any / map
// values suitable for re-marshaling into the envelope.
func normalizeContent(content any) any {
	switch v := content.(type) {
	case nil:
		return nil
	case string:
		return v
	case []byte:
		return string(v)
	}
	raw, err := common.Marshal(content)
	if err != nil {
		return toWireString(content)
	}
	var out any
	if err := common.Unmarshal(raw, &out); err != nil {
		return toWireString(content)
	}
	return out
}

func toWireString(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// toWireToolOutputValue keeps only text blocks, joined by newlines, matching
// the CLI's toWireToolOutput.
func toWireToolOutputValue(content any) string {
	switch v := normalizeContent(content).(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		texts := make([]string, 0, len(v))
		for _, raw := range v {
			part, ok := raw.(map[string]any)
			if !ok || stringField(part, "type") != "text" {
				continue
			}
			texts = append(texts, stringField(part, "text"))
		}
		return strings.Join(texts, "\n")
	default:
		return fmt.Sprint(v)
	}
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

var toolNameAliases = map[string]string{
	"bash_output":         "shell_output",
	"task_output":         "shell_output",
	"tool_search":         "search_tools",
	"read_multiple_files": "read_file",
}

func toWireToolName(name string) string {
	if alias, ok := toolNameAliases[name]; ok {
		return alias
	}
	return name
}

// clientToolNameReverseMap derives this request's wire -> client tool-name map
// so responses can restore the names the client registered. The static alias
// table is many-to-one (bash_output/task_output both become shell_output), so
// the mapping must be per request rather than global. When several client names
// collapse onto the same wire name, the first declared name wins.
func clientToolNameReverseMap(request *dto.GeneralOpenAIRequest) map[string]string {
	if request == nil {
		return nil
	}
	reverse := make(map[string]string)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		wire := toWireToolName(name)
		if _, exists := reverse[wire]; !exists {
			reverse[wire] = name
		}
	}
	for i := range request.Tools {
		add(request.Tools[i].Function.Name)
	}
	// A tool named only by tool_choice is still declared for this request, so it
	// must also be restored on the response side.
	if choice, ok := request.ToolChoice.(map[string]any); ok {
		if fn, ok := choice["function"].(map[string]any); ok {
			add(stringField(fn, "name"))
		}
	}
	for i := range request.Messages {
		msg := &request.Messages[i]
		for _, call := range parseToolCalls(msg.ToolCalls) {
			if fn, ok := call["function"].(map[string]any); ok {
				add(stringField(fn, "name"))
			}
		}
		if msg.Name != nil {
			add(*msg.Name)
		}
	}
	if len(reverse) == 0 {
		return nil
	}
	return reverse
}
