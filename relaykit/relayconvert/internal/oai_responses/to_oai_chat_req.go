package oairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

const (
	responsesInputTypeFunctionCall       = "function_call"
	responsesInputTypeFunctionCallOutput = "function_call_output"
	responsesInputTypeCustomToolCall     = "custom_tool_call"
	responsesInputTypeCustomToolOutput   = "custom_tool_call_output"
	responsesInputTypeToolSearchCall     = "tool_search_call"
	responsesInputTypeToolSearchOutput   = "tool_search_output"
)

const (
	ResponsesInputTypeFunctionCall       = responsesInputTypeFunctionCall
	ResponsesInputTypeFunctionCallOutput = responsesInputTypeFunctionCallOutput
	ResponsesInputTypeCustomToolCall     = responsesInputTypeCustomToolCall
	ResponsesInputTypeCustomToolOutput   = responsesInputTypeCustomToolOutput
	ResponsesInputTypeToolSearchCall     = responsesInputTypeToolSearchCall
	ResponsesInputTypeToolSearchOutput   = responsesInputTypeToolSearchOutput
)

func ResponsesRequestToChatCompletionsRequest(req *dto.OpenAIResponsesRequest) (*dto.GeneralOpenAIRequest, error) {
	if req == nil {
		return nil, errors.New("request is nil")
	}
	if req.Model == "" {
		return nil, errors.New("model is required")
	}
	if err := validateResponsesRequestChatUnsupportedFields(req); err != nil {
		return nil, err
	}

	messages, err := responsesRequestMessagesToChat(req)
	if err != nil {
		return nil, err
	}
	// MiniMax 等上游要求 system 只能作为首条消息：把散落在各处的 system 消息
	// 合并成一条置于头部（cc-switch collapse_system_messages_to_head 同款）。
	messages = collapseSystemMessagesToHead(messages)

	tools, err := responsesRequestToolsToChat(req.Tools)
	if err != nil {
		return nil, err
	}

	toolChoice, err := responsesRequestToolChoiceToChat(req.ToolChoice)
	if err != nil {
		return nil, err
	}

	responseFormat, err := responsesRequestTextToChatResponseFormat(req.Text)
	if err != nil {
		return nil, err
	}

	out := &dto.GeneralOpenAIRequest{
		Model:                req.Model,
		Messages:             messages,
		Stream:               req.Stream,
		StreamOptions:        req.StreamOptions,
		Temperature:          req.Temperature,
		TopP:                 req.TopP,
		TopLogProbs:          req.TopLogProbs,
		ResponseFormat:       responseFormat,
		Tools:                tools,
		ToolChoice:           toolChoice,
		User:                 req.User,
		Store:                req.Store,
		Metadata:             req.Metadata,
		SafetyIdentifier:     req.SafetyIdentifier,
		PromptCacheRetention: req.PromptCacheRetention,
		EnableThinking:       req.EnableThinking,
		ThinkingBudget:       req.ThinkingBudget,
	}

	// max_output_tokens 路由：o 系列 / gpt-5 只接受 max_completion_tokens，
	// 其余 chat 上游接受 max_tokens（cc-switch 同款路由），与 openai 通道
	// ConvertOpenAIRequest 的归一化口径保持一致。
	if isMaxCompletionTokensModel(req.Model) {
		out.MaxCompletionTokens = req.MaxOutputTokens
	} else {
		out.MaxTokens = req.MaxOutputTokens
	}

	out.FrequencyPenalty, err = responsesRawFloat(req.FrequencyPenalty)
	if err != nil {
		return nil, fmt.Errorf("invalid frequency_penalty: %w", err)
	}
	out.PresencePenalty, err = responsesRawFloat(req.PresencePenalty)
	if err != nil {
		return nil, fmt.Errorf("invalid presence_penalty: %w", err)
	}

	if req.Reasoning != nil {
		out.ReasoningEffort = req.Reasoning.Effort
	}
	if req.ServiceTier != "" {
		out.ServiceTier, _ = kitutil.Marshal(req.ServiceTier)
	}
	if len(req.ParallelToolCalls) > 0 && kitutil.GetJsonType(req.ParallelToolCalls) == "boolean" {
		var parallelToolCalls bool
		if err := kitutil.Unmarshal(req.ParallelToolCalls, &parallelToolCalls); err == nil {
			out.ParallelTooCalls = &parallelToolCalls
		}
	}
	if len(req.PromptCacheKey) > 0 && kitutil.GetJsonType(req.PromptCacheKey) == "string" {
		var promptCacheKey string
		if err := kitutil.Unmarshal(req.PromptCacheKey, &promptCacheKey); err == nil {
			out.PromptCacheKey = promptCacheKey
		}
	}

	// Strict OpenAI 兼容上游（vLLM、企业网关）会拒绝带 tool_choice /
	// parallel_tool_calls 但 tools 为空的请求。转换后 tools 恰好为空时移除
	// 这两个字段（cc-switch 同款处理），避免 400/503。
	if len(out.Tools) == 0 {
		out.ToolChoice = nil
		out.ParallelTooCalls = nil
	}

	return out, nil
}

