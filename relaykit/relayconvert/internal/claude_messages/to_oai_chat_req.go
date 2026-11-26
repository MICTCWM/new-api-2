package claudemessages

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
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

func ClaudeMessagesRequestToOpenAIChat(claudeRequest dto.ClaudeRequest, info convmeta.Meta) (*dto.GeneralOpenAIRequest, error) {
	openAIRequest := dto.GeneralOpenAIRequest{
		Model:       claudeRequest.Model,
		Temperature: claudeRequest.Temperature,
	}
	if claudeRequest.MaxTokens != nil {
		openAIRequest.MaxTokens = kitutil.GetPointer(*claudeRequest.MaxTokens)
	}
	if claudeRequest.TopP != nil {
		openAIRequest.TopP = kitutil.GetPointer(*claudeRequest.TopP)
	}
	if claudeRequest.TopK != nil {
		openAIRequest.TopK = kitutil.GetPointer(*claudeRequest.TopK)
	}
	if claudeRequest.Stream != nil {
		openAIRequest.Stream = kitutil.GetPointer(*claudeRequest.Stream)
	}

	isOpenRouter := convmeta.OptionsOf(info).OpenRouterDialect
	if isOpenRouter {
		if effort := claudeRequest.GetEfforts(); effort != "" {
			effortBytes, _ := kitutil.Marshal(effort)
			openAIRequest.Verbosity = effortBytes
		}
		if claudeRequest.Thinking != nil {
			var reasoningConfig openRouterRequestReasoning
			if claudeRequest.Thinking.Type == "enabled" {
				reasoningConfig = openRouterRequestReasoning{
					Enabled:   true,
					MaxTokens: claudeRequest.Thinking.GetBudgetTokens(),
				}
			} else if claudeRequest.Thinking.Type == "adaptive" {
				reasoningConfig = openRouterRequestReasoning{
					Enabled: true,
				}
			}
			reasoningJSON, err := kitutil.Marshal(reasoningConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal reasoning: %w", err)
			}
			openAIRequest.Reasoning = reasoningJSON
		}
	} else if info != nil {
		thinkingSuffix := "-thinking"
		if strings.HasSuffix(info.GetOriginModelName(), thinkingSuffix) &&
			!strings.HasSuffix(openAIRequest.Model, thinkingSuffix) {
			openAIRequest.Model = openAIRequest.Model + thinkingSuffix
		}
	}

	if len(claudeRequest.StopSequences) == 1 {
		openAIRequest.Stop = claudeRequest.StopSequences[0]
	} else if len(claudeRequest.StopSequences) > 1 {
		openAIRequest.Stop = claudeRequest.StopSequences
	}

	tools, _ := kitutil.Any2Type[[]dto.Tool](claudeRequest.Tools)
	openAITools := make([]dto.ToolCallRequest, 0)
	for _, claudeTool := range tools {
		openAITool := dto.ToolCallRequest{
			Type: "function",
			Function: dto.FunctionRequest{
				Name:        claudeTool.Name,
				Description: claudeTool.Description,
				Parameters:  claudeTool.InputSchema,
			},
		}
		openAITools = append(openAITools, openAITool)
	}
	openAIRequest.Tools = openAITools

	openAIMessages := make([]dto.Message, 0)
	if claudeRequest.System != nil {
		if claudeRequest.IsStringSystem() && claudeRequest.GetStringSystem() != "" {
			openAIMessage := dto.Message{
				Role: "system",
			}
			openAIMessage.SetStringContent(claudeRequest.GetStringSystem())
			openAIMessages = append(openAIMessages, openAIMessage)
		} else {
			systems := claudeRequest.ParseSystem()
			if len(systems) > 0 {
				openAIMessage := dto.Message{
					Role: "system",
				}
				isOpenRouterClaude := isOpenRouter && strings.HasPrefix(convmeta.UpstreamModelName(info), "anthropic/claude")
				if isOpenRouterClaude {
					systemMediaMessages := make([]dto.MediaContent, 0, len(systems))
					for _, system := range systems {
						message := dto.MediaContent{
							Type:         "text",
							Text:         system.GetText(),
							CacheControl: system.CacheControl,
						}
						systemMediaMessages = append(systemMediaMessages, message)
					}
					openAIMessage.SetMediaContent(systemMediaMessages)
				} else {
					systemParts := make([]string, 0, len(systems))
					for _, system := range systems {
						if system.Text != nil && *system.Text != "" {
							systemParts = append(systemParts, *system.Text)
						}
					}
					// Keep block boundaries as separators instead of concatenating the
					// blocks into a single run-on string. This preserves authored text
					// while staying compatible with upstreams that only accept a string
					// system message.
					openAIMessage.SetStringContent(strings.Join(systemParts, "\n\n"))
				}
				openAIMessages = append(openAIMessages, openAIMessage)
			}
		}
	}

	toolNames := make(map[string]string)
	for _, claudeMessage := range claudeRequest.Messages {
		openAIMessage := dto.Message{
			Role: claudeMessage.Role,
		}
		if claudeMessage.IsStringContent() {
			openAIMessage.SetStringContent(claudeMessage.GetStringContent())
		} else {
			content, err := claudeMessage.ParseContent()
			if err != nil {
				return nil, err
			}
			var toolCalls []dto.ToolCallRequest
			mediaMessages := make([]dto.MediaContent, 0, len(content))

			for _, mediaMsg := range content {
				switch mediaMsg.Type {
				case "text", "input_text":
					message := dto.MediaContent{
						Type:         "text",
						Text:         mediaMsg.GetText(),
						CacheControl: mediaMsg.CacheControl,
					}
					mediaMessages = append(mediaMessages, message)
				case "thinking":
					if mediaMsg.Thinking != nil && *mediaMsg.Thinking != "" {
						appendMessageReasoningContent(&openAIMessage, *mediaMsg.Thinking)
					}
				case "redacted_thinking":
					// Redacted thinking is intentionally opaque. There is no
					// equivalent OpenAI request field, so it cannot be forwarded.
				case "image":
					imageData := ""
					if mediaMsg.Source != nil {
						if mediaMsg.Source.Url != "" {
							imageData = mediaMsg.Source.Url
						} else if mediaMsg.Source.Data != nil {
							imageData = fmt.Sprintf("data:%s;base64,%s", mediaMsg.Source.MediaType, kitutil.Interface2String(mediaMsg.Source.Data))
						}
					}
					if imageData == "" {
						continue
					}
					mediaMessage := dto.MediaContent{
						Type:     "image_url",
						ImageUrl: &dto.MessageImageUrl{Url: imageData},
					}
					mediaMessages = append(mediaMessages, mediaMessage)
				case "document":
					if mediaMsg.Source == nil {
						continue
					}
					fileData := ""
					if mediaMsg.Source.Url != "" {
						fileData = mediaMsg.Source.Url
					} else if mediaMsg.Source.Data != nil {
						fileData = fmt.Sprintf("data:%s;base64,%s", mediaMsg.Source.MediaType, kitutil.Interface2String(mediaMsg.Source.Data))
					}
					if fileData == "" {
						continue
					}
					mediaMessages = append(mediaMessages, dto.MediaContent{
						Type: "file",
						File: map[string]any{
							"filename":  "document.pdf",
							"file_data": fileData,
						},
					})
				case "tool_use":
					if mediaMsg.Id != "" && mediaMsg.Name != "" {
						toolNames[mediaMsg.Id] = mediaMsg.Name
					}
					toolCall := dto.ToolCallRequest{
						ID:   mediaMsg.Id,
						Type: "function",
						Function: dto.FunctionRequest{
							Name:      mediaMsg.Name,
							Arguments: requestToJSONString(mediaMsg.Input),
						},
					}
					toolCalls = append(toolCalls, toolCall)
				case "tool_result":
					oaiToolMessage := dto.Message{
						Role:       "tool",
						ToolCallId: mediaMsg.ToolUseId,
					}
					if name := toolNames[mediaMsg.ToolUseId]; name != "" {
						oaiToolMessage.Name = kitutil.GetPointer(name)
					}
					oaiToolMessage.SetStringContent(claudeToolResultContent(mediaMsg))
					openAIMessages = append(openAIMessages, oaiToolMessage)
				}
			}

			if len(toolCalls) > 0 {
				openAIMessage.SetToolCalls(toolCalls)
			}
			// OpenAI assistant messages may carry both user-visible content and
			// tool_calls. Dropping the content here used to erase the assistant's
			// text whenever it also called a tool.
			if len(mediaMessages) > 0 {
				openAIMessage.SetMediaContent(mediaMessages)
			}
		}
		if len(openAIMessage.ParseContent()) > 0 || len(openAIMessage.ToolCalls) > 0 || openAIMessage.ReasoningContent != nil {
			openAIMessages = append(openAIMessages, openAIMessage)
		}
	}

	openAIRequest.Messages = openAIMessages
	return &openAIRequest, nil
}

