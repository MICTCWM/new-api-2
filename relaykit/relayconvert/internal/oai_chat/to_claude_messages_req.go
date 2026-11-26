package oaichat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"context"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	relaymedia "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/media"
	sharedclaude "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/claude"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
)

const (
	webSearchMaxUsesLow    = 1
	webSearchMaxUsesMedium = 5
	webSearchMaxUsesHigh   = 10
)

type openRouterRequestReasoning struct {
	Enabled   bool   `json:"enabled"`
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Exclude   bool   `json:"exclude,omitempty"`
}

func OpenAIChatRequestToClaudeMessages(c context.Context, info convmeta.Meta, textRequest dto.GeneralOpenAIRequest) (*dto.ClaudeRequest, error) {
	opts := convmeta.OptionsOf(info)
	claudeTools := make([]any, 0, len(textRequest.Tools))
	claudeToolNames := make(map[string]struct{}, len(textRequest.Tools))
	addClaudeTool := func(tool dto.ToolCallRequest) {
		if _, ok := tool.Function.Parameters.(map[string]any); !ok && tool.Type != "function" {
			return
		}
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			return
		}
		if _, exists := claudeToolNames[name]; exists {
			return
		}
		claudeToolNames[name] = struct{}{}
		claudeTools = append(claudeTools, &dto.Tool{
			Name:        name,
			Description: tool.Function.Description,
			InputSchema: sharedclaude.FunctionParametersToInputSchema(tool.Function.Parameters),
		})
	}
	for _, tool := range textRequest.Tools {
		addClaudeTool(tool)
	}
	// Some compatible providers allow declaring tools inside a system message.
	// Those declarations are not part of the top-level OpenAI tools array, so
	// collect them into Claude's request-level tools instead of dropping them.
	for _, message := range textRequest.Messages {
		if len(message.Tools) == 0 {
			continue
		}
		if message.Role != "system" {
			return nil, errors.New("dynamic tools are only supported on system messages")
		}
		if !isEmptyOpenAIContent(message.Content) {
			return nil, errors.New("system message cannot contain both content and tools")
		}
		for _, tool := range message.Tools {
			addClaudeTool(tool)
		}
	}

	if textRequest.WebSearchOptions != nil {
		webSearchTool := dto.ClaudeWebSearchTool{
			Type: "web_search_20250305",
			Name: "web_search",
		}

		if textRequest.WebSearchOptions.UserLocation != nil {
			anthropicUserLocation := &dto.ClaudeWebSearchUserLocation{
				Type: "approximate",
			}

			var userLocationMap map[string]interface{}
			if err := kitutil.Unmarshal(textRequest.WebSearchOptions.UserLocation, &userLocationMap); err == nil {
				if approximateData, ok := userLocationMap["approximate"].(map[string]interface{}); ok {
					if timezone, ok := approximateData["timezone"].(string); ok && timezone != "" {
						anthropicUserLocation.Timezone = timezone
					}
					if country, ok := approximateData["country"].(string); ok && country != "" {
						anthropicUserLocation.Country = country
					}
					if region, ok := approximateData["region"].(string); ok && region != "" {
						anthropicUserLocation.Region = region
					}
					if city, ok := approximateData["city"].(string); ok && city != "" {
						anthropicUserLocation.City = city
					}
				}
			}

			webSearchTool.UserLocation = anthropicUserLocation
		}

		switch textRequest.WebSearchOptions.SearchContextSize {
		case "low":
			webSearchTool.MaxUses = webSearchMaxUsesLow
		case "medium":
			webSearchTool.MaxUses = webSearchMaxUsesMedium
		case "high":
			webSearchTool.MaxUses = webSearchMaxUsesHigh
		}

		claudeTools = append(claudeTools, &webSearchTool)
	}

	claudeRequest := dto.ClaudeRequest{
		Model:         textRequest.Model,
		StopSequences: nil,
		Temperature:   textRequest.Temperature,
	}
	if len(claudeTools) > 0 {
		claudeRequest.Tools = claudeTools
	}
	if maxTokens := textRequest.GetMaxTokens(); maxTokens > 0 {
		claudeRequest.MaxTokens = kitutil.GetPointer(maxTokens)
	}
	if textRequest.TopP != nil {
		claudeRequest.TopP = kitutil.GetPointer(*textRequest.TopP)
	}
	if textRequest.TopK != nil {
		claudeRequest.TopK = kitutil.GetPointer(*textRequest.TopK)
	}
	if textRequest.IsStream(nil) {
		claudeRequest.Stream = kitutil.GetPointer(true)
	}

	if textRequest.ToolChoice != nil || textRequest.ParallelTooCalls != nil {
		claudeToolChoice := sharedclaude.MapOpenAIToolChoice(textRequest.ToolChoice, textRequest.ParallelTooCalls)
		if claudeToolChoice != nil {
			claudeRequest.ToolChoice = claudeToolChoice
		}
	}

	if claudeRequest.MaxTokens == nil || *claudeRequest.MaxTokens == 0 {
		if defaultMaxTokens, configured := opts.Claude.DefaultMaxTokensFor(textRequest.Model); configured {
			value := uint(defaultMaxTokens)
			claudeRequest.MaxTokens = &value
		}
	}

	if baseModel, effortLevel, ok := reasoning.TrimEffortSuffix(textRequest.Model); ok && effortLevel != "" &&
		(strings.HasPrefix(textRequest.Model, "claude-opus-4-6") ||
			strings.HasPrefix(textRequest.Model, "claude-opus-4-7") ||
			strings.HasPrefix(textRequest.Model, "claude-opus-4-8")) {
		claudeRequest.Model = baseModel
		claudeRequest.Thinking = &dto.Thinking{
			Type: "adaptive",
		}
		claudeRequest.OutputConfig = json.RawMessage(fmt.Sprintf(`{"effort":"%s"}`, effortLevel))
		if strings.HasPrefix(baseModel, "claude-opus-4-7") ||
			strings.HasPrefix(baseModel, "claude-opus-4-8") {
			claudeRequest.Thinking.Display = "summarized"
			claudeRequest.Temperature = nil
			claudeRequest.TopP = nil
			claudeRequest.TopK = nil
		} else {
			claudeRequest.TopP = nil
			claudeRequest.Temperature = kitutil.GetPointer[float64](1.0)
		}
	} else if opts.Claude.ThinkingAdapterEnabled &&
		strings.HasSuffix(textRequest.Model, "-thinking") {

		trimmedModel := strings.TrimSuffix(textRequest.Model, "-thinking")
		if strings.HasPrefix(trimmedModel, "claude-opus-4-7") ||
			strings.HasPrefix(trimmedModel, "claude-opus-4-8") {
			claudeRequest.Thinking = &dto.Thinking{Type: "adaptive", Display: "summarized"}
			claudeRequest.OutputConfig = json.RawMessage(`{"effort":"high"}`)
			claudeRequest.Temperature = nil
			claudeRequest.TopP = nil
			claudeRequest.TopK = nil
		} else {
			if claudeRequest.MaxTokens == nil || *claudeRequest.MaxTokens < 1280 {
				claudeRequest.MaxTokens = kitutil.GetPointer[uint](1280)
			}

			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: kitutil.GetPointer[int](int(float64(*claudeRequest.MaxTokens) * opts.Claude.ThinkingAdapterBudgetTokensPercentage)),
			}
			claudeRequest.TopP = nil
			claudeRequest.Temperature = kitutil.GetPointer[float64](1.0)
		}
		if !opts.ShouldPreserveThinkingSuffix(textRequest.Model) {
			claudeRequest.Model = trimmedModel
		}
	}

	if textRequest.ReasoningEffort != "" {
		switch textRequest.ReasoningEffort {
		case "low":
			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: kitutil.GetPointer[int](1280),
			}
		case "medium":
			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: kitutil.GetPointer[int](2048),
			}
		case "high":
			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: kitutil.GetPointer[int](4096),
			}
		}
	}

	if textRequest.Reasoning != nil {
		var reasoningConfig openRouterRequestReasoning
		if err := kitutil.Unmarshal(textRequest.Reasoning, &reasoningConfig); err != nil {
			return nil, err
		}

		budgetTokens := reasoningConfig.MaxTokens
		if budgetTokens > 0 {
			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: &budgetTokens,
			}
		}
	}

	if textRequest.Stop != nil {
		switch stop := textRequest.Stop.(type) {
		case string:
			claudeRequest.StopSequences = []string{stop}
		case []string:
			claudeRequest.StopSequences = stop
		case []any:
			stopSequences := make([]string, 0, len(stop))
			for _, item := range stop {
				if value, ok := item.(string); ok {
					stopSequences = append(stopSequences, value)
				}
			}
			claudeRequest.StopSequences = stopSequences
		}
	}

	formatMessages := make([]dto.Message, 0, len(textRequest.Messages))
	for _, message := range textRequest.Messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		// OpenAI's developer role is the newer spelling of the system
		// instruction role. Claude only accepts system/user/assistant, so
		// normalize it before the message reaches the upstream schema.
		if role == "developer" {
			role = "system"
		}
		if role != "system" && role != "user" && role != "assistant" && role != "tool" {
			continue
		}

		fmtMessage := dto.Message{
			Role:    role,
			Content: message.Content,
		}
		if role == "tool" {
			fmtMessage.ToolCallId = message.ToolCallId
		}
		if role == "assistant" && hasOpenAIToolCalls(message.ToolCalls) {
			fmtMessage.ToolCalls = message.ToolCalls
		}
		// Do not invent placeholder text for empty messages. An assistant
		// message with tool_calls legitimately has no text; empty user/assistant
		// messages carry no information and are omitted instead of being turned
		// into "...". Message boundaries between same-role turns are also kept:
		// Anthropic accepts consecutive same-role turns and combines them itself.
		if role != "tool" && !hasOpenAIToolCalls(fmtMessage.ToolCalls) && isEmptyOpenAIContent(fmtMessage.Content) {
			continue
		}
		formatMessages = append(formatMessages, fmtMessage)
	}

	claudeMessages := make([]dto.ClaudeMessage, 0)
	isFirstMessage := true
	var systemMessages []dto.ClaudeMediaMessage

	for _, message := range formatMessages {
		if message.Role == "system" {
			if message.IsStringContent() {
				if text := message.StringContent(); text != "" {
					systemMessages = append(systemMessages, dto.ClaudeMediaMessage{
						Type: "text",
						Text: kitutil.GetPointer[string](text),
					})
				}
			} else {
				for _, ctx := range message.ParseContent() {
					if ctx.Type == "text" && ctx.Text != "" {
						systemMessages = append(systemMessages, dto.ClaudeMediaMessage{
							Type: "text",
							Text: kitutil.GetPointer[string](ctx.Text),
						})
					}
				}
			}
			continue
		}

		if isFirstMessage {
			isFirstMessage = false
			if message.Role != "user" {
				claudeMessage := dto.ClaudeMessage{
					Role: "user",
					Content: []dto.ClaudeMediaMessage{
						{
							Type: "text",
							Text: kitutil.GetPointer[string]("..."),
						},
					},
				}
				claudeMessages = append(claudeMessages, claudeMessage)
			}
		}

		claudeMessage := dto.ClaudeMessage{
			Role: message.Role,
		}
		if message.Role == "tool" {
			toolResult := dto.ClaudeMediaMessage{
				Type:      "tool_result",
				ToolUseId: message.ToolCallId,
				Content:   claudeToolResultContentFromOpenAI(message.Content),
			}
			if len(claudeMessages) > 0 && claudeMessages[len(claudeMessages)-1].Role == "user" {
				lastClaudeMessage := claudeMessages[len(claudeMessages)-1]
				blocks, ok := claudeMessageContentBlocks(lastClaudeMessage.Content)
				if ok {
					lastClaudeMessage.Content = append(blocks, toolResult)
					claudeMessages[len(claudeMessages)-1] = lastClaudeMessage
					continue
				}
			}

			claudeMessage.Role = "user"
			claudeMessage.Content = []dto.ClaudeMediaMessage{toolResult}
		} else if message.IsStringContent() && !hasOpenAIToolCalls(message.ToolCalls) {
			claudeMessage.Content = message.StringContent()
		} else {
			claudeMediaMessages := make([]dto.ClaudeMediaMessage, 0)
			for _, mediaMessage := range message.ParseContent() {
				switch mediaMessage.Type {
				case "text":
					if mediaMessage.Text != "" {
						claudeMediaMessages = append(claudeMediaMessages, dto.ClaudeMediaMessage{
							Type: "text",
							Text: kitutil.GetPointer[string](mediaMessage.Text),
						})
					}
				default:
					source := mediaMessage.ToFileSource()
					if source == nil {
						continue
					}
					base64Data, mimeType, err := relaymedia.ResolveBase64Data(c, source, "formatting image for Claude")
					if err != nil {
						return nil, fmt.Errorf("get file data failed: %s", err.Error())
					}
					claudeMediaMessage := dto.ClaudeMediaMessage{
						Source: &dto.ClaudeMessageSource{
							Type: "base64",
						},
					}
					if strings.HasPrefix(mimeType, "application/pdf") {
						claudeMediaMessage.Type = "document"
					} else {
						claudeMediaMessage.Type = "image"
					}

					claudeMediaMessage.Source.MediaType = mimeType
					claudeMediaMessage.Source.Data = base64Data
					claudeMediaMessages = append(claudeMediaMessages, claudeMediaMessage)
					continue
				}
			}

			if hasOpenAIToolCalls(message.ToolCalls) {
				for _, toolCall := range message.ParseToolCalls() {
					inputObj := make(map[string]any)
					if args := toolCall.Function.Arguments; args != "" {
						if err := kitutil.Unmarshal([]byte(args), &inputObj); err != nil {
							kitutil.LogInfo("tool call function arguments is not a map[string]any: " + fmt.Sprintf("%v", toolCall.Function.Arguments))
						}
					}
					claudeMediaMessages = append(claudeMediaMessages, dto.ClaudeMediaMessage{
						Type:  "tool_use",
						Id:    toolCall.ID,
						Name:  toolCall.Function.Name,
						Input: inputObj,
					})
				}
			}
			claudeMessage.Content = claudeMediaMessages
		}
		claudeMessages = append(claudeMessages, claudeMessage)
	}

	if len(systemMessages) > 0 {
		claudeRequest.System = systemMessages
	}

	claudeRequest.Prompt = ""
	claudeRequest.Messages = claudeMessages
	// Checked last so every injection path (default hook, thinking adapter
	// floor) has had its chance to satisfy the required field.
	if claudeRequest.MaxTokens == nil {
		return nil, sharedclaude.ErrMissingMaxTokens
	}
	return &claudeRequest, nil
}