// isMaxCompletionTokensModel 判断目标模型是否只接受 max_completion_tokens
// （o 系列 / gpt-5），与 openai 通道 ConvertOpenAIRequest 的准入口径一致。
func isMaxCompletionTokensModel(model string) bool {
	return dto.IsOpenAIReasoningOModel(model) || dto.IsOpenAIGPT5Model(model)
}

func validateResponsesRequestChatUnsupportedFields(req *dto.OpenAIResponsesRequest) error {
	unsupported := make([]string, 0, 4)
	if rawJSONPresent(req.Conversation) {
		unsupported = append(unsupported, "conversation")
	}
	if strings.TrimSpace(req.PreviousResponseID) != "" {
		unsupported = append(unsupported, "previous_response_id")
	}
	if rawJSONPresent(req.Prompt) {
		unsupported = append(unsupported, "prompt")
	}
	if rawJSONPresent(req.ContextManagement) {
		unsupported = append(unsupported, "context_management")
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("responses to chat conversion does not support stateful fields: %s", strings.Join(unsupported, ", "))
	}
	return nil
}

func ValidateRequestChatUnsupportedFields(req *dto.OpenAIResponsesRequest) error {
	return validateResponsesRequestChatUnsupportedFields(req)
}

func responsesRequestMessagesToChat(req *dto.OpenAIResponsesRequest) ([]dto.Message, error) {
	builder := &chatMessageBuilder{lastAssistantIndex: -1}
	if rawJSONPresent(req.Instructions) {
		instructions, err := responsesJSONString(req.Instructions)
		if err != nil {
			return nil, fmt.Errorf("invalid instructions: %w", err)
		}
		if strings.TrimSpace(instructions) != "" {
			builder.messages = append(builder.messages, dto.Message{Role: "system", Content: instructions})
		}
	}

	if !rawJSONPresent(req.Input) {
		return builder.messages, nil
	}

	switch kitutil.GetJsonType(req.Input) {
	case "string":
		input, err := responsesJSONString(req.Input)
		if err != nil {
			return nil, fmt.Errorf("invalid input string: %w", err)
		}
		builder.messages = append(builder.messages, dto.Message{Role: "user", Content: input})
	case "array":
		var items []map[string]any
		if err := kitutil.Unmarshal(req.Input, &items); err != nil {
			return nil, fmt.Errorf("invalid input array: %w", err)
		}
		for _, item := range items {
			if err := builder.processItem(item); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported responses input type %q", kitutil.GetJsonType(req.Input))
	}

	// 收尾：把剩余未消费的 tool-call 批次落成 assistant、把尾部 pending
	// reasoning 回溯附挂到上一条 assistant、给仍缺 reasoning_content 的
	// assistant 工具调用消息补占位（cc-switch append_responses_input_as_chat
	// _messages 末尾的同款收尾）。
	builder.flushPendingToolCalls()
	builder.backAttachPendingReasoning()
	builder.backfillToolCallReasoning()
	return builder.messages, nil
}

// chatMessageBuilder 把 Responses input 条目流式累积为 chat messages，并处理
// 推理文本（reasoning）的归属：reasoning 先进入 pendingReasoning 池，前向附挂
// 到其后第一条 assistant 消息（含工具调用批次）；在 user 等回合边界或整个
// input 结束时回溯附挂到上一条 assistant。reasoning 不允许跨 user 回合泄漏到
// 之后的 assistant 消息。语义对齐 cc-switch 的 append_responses_item_as_chat
// _message / append_pending_reasoning / attach_pending_reasoning_to_* 系列。
type chatMessageBuilder struct {
	messages           []dto.Message
	pendingToolCalls   []dto.ToolCallRequest
	pendingReasoning   *string
	lastAssistantIndex int
}

func (b *chatMessageBuilder) processItem(item map[string]any) error {
	itemType := strings.TrimSpace(kitutil.Interface2String(item["type"]))
	switch itemType {
	case responsesInputTypeFunctionCall:
		toolCall, err := responsesFunctionCallItemToChatToolCall(item)
		if err != nil {
			return err
		}
		b.appendUniqueReasoning(responsesItemReasoningText(item))
		b.pendingToolCalls = append(b.pendingToolCalls, toolCall)
		return nil
	case responsesInputTypeCustomToolCall, responsesInputTypeToolSearchCall:
		toolCall, err := responsesCustomToolCallItemToChatToolCall(item, itemType)
		if err != nil {
			return err
		}
		b.appendUniqueReasoning(responsesItemReasoningText(item))
		b.pendingToolCalls = append(b.pendingToolCalls, toolCall)
		return nil
	case responsesInputTypeFunctionCallOutput:
		b.flushPendingToolCalls()
		b.messages = append(b.messages, dto.Message{
			Role:       "tool",
			ToolCallId: responsesCallID(item),
			Content:    responseToolOutputToChatContent(item["output"]),
		})
		return nil
	case responsesInputTypeCustomToolOutput, responsesInputTypeToolSearchOutput:
		b.flushPendingToolCalls()
		b.messages = append(b.messages, dto.Message{
			Role:       "tool",
			ToolCallId: responsesCallID(item),
			Content:    responsesWholeItemToToolContent(item),
		})
		return nil
	case "reasoning":
		// reasoning 条目一律先进入 pending 池，前向附挂到其后的 message /
		// function_call（cc-switch 同款，避免把新一轮思考错拼进旧消息）。
		b.appendReasoning(responsesReasoningItemText(item))
		return nil
	case "input_text", "input_image", "input_file", "input_audio", "input_video":
		// 简化的 content-part 型条目（自带 role）：把条目本身作为 content part
		// 处理，文本/图片/文件等按要求改写。
		b.flushPendingToolCalls()
		role := responsesItemRoleToChatRole(item)
		content, err := responsesContentPartsToChatContent([]any{item})
		if err != nil {
			return err
		}
		b.appendTurnMessage(role, content, item)
		return nil
	}

	// message 型条目及未知条目：带 role/content 视为普通消息。
	if strings.TrimSpace(kitutil.Interface2String(item["role"])) != "" || item["content"] != nil {
		b.flushPendingToolCalls()
		role := responsesItemRoleToChatRole(item)
		content, err := responsesInputContentToChatContent(item["content"])
		if err != nil {
			return err
		}
		b.appendTurnMessage(role, content, item)
		return nil
	}
	// 惰性条目（无 role 无 content）：关闭未决 tool-call 批次。
	b.flushPendingToolCalls()
	return nil
}

func (b *chatMessageBuilder) appendTurnMessage(role string, content any, item map[string]any) {
	if role == "assistant" {
		// assistant 消息自身可再携带 embedded reasoning（message 关的
		// reasoning_content / reasoning 字段），先并入池再挂载。
		b.appendUniqueReasoning(responsesItemReasoningText(item))
		msg := dto.Message{Role: "assistant", Content: content}
		b.attachPendingReasoningToAssistant(&msg)
		b.messages = append(b.messages, msg)
		b.lastAssistantIndex = len(b.messages) - 1
		return
	}
	// 非 assistant 回合边界（user 等）：pending reasoning 不跨越泄漏，回溯
	// 附挂到上一条 assistant；无上一条 assistant 时自然丢弃。
	b.backAttachPendingReasoning()
	b.messages = append(b.messages, dto.Message{Role: role, Content: content})
	b.lastAssistantIndex = -1
}

func (b *chatMessageBuilder) flushPendingToolCalls() {
	if len(b.pendingToolCalls) == 0 {
		return
	}
	msg := dto.Message{Role: "assistant"}
	toolCalls, err := kitutil.Marshal(b.pendingToolCalls)
	if err == nil {
		msg.ToolCalls = toolCalls
	}
	b.pendingToolCalls = nil
	b.attachPendingReasoningToAssistant(&msg)
	b.messages = append(b.messages, msg)
	b.lastAssistantIndex = len(b.messages) - 1
}

func (b *chatMessageBuilder) attachPendingReasoningToAssistant(msg *dto.Message) {
	if b.pendingReasoning == nil {
		return
	}
	reasoning := strings.TrimSpace(*b.pendingReasoning)
	b.pendingReasoning = nil
	if reasoning == "" {
		return
	}
	appendMessageReasoningContent(msg, reasoning)
}

func (b *chatMessageBuilder) backAttachPendingReasoning() {
	if b.pendingReasoning == nil {
		return
	}
	reasoning := strings.TrimSpace(*b.pendingReasoning)
	b.pendingReasoning = nil
	if reasoning == "" || b.lastAssistantIndex < 0 || b.lastAssistantIndex >= len(b.messages) {
		return
	}
	last := &b.messages[b.lastAssistantIndex]
	if last.Role != "assistant" {
		return
	}
	appendMessageReasoningContent(last, reasoning)
}

// appendReasoning 直接累加（reasoning 条目之间用空行分隔）。
func (b *chatMessageBuilder) appendReasoning(reasoning string) {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		return
	}
	if b.pendingReasoning != nil && *b.pendingReasoning != "" {
		*b.pendingReasoning += "\n\n" + reasoning
	} else {
		b.pendingReasoning = kitutil.GetPointer(reasoning)
	}
}

