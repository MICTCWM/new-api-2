package helper

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// classifyStreamPayload reports whether a single upstream SSE payload carries
// visible content or reasoning. The scanner is protocol-agnostic, so it looks
// for the field names shared by the protocols it relays (OpenAI chat, OpenAI
// responses, Claude messages, Gemini, Dify and the legacy completion formats).
//
// Tool calls, tool arguments and image/audio payloads count as content: a
// response that only calls a tool is a valid, non-empty response.
func classifyStreamPayload(data string) (hasContent, hasReasoning bool) {
	data = strings.TrimSpace(data)
	if data == "" {
		return false, false
	}

	var payload any
	if err := common.Unmarshal([]byte(data), &payload); err != nil {
		// Non-JSON stream payloads (for example raw audio chunks) are opaque
		// output. Treat them as content rather than risking a false empty
		// response and an unnecessary retry.
		return true, false
	}

	classifyStreamValue(payload, "", &hasContent, &hasReasoning)
	return hasContent, hasReasoning
}

func classifyStreamValue(value any, parentKey string, hasContent, hasReasoning *bool) {
	switch typed := value.(type) {
	case map[string]any:
		eventType := strings.ToLower(stringValue(typed["type"]))
		if isVisibleOutputType(eventType) {
			*hasContent = true
		}

		for key, child := range typed {
			normalizedKey := strings.ToLower(key)
			switch normalizedKey {
			case "reasoning_content", "reasoning", "thinking", "reasoning_text", "summary_text":
				if nonEmptyString(child) {
					*hasReasoning = true
				}
			case "content", "text", "answer", "output_text", "transcript", "arguments",
				"partial_json", "input_text", "partial_image_b64", "b64_json", "url", "result", "args":
				if nonEmptyString(child) {
					*hasContent = true
				}
			case "audio":
				if nonEmptyString(child) || nonEmptyCollection(child) {
					*hasContent = true
				}
			case "inline_data", "inlinedata", "functioncall", "function_call",
				"executable_code", "executablecode", "code_execution_result":
				if nonEmptyCollection(child) {
					*hasContent = true
				}
			case "delta":
				if delta, ok := child.(string); ok && strings.TrimSpace(delta) != "" {
					if strings.Contains(eventType, "reasoning") || strings.Contains(eventType, "thinking") {
						*hasReasoning = true
					} else {
						*hasContent = true
					}
				}
			case "tool_calls", "tool_call":
				if nonEmptyCollection(child) {
					*hasContent = true
				}
			case "content_block":
				if block, ok := child.(map[string]any); ok && isVisibleOutputType(strings.ToLower(stringValue(block["type"]))) {
					*hasContent = true
				}
			case "output":
				if nonEmptyCollection(child) {
					*hasContent = true
				}
			}

			classifyStreamValue(child, normalizedKey, hasContent, hasReasoning)
		}
	case []any:
		for _, item := range typed {
			classifyStreamValue(item, parentKey, hasContent, hasReasoning)
		}
	case string:
		// A bare string in a stream-list field is not enough to infer content;
		// the protocol-specific keys above cover text deltas.
	}
}

func isVisibleOutputType(eventType string) bool {
	switch eventType {
	case "tool_use", "server_tool_use", "function_call", "custom_tool_call", "image_generation_call",
		"web_search_call", "web_search_tool_result", "file_search_call", "computer_call",
		"code_interpreter_call", "code_execution_tool_result", "mcp_call", "mcp_call_output",
		"mcp_tool_use", "mcp_tool_result", "mcp_list_tools", "mcp_approval_request":
		return true
	}
	return strings.Contains(eventType, "image_generation.completed") ||
		strings.Contains(eventType, "image_edit.completed") ||
		strings.Contains(eventType, "image_generation.partial_image") ||
		strings.Contains(eventType, "image_edit.partial_image")
}

func nonEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func nonEmptyCollection(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case string:
		return strings.TrimSpace(typed) != ""
	default:
		return false
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
