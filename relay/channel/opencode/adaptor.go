package opencode

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const (
	CanonicalURL = "https://opencode.ai/zen/go/v1/chat/completions"
	Model        = "deepseek-v4-flash"
	GlmModel     = "glm-5.3-flash"

	// SessionHeader is required by OpenCode Go for routing and prompt caching.
	SessionHeader = "x-opencode-session"
	// UserAgent identifies this gateway as a coding-agent style client.
	// OpenCode rejects generic SDK/HTTP library user agents.
	UserAgent = "ByteIQ-API"
)

var ModelList = []string{Model, GlmModel}

var ChannelName = "opencode"

func IsNativeModel(modelName string) bool {
	switch strings.TrimSpace(modelName) {
	case Model, GlmModel:
		return true
	default:
		return false
	}
}

type Adaptor struct {
	openai.Adaptor
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return "", types.NewError(errors.New("opencode channel: only chat completions is supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	return CanonicalURL, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	if strings.TrimSpace(info.ApiKey) == "" {
		return types.NewError(errors.New("opencode channel: API key is required"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if !strings.EqualFold(header.Get("Authorization"), "Bearer "+strings.TrimSpace(info.ApiKey)) {
		header.Set("Authorization", "Bearer "+strings.TrimSpace(info.ApiKey))
	}
	header.Set("Content-Type", "application/json")
	if info.IsStream {
		header.Set("Accept", "text/event-stream")
	} else {
		header.Set("Accept", "application/json")
	}

	// OpenCode Go requires a stable session id per conversation.
	header.Set(SessionHeader, ResolveSessionID(c, info))
	header.Set("User-Agent", resolveUserAgent(c, info))
	return nil
}

// SanitizeUpstreamHeaders strips alternate OpenCode session header spellings
// from the final upstream request, keeping the session mechanism internal to
// the OpenCode hop regardless of header override passthrough configuration.
func (a *Adaptor) SanitizeUpstreamHeaders(header *http.Header) {
	for _, name := range service.OpencodeSessionHeaderSpellings {
		if strings.EqualFold(name, SessionHeader) {
			continue
		}
		header.Del(name)
	}
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, types.NewError(errors.New("opencode channel: request is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	isModelMapped := info != nil && info.ChannelMeta != nil && info.ChannelMeta.IsModelMapped
	if !isModelMapped {
		if request.Model != "" && !IsNativeModel(request.Model) {
			return nil, types.NewError(errors.New("opencode channel: unsupported model"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		if request.Model == "" {
			request.Model = Model
		}
	} else if strings.TrimSpace(request.Model) == "" {
		return nil, types.NewError(errors.New("opencode channel: mapped model is empty"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	// Keep session out of the JSON body forwarded to OpenCode; it belongs in
	// the x-opencode-session request header only.
	return a.Adaptor.ConvertOpenAIRequest(c, info, request)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, types.NewError(errors.New("opencode channel: /v1/messages is not supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, types.NewError(errors.New("opencode channel: Gemini endpoint is not supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, types.NewError(errors.New("opencode channel: audio endpoint is not supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, types.NewError(errors.New("opencode channel: image endpoint is not supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

// ResolveSessionID returns the OpenCode upstream session id.
// The client-supplied session value is used only as a conversation identity
// source; the returned value is a gateway-managed session affinitized by
// (conversation identity x channel id), never equals the client value, and
// is never empty.
func ResolveSessionID(c *gin.Context, info *relaycommon.RelayInfo) string {
	return service.ResolveOpencodeSession(c, info)
}

func resolveUserAgent(c *gin.Context, info *relaycommon.RelayInfo) string {
	clientUA := ""
	if c != nil && c.Request != nil {
		clientUA = strings.TrimSpace(c.Request.UserAgent())
	}
	if clientUA == "" && info != nil {
		for key, value := range info.RequestHeaders {
			if strings.EqualFold(key, "User-Agent") {
				clientUA = strings.TrimSpace(value)
				break
			}
		}
	}
	if clientUA != "" && !isGenericUserAgent(clientUA) {
		return clientUA
	}
	version := strings.TrimSpace(common.Version)
	if version == "" || version == "v0.0.0" {
		return UserAgent + "/1.0"
	}
	return UserAgent + "/" + strings.TrimPrefix(version, "v")
}

func isGenericUserAgent(ua string) bool {
	lower := strings.ToLower(ua)
	for _, marker := range []string{
		"go-http-client",
		"python-requests",
		"python-urllib",
		"aiohttp",
		"httpx",
		"curl/",
		"wget/",
		"postman",
		"insomnia",
		"openai/",
		"openai-python",
		"openai-node",
		"node-fetch",
		"axios/",
		"got/",
		"undici",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

var _ channel.Adaptor = (*Adaptor)(nil)
var _ channel.UpstreamHeaderSanitizer = (*Adaptor)(nil)