// appendUniqueReasoning 去重累加（同一工具调用批次里重复回传的思考不叠加）。
func (b *chatMessageBuilder) appendUniqueReasoning(reasoning string) {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		return
	}
	if b.pendingReasoning != nil && strings.Contains(*b.pendingReasoning, reasoning) {
		return
	}
	b.appendReasoning(reasoning)
}

// backfillToolCallReasoning 在所有 input 处理完毕后执行：kimi/Moonshot、DeepSeek
// 等 thinking 模型要求每条带 tool_calls 的 assistant 消息必须携带非空
// reasoning_content，历史恢复 miss 时补占位，避免上游报错。
func (b *chatMessageBuilder) backfillToolCallReasoning() {
	for i := range b.messages {
		msg := &b.messages[i]
		if msg.Role != "assistant" {
			continue
		}
		if msg.ReasoningContent != nil && strings.TrimSpace(*msg.ReasoningContent) != "" {
			continue
		}
		if len(msg.ParseToolCalls()) == 0 {
			continue
		}
		msg.ReasoningContent = kitutil.GetPointer("tool call")
	}
}

func appendMessageReasoningContent(msg *dto.Message, reasoning string) {
	if msg.ReasoningContent != nil && strings.TrimSpace(*msg.ReasoningContent) != "" {
		msg.ReasoningContent = kitutil.GetPointer(*msg.ReasoningContent + "\n\n" + reasoning)
	} else {
		msg.ReasoningContent = kitutil.GetPointer(reasoning)
	}
}

