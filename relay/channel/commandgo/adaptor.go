package commandgo

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

const (
	// BaseURL is the Command Code alpha API root used by the CLI agent protocol.
	BaseURL = "https://api.commandcode.ai/alpha"
	// CanonicalURL is the agent envelope endpoint.
	CanonicalURL = BaseURL + "/generate"

	// Model is the single canonical upstream model this channel exposes.
	Model = "deepseek/deepseek-v4.1-flash"

	// sessionIDContextKey holds the resolved thread/session id between request
	// conversion and header setup.
	sessionIDContextKey = "commandgo_session_id"
	// toolNameReverseContextKey holds this request's wire -> client tool-name
	// map so the response translator can restore client-visible tool names.
	toolNameReverseContextKey = "commandgo_tool_name_reverse"
)

var ModelList = []string{Model}

var ChannelName = "commandgo"

type Adaptor struct {
	openai.Adaptor
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return "", types.NewErrorWithStatusCode(errors.New("command go channel: only chat completions is supported"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	return CanonicalURL, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	accountKey := ResolveAccountKey(c, info)
	if accountKey == "" {
		return types.NewErrorWithStatusCode(errors.New("command go channel: API key is required"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	// Report the account fingerprint/lifecycle out of band. Detached and
	// throttled per key; failures never affect the relay request itself. The
	// reports use the canonical BaseURL (never a channel override) and the
	// channel's proxy-aware client so they follow the same route as the relay.
	// Client construction is deferred until a report is actually due, and a
	// build failure abandons the report instead of falling back to the default
	// client (which would bypass the channel proxy).
	// Channel tests must not mutate the account's report lifecycle.
	if info.HasChannelMeta() && !info.IsChannelTest {
		EnsureInitializedWithProxySettings(BaseURL, accountKey, info.ChannelSetting.Proxy, info.ChannelSetting)
	}

	sessionID := ""
	if c != nil {
		sessionID = strings.TrimSpace(c.GetString(sessionIDContextKey))
	}
	if sessionID == "" {
		sessionID = ResolveSessionID(c, accountKey, "")
	}
	profile := ProfileForAccount(accountKey)

	header.Set("Authorization", "Bearer "+accountKey)
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", "cli")
	header.Set("x-command-code-version", ProtocolVersion)
	header.Set("x-cli-environment", "production")
	header.Set("x-project-slug", SlugifyProjectPath(profile.ProjectDir))
	header.Set("x-taste-learning", "false")
	header.Set("x-session-id", sessionID)
	header.Set("traceparent", generateTraceparent())
	if c != nil && c.Request != nil && c.Request.Header.Get("x-cmd-zdr") == "1" {
		header.Set("x-cmd-zdr", "1")
	}
	// The CLI does not send an Accept header; the envelope's stream=true is the
	// only content-negotiation signal the upstream needs.
	header.Del("Accept")
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, types.NewErrorWithStatusCode(errors.New("command go channel: request is nil"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	isModelMapped := info != nil && info.ChannelMeta != nil && info.ChannelMeta.IsModelMapped
	model := strings.TrimSpace(request.Model)
	if isModelMapped {
		if model == "" && info != nil {
			model = strings.TrimSpace(info.UpstreamModelName)
		}
		if model == "" {
			return nil, types.NewErrorWithStatusCode(errors.New("command go channel: mapped model is empty"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	} else {
		if model != "" && model != Model {
			return nil, types.NewErrorWithStatusCode(errors.New("command go channel: unsupported model"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		if model == "" {
			model = Model
		}
	}

	applyChannelSystemPrompt(c, info, request)

	// Mirror the Command channel: the upstream does not accept reasoning_effort
	// "none", so request reasoning, then strip it from the outbound response.
	if strings.EqualFold(strings.TrimSpace(request.ReasoningEffort), "none") {
		request.ReasoningEffort = "high"
		common.SetContextKey(c, constant.ContextKeyCommandHideThinking, true)
	}

	accountKey := ResolveAccountKey(c, info)
	profile := ProfileForAccount(accountKey)
	sessionID := ResolveSessionID(c, accountKey, request.PromptCacheKey)
	if c != nil {
		c.Set(sessionIDContextKey, sessionID)
		if reverse := clientToolNameReverseMap(request); len(reverse) > 0 {
			c.Set(toolNameReverseContextKey, reverse)
		}
	}
	return BuildEnvelope(request, model, sessionID, profile.ProjectDir)
}

// applyChannelSystemPrompt reproduces the channel-level system prompt injection
// that the relay applies to *dto.GeneralOpenAIRequest after conversion. The
// Command Go adaptor returns an envelope map, so it must fold the prompt into
// the conversation before building the envelope.
func applyChannelSystemPrompt(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) {
	if info == nil || request == nil || strings.TrimSpace(info.ChannelSetting.SystemPrompt) == "" {
		return
	}
	systemRole := request.GetSystemRoleName()
	for i := range request.Messages {
		if request.Messages[i].Role != systemRole {
			continue
		}
		if !info.ChannelSetting.SystemPromptOverride {
			return
		}
		common.SetContextKey(c, constant.ContextKeySystemPromptOverride, true)
		if request.Messages[i].IsStringContent() {
			request.Messages[i].SetStringContent(info.ChannelSetting.SystemPrompt + "\n" + request.Messages[i].StringContent())
			return
		}
		contents := request.Messages[i].ParseContent()
		contents = append([]dto.MediaContent{{
			Type: dto.ContentTypeText,
			Text: info.ChannelSetting.SystemPrompt,
		}}, contents...)
		request.Messages[i].SetMediaContent(contents)
		return
	}
	request.Messages = append([]dto.Message{{
		Role:    systemRole,
		Content: info.ChannelSetting.SystemPrompt,
	}}, request.Messages...)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, types.NewErrorWithStatusCode(errors.New("command go channel: /v1/messages is not supported"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, types.NewErrorWithStatusCode(errors.New("command go channel: Gemini endpoint is not supported"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, types.NewErrorWithStatusCode(errors.New("command go channel: audio endpoint is not supported"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, types.NewErrorWithStatusCode(errors.New("command go channel: image endpoint is not supported"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

// MarkUpstreamAttempt is invoked by DoApiRequest immediately before the HTTP
// request is sent, after URL/header validation has succeeded. Local
// configuration errors therefore must not be attributed to the pool account.
func (a *Adaptor) MarkUpstreamAttempt(c *gin.Context) {
	common.SetContextKey(c, constant.ContextKeyCommandGoUpstreamAttempted, true)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

// ResolveAccountKey reads the pool-selected account key from the context and
// falls back to the channel API key so a single-account channel keeps working.
// Keys without a user_ prefix are accepted; the upstream decides validity.
func ResolveAccountKey(c *gin.Context, info *relaycommon.RelayInfo) string {
	if c != nil {
		if key := strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyCommandGoAccountKey)); key != "" {
			return key
		}
	}
	if info != nil {
		return strings.TrimSpace(info.ApiKey)
	}
	return ""
}

var _ channel.Adaptor = (*Adaptor)(nil)
