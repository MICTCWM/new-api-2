package commandgo

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAdaptorContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

func TestGetRequestURLOnlyChatCompletions(t *testing.T) {
	a := &Adaptor{}
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions}
	url, err := a.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, CanonicalURL, url)

	info = &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeEmbeddings}
	_, err = a.GetRequestURL(info)
	require.Error(t, err)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.True(t, types.IsSkipRetryError(apiErr))
}

func TestDoRequestDoesNotMarkAttemptOnLocalValidationFailure(t *testing.T) {
	a := &Adaptor{}
	c := newAdaptorContext()
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeEmbeddings}
	_, err := a.DoRequest(c, info, nil)
	require.Error(t, err)
	assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyCommandGoUpstreamAttempted),
		"local URL validation failures must not count as upstream attempts")
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.True(t, types.IsSkipRetryError(apiErr))
}

func TestConvertOpenAIRequestRejectsUnsupportedModel(t *testing.T) {
	a := &Adaptor{}
	c := newAdaptorContext()
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	_, err := a.ConvertOpenAIRequest(c, info, &dto.GeneralOpenAIRequest{Model: "other-model"})
	require.Error(t, err)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
}

func TestConvertOpenAIRequestUsesMappedModel(t *testing.T) {
	a := &Adaptor{}
	c := newAdaptorContext()
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{IsModelMapped: true, UpstreamModelName: "mapped-model"}}
	envelope, err := a.ConvertOpenAIRequest(c, info, &dto.GeneralOpenAIRequest{Model: "mapped-model"})
	require.NoError(t, err)
	params := envelope.(map[string]any)["params"].(map[string]any)
	assert.Equal(t, "mapped-model", params["model"])
}

func TestConvertOpenAIRequestRewritesReasoningNone(t *testing.T) {
	a := &Adaptor{}
	c := newAdaptorContext()
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	request := &dto.GeneralOpenAIRequest{Model: Model, ReasoningEffort: "none"}
	envelope, err := a.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	assert.Equal(t, "high", request.ReasoningEffort)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyCommandHideThinking))
	params := envelope.(map[string]any)["params"].(map[string]any)
	assert.Equal(t, "high", params["reasoning_effort"])
}

func TestConvertOpenAIRequestSetsSystemPromptOverrideContext(t *testing.T) {
	a := &Adaptor{}
	c := newAdaptorContext()
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelSetting: dto.ChannelSettings{
			SystemPrompt:         "injected",
			SystemPromptOverride: true,
		}},
	}
	request := &dto.GeneralOpenAIRequest{
		Model:    Model,
		Messages: []dto.Message{{Role: "system", Content: "original"}, {Role: "user", Content: "hi"}},
	}
	_, err := a.ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	assert.True(t, common.GetContextKeyBool(c, constant.ContextKeySystemPromptOverride))
	assert.Equal(t, "injected\noriginal", request.Messages[0].StringContent())
}

func TestConvertNonChatRequestsRejected(t *testing.T) {
	a := &Adaptor{}
	c := newAdaptorContext()
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	_, err := a.ConvertClaudeRequest(c, info, &dto.ClaudeRequest{})
	require.Error(t, err)
	_, err = a.ConvertGeminiRequest(c, info, &dto.GeminiChatRequest{})
	require.Error(t, err)
	_, err = a.ConvertAudioRequest(c, info, dto.AudioRequest{})
	require.Error(t, err)
	_, err = a.ConvertImageRequest(c, info, dto.ImageRequest{})
	require.Error(t, err)
}