// responsesItemRoleToChatRole 把 Responses 消息 role 映射为 chat role
// （developer/system/latest_reminder 等统一归一，cc-switch 同款）。
func responsesItemRoleToChatRole(item map[string]any) string {
	switch strings.TrimSpace(kitutil.Interface2String(item["role"])) {
	case "assistant":
		return "assistant"
	case "system", "developer":
		return "system"
	case "tool":
		return "tool"
	default:
		return "user"
	}
}

// responsesItemReasoningText 从消息 / 工具调用条目提取 reasoning 文本
// （reasoning_content / reasoning 字符串或对象、reasoning_details）。
func responsesItemReasoningText(item map[string]any) string {
	for _, key := range []string{"reasoning_content", "reasoning"} {
		switch value := item[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return value
			}
		case map[string]any:
			for _, k := range []string{"content", "text", "summary"} {
				if text, ok := value[k].(string); ok && strings.TrimSpace(text) != "" {
					return text
				}
			}
		}
	}
	if details, ok := item["reasoning_details"]; ok {
		return responsesReasoningDetailsText(details)
	}
	return ""
}

func responsesReasoningDetailsText(value any) string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			return v
		}
	case []any:
		var sb strings.Builder
		for _, part := range v {
			if text := responsesReasoningDetailsText(part); text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(text)
			}
		}
		return sb.String()
	case map[string]any:
		for _, key := range []string{"text", "content", "summary"} {
			if text, ok := v[key].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
		if parts, ok := v["parts"].([]any); ok {
			return responsesReasoningDetailsText(parts)
		}
	}
	return ""
}