func requestToJSONString(v interface{}) string {
	b, err := kitutil.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func appendMessageReasoningContent(message *dto.Message, reasoning string) {
	if reasoning == "" {
		return
	}
	if message.ReasoningContent != nil && strings.TrimSpace(*message.ReasoningContent) != "" {
		combined := *message.ReasoningContent + "\n\n" + reasoning
		message.ReasoningContent = &combined
		return
	}
	message.ReasoningContent = kitutil.GetPointer(reasoning)
}

func claudeToolResultContent(mediaMsg dto.ClaudeMediaMessage) string {
	if mediaMsg.IsStringContent() {
		return mediaMsg.GetStringContent()
	}
	mediaContents := mediaMsg.ParseMediaContent()
	textParts := make([]string, 0, len(mediaContents))
	structured := make([]dto.ClaudeMediaMessage, 0, len(mediaContents))
	for _, content := range mediaContents {
		if content.Type == "text" && content.GetText() != "" {
			textParts = append(textParts, content.GetText())
			continue
		}
		structured = append(structured, content)
	}
	if len(structured) == 0 {
		return strings.Join(textParts, "\n")
	}
	if len(textParts) > 0 {
		structured = append([]dto.ClaudeMediaMessage{{Type: "text", Text: kitutil.GetPointer(strings.Join(textParts, "\n"))}}, structured...)
	}
	encodedJSON, err := kitutil.Marshal(structured)
	if err != nil {
		return strings.Join(textParts, "\n")
	}
	return string(encodedJSON)
}