func hasOpenAIToolCalls(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed != "[]"
}

func isEmptyOpenAIContent(content any) bool {
	if content == nil {
		return true
	}
	switch value := content.(type) {
	case string:
		return value == ""
	case []any:
		return len(value) == 0
	case []dto.MediaContent:
		return len(value) == 0
	case []dto.ClaudeMediaMessage:
		return len(value) == 0
	default:
		return false
	}
}

func claudeMessageContentBlocks(content any) ([]dto.ClaudeMediaMessage, bool) {
	switch value := content.(type) {
	case nil:
		return []dto.ClaudeMediaMessage{}, true
	case string:
		if value == "" {
			return []dto.ClaudeMediaMessage{}, true
		}
		return []dto.ClaudeMediaMessage{{Type: "text", Text: kitutil.GetPointer(value)}}, true
	case []dto.ClaudeMediaMessage:
		return value, true
	case []any:
		blocks, err := kitutil.Any2Type[[]dto.ClaudeMediaMessage](value)
		if err == nil {
			return blocks, true
		}
	}
	return nil, false
}

func claudeToolResultContentFromOpenAI(content any) any {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []any:
		blocks := make([]dto.ClaudeMediaMessage, 0, len(value))
		for _, item := range value {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "text", "input_text":
				if text, ok := part["text"].(string); ok {
					blocks = append(blocks, dto.ClaudeMediaMessage{Type: "text", Text: kitutil.GetPointer(text)})
				}
			case "image_url":
				url := ""
				switch image := part["image_url"].(type) {
				case string:
					url = image
				case map[string]any:
					url, _ = image["url"].(string)
				}
				if url != "" {
					blocks = append(blocks, dto.ClaudeMediaMessage{Type: "image", Source: &dto.ClaudeMessageSource{Type: "url", Url: url}})
				}
			}
		}
		if len(blocks) > 0 {
			return blocks
		}
		encoded, err := kitutil.Marshal(value)
		if err == nil {
			return string(encoded)
		}
		return ""
	default:
		encoded, err := kitutil.Marshal(value)
		if err == nil {
			return string(encoded)
		}
		return fmt.Sprintf("%v", value)
	}
}