// responsesReasoningItemText 提取 reasoning 条目的摘要文本
// （reasoning_content / content / text，或 summary 字符串 / parts 数组）。
func responsesReasoningItemText(item map[string]any) string {
	for _, key := range []string{"reasoning_content", "content", "text"} {
		if text, ok := item[key].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	summary, ok := item["summary"]
	if !ok {
		return ""
	}
	switch s := summary.(type) {
	case string:
		return strings.TrimSpace(s)
	case []any:
		var sb strings.Builder
		for _, part := range s {
			switch p := part.(type) {
			case string:
				if strings.TrimSpace(p) != "" {
					if sb.Len() > 0 {
						sb.WriteString("\n\n")
					}
					sb.WriteString(p)
				}
			case map[string]any:
				for _, key := range []string{"text", "content"} {
					if text, ok := p[key].(string); ok && strings.TrimSpace(text) != "" {
						if sb.Len() > 0 {
							sb.WriteString("\n\n")
						}
						sb.WriteString(text)
						break
					}
				}
			}
		}
		return strings.TrimSpace(sb.String())
	}
	return ""
}

// collapseSystemMessagesToHead 把散落在 messages 各处的字符串类 system 消息
// 合并为一条置于头部（空 system 丢弃），非字符串内容的 system 消息原样保留。
func collapseSystemMessagesToHead(messages []dto.Message) []dto.Message {
	var systemTexts []string
	rest := make([]dto.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "system" {
			if text, ok := msg.Content.(string); ok && strings.TrimSpace(text) != "" {
				systemTexts = append(systemTexts, text)
				continue
			}
		}
		rest = append(rest, msg)
	}

	out := make([]dto.Message, 0, len(rest)+1)
	if len(systemTexts) > 0 {
		out = append(out, dto.Message{Role: "system", Content: strings.Join(systemTexts, "\n\n")})
	}
	return append(out, rest...)
}

func responsesInputContentToChatContent(content any) (any, error) {
	if content == nil {
		return "", nil
	}

	switch value := content.(type) {
	case string:
		return value, nil
	case []any:
		return responsesContentPartsToChatContent(value)
	case []map[string]any:
		parts := make([]any, 0, len(value))
		for _, part := range value {
			parts = append(parts, part)
		}
		return responsesContentPartsToChatContent(parts)
	default:
		return content, nil
	}
}

func responsesContentPartsToChatContent(parts []any) (any, error) {
	chatParts := make([]any, 0, len(parts))
	var textOnly strings.Builder
	onlyText := true

	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			onlyText = false
			chatParts = append(chatParts, rawPart)
			continue
		}

		partType := strings.TrimSpace(kitutil.Interface2String(part["type"]))
		switch partType {
		case "input_text", "output_text", "text":
			text := kitutil.Interface2String(part["text"])
			textOnly.WriteString(text)
			chatParts = append(chatParts, map[string]any{
				"type": dto.ContentTypeText,
				"text": text,
			})
		case "input_image":
			onlyText = false
			chatParts = append(chatParts, map[string]any{
				"type":      dto.ContentTypeImageURL,
				"image_url": responsesImagePartToChatImageURL(part),
			})
		case "input_file":
			onlyText = false
			chatParts = append(chatParts, map[string]any{
				"type": dto.ContentTypeFile,
				"file": responsesFilePartToChatFile(part),
			})
		case "input_audio":
			onlyText = false
			chatParts = append(chatParts, map[string]any{
				"type":        dto.ContentTypeInputAudio,
				"input_audio": responsesPartPayload(part, "input_audio"),
			})
		case "input_video":
			onlyText = false
			chatParts = append(chatParts, map[string]any{
				"type":      dto.ContentTypeVideoUrl,
				"video_url": responsesVideoPartToChatVideoURL(part),
			})
		default:
			onlyText = false
			chatParts = append(chatParts, part)
		}
	}

	if onlyText {
		return textOnly.String(), nil
	}
	return chatParts, nil
}

