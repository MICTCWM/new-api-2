package command

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

const (
	BaseURL      = "https://api.commandcode.ai/provider/v1"
	CanonicalURL = BaseURL + "/chat/completions"
	Model        = "deepseek/deepseek-v4-flash"
	ProModel     = "deepseek/deepseek-v4-pro"
	V41Model     = "deepseek/deepseek-v4.1-flash"
)

var ModelList = []string{Model, ProModel, V41Model}

var ChannelName = "command"

type Adaptor struct {
	openai.Adaptor
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return "", types.NewError(errors.New("command channel: only chat completions is supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	return CanonicalURL, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	if strings.TrimSpace(info.ApiKey) == "" {
		return types.NewError(errors.New("command channel: API key is required"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	header.Set("Authorization", "Bearer "+strings.TrimSpace(info.ApiKey))
	header.Set("Content-Type", "application/json")
	if info.IsStream {
		header.Set("Accept", "text/event-stream")
	} else {
		header.Set("Accept", "application/json")
	}
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, types.NewError(errors.New("command channel: request is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	isModelMapped := info != nil && info.ChannelMeta != nil && info.ChannelMeta.IsModelMapped
	if !isModelMapped {
		if request.Model != "" && request.Model != Model && request.Model != ProModel && request.Model != V41Model {
			return nil, types.NewError(errors.New("command channel: unsupported model"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		if request.Model == "" {
			request.Model = Model
		}
	} else if strings.TrimSpace(request.Model) == "" {
		return nil, types.NewError(errors.New("command channel: mapped model is empty"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	return a.Adaptor.ConvertOpenAIRequest(c, info, request)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return nil, types.NewError(errors.New("command channel: /v1/messages is not supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, types.NewError(errors.New("command channel: Gemini endpoint is not supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, types.NewError(errors.New("command channel: audio endpoint is not supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, types.NewError(errors.New("command channel: image endpoint is not supported"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
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

var _ channel.Adaptor = (*Adaptor)(nil)