func responsesFunctionCallItemToChatToolCall(item map[string]any) (dto.ToolCallRequest, error) {
	name := strings.TrimSpace(kitutil.Interface2String(item["name"]))
	if name == "" {
		return dto.ToolCallRequest{}, errors.New("function_call item is missing name")
	}
	return dto.ToolCallRequest{
		ID:   responsesCallID(item),
		Type: "function",
		Function: dto.FunctionRequest{
			Name:      name,
			Arguments: responsesArgumentsString(item["arguments"]),
		},
	}, nil
}

// responsesCustomToolCallItemToChatToolCall 把 custom_tool_call / tool_search_call
// 条目转成 chat tool_call。tool_search 以函数工具代理名呈现（cc-switch 同款），
// 普通 custom 工具保留自定义类型；两者均以 call_id（缺省用 id）为键，便于
// 与后续 tool 结果消息配对。
func responsesCustomToolCallItemToChatToolCall(item map[string]any, itemType string) (dto.ToolCallRequest, error) {
	raw, err := kitutil.Marshal(item)
	if err != nil {
		return dto.ToolCallRequest{}, err
	}
	if itemType == responsesInputTypeToolSearchCall {
		return dto.ToolCallRequest{
			ID:   responsesCallID(item),
			Type: "function",
			Function: dto.FunctionRequest{
				Name:      "tool_search",
				Arguments: responsesArgumentsString(item["arguments"]),
			},
		}, nil
	}
	return dto.ToolCallRequest{
		ID:     responsesCallID(item),
		Type:   dto.CustomType,
		Custom: raw,
		Function: dto.FunctionRequest{
			Name:      strings.TrimSpace(kitutil.Interface2String(item["name"])),
			Arguments: responsesArgumentsString(item["input"]),
		},
	}, nil
}

// responsesWholeItemToToolContent 把 custom_tool_call_output / tool_search_output
// 的完整条目 JSON 作为 tool 消息内容（cc-switch 同款表示，供上游透传）。
func responsesWholeItemToToolContent(item map[string]any) any {
	raw, err := kitutil.Marshal(item)
	if err != nil {
		return fmt.Sprintf("%v", item)
	}
	return string(raw)
}

func responsesRequestToolsToChat(raw json.RawMessage) ([]dto.ToolCallRequest, error) {
	if !rawJSONPresent(raw) {
		return nil, nil
	}

	var tools []map[string]any
	if err := kitutil.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("invalid tools: %w", err)
	}

	out := make([]dto.ToolCallRequest, 0, len(tools))
	for _, tool := range tools {
		toolType := strings.TrimSpace(kitutil.Interface2String(tool["type"]))
		if toolType == "function" {
			out = append(out, dto.ToolCallRequest{
				Type: "function",
				Function: dto.FunctionRequest{
					Name:        strings.TrimSpace(kitutil.Interface2String(tool["name"])),
					Description: kitutil.Interface2String(tool["description"]),
					Parameters:  tool["parameters"],
				},
			})
			continue
		}

		rawTool, err := kitutil.Marshal(tool)
		if err != nil {
			return nil, err
		}
		out = append(out, dto.ToolCallRequest{
			Type:   toolType,
			Custom: rawTool,
		})
	}
	return out, nil
}

func responsesRequestToolChoiceToChat(raw json.RawMessage) (any, error) {
	if !rawJSONPresent(raw) {
		return nil, nil
	}
	if kitutil.GetJsonType(raw) == "string" {
		var choice string
		if err := kitutil.Unmarshal(raw, &choice); err != nil {
			return nil, fmt.Errorf("invalid tool_choice: %w", err)
		}
		return choice, nil
	}

	var choice map[string]any
	if err := kitutil.Unmarshal(raw, &choice); err != nil {
		return nil, fmt.Errorf("invalid tool_choice: %w", err)
	}
	if kitutil.Interface2String(choice["type"]) == "function" {
		name := strings.TrimSpace(kitutil.Interface2String(choice["name"]))
		if name != "" {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": name,
				},
			}, nil
		}
	}
	return choice, nil
}

func RequestToolChoiceToChat(raw json.RawMessage) (any, error) {
	return responsesRequestToolChoiceToChat(raw)
}

func responsesRequestTextToChatResponseFormat(raw json.RawMessage) (*dto.ResponseFormat, error) {
	if !rawJSONPresent(raw) {
		return nil, nil
	}

	var textConfig map[string]any
	if err := kitutil.Unmarshal(raw, &textConfig); err != nil {
		return nil, fmt.Errorf("invalid text config: %w", err)
	}
	format, ok := textConfig["format"].(map[string]any)
	if !ok {
		return nil, nil
	}

	formatType := strings.TrimSpace(kitutil.Interface2String(format["type"]))
	if formatType == "" {
		return nil, nil
	}

	out := &dto.ResponseFormat{Type: formatType}
	if formatType == "json_schema" {
		schemaRaw, err := kitutil.Marshal(format)
		if err != nil {
			return nil, err
		}
		out.JsonSchema = schemaRaw
	}
	return out, nil
}

func RequestTextToChatResponseFormat(raw json.RawMessage) (*dto.ResponseFormat, error) {
	return responsesRequestTextToChatResponseFormat(raw)
}

func responsesImagePartToChatImageURL(part map[string]any) any {
	if imageURL, ok := part["image_url"]; ok {
		return imageURL
	}
	imageURL := map[string]any{}
	for _, key := range []string{"url", "file_id", "detail"} {
		if value, ok := part[key]; ok {
			imageURL[key] = value
		}
	}
	if len(imageURL) == 0 {
		return part
	}
	return imageURL
}

func responsesFilePartToChatFile(part map[string]any) any {
	if file, ok := part["file"]; ok {
		return file
	}
	file := map[string]any{}
	for _, key := range []string{"file_id", "file_data", "filename", "file_url"} {
		if value, ok := part[key]; ok {
			file[key] = value
		}
	}
	if len(file) == 0 {
		return part
	}
	return file
}

func responsesVideoPartToChatVideoURL(part map[string]any) any {
	if videoURL, ok := part["video_url"]; ok {
		if videoURLMap, ok := videoURL.(map[string]any); ok {
			if url := kitutil.Interface2String(videoURLMap["url"]); url != "" {
				return url
			}
		}
		return videoURL
	}
	if url := kitutil.Interface2String(part["url"]); url != "" {
		return url
	}
	return responsesPartPayload(part, "video_url")
}

func responsesPartPayload(part map[string]any, key string) any {
	if value, ok := part[key]; ok {
		return value
	}
	payload := make(map[string]any, len(part))
	for k, value := range part {
		if k == "type" {
			continue
		}
		payload[k] = value
	}
	return payload
}

func responsesCallID(item map[string]any) string {
	callID := strings.TrimSpace(kitutil.Interface2String(item["call_id"]))
	if callID != "" {
		return callID
	}
	return strings.TrimSpace(kitutil.Interface2String(item["id"]))
}

func CallID(item map[string]any) string {
	return responsesCallID(item)
}

func responsesArgumentsString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		raw, err := kitutil.Marshal(v)
		if err != nil {
			return kitutil.Interface2String(v)
		}
		return string(raw)
	}
}

func responseToolOutputToChatContent(value any) any {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		raw, err := kitutil.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(raw)
	}
}

func responsesRawFloat(raw json.RawMessage) (*float64, error) {
	if !rawJSONPresent(raw) {
		return nil, nil
	}
	var value float64
	if err := kitutil.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func responsesJSONString(raw json.RawMessage) (string, error) {
	if kitutil.GetJsonType(raw) != "string" {
		return string(raw), nil
	}
	var value string
	if err := kitutil.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func rawJSONPresent(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	return kitutil.GetJsonType(raw) != "null"
}

func JSONString(raw json.RawMessage) (string, error) {
	return responsesJSONString(raw)
}

func RawJSONPresent(raw json.RawMessage) bool {
	return rawJSONPresent(raw)
}
