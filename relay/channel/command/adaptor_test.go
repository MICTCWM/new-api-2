package command

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIRequestPreservesMappedUpstreamModel(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "custom-upstream-model"}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{IsModelMapped: true},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, "custom-upstream-model", convertedRequest.Model)
}

func TestConvertOpenAIRequestUsesFixedModelWithoutMapping(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: 62},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, Model, convertedRequest.Model)
}

func TestConvertOpenAIRequestPreservesNativeProModel(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: ProModel}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: 62},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, ProModel, convertedRequest.Model)
}

func TestGetRequestURLDoesNotDuplicateVersionPath(t *testing.T) {
	url, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions,
	})
	require.NoError(t, err)
	require.Equal(t, "https://api.commandcode.ai/provider/v1/chat/completions", url)
}

func newCommandConvertContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	return c
}

func TestConvertOpenAIRequestRewritesNoneReasoningEffort(t *testing.T) {
	for _, effort := range []string{"none", "NONE", " none "} {
		t.Run(effort, func(t *testing.T) {
			c := newCommandConvertContext()
			request := &dto.GeneralOpenAIRequest{Model: Model, ReasoningEffort: effort}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCommand, UpstreamModelName: Model},
			}

			converted, err := (&Adaptor{}).ConvertOpenAIRequest(c, info, request)
			require.NoError(t, err)
			convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
			require.True(t, ok)
			require.Equal(t, "high", convertedRequest.ReasoningEffort)
			require.True(t, common.GetContextKeyBool(c, constant.ContextKeyCommandHideThinking))
		})
	}
}

func TestConvertOpenAIRequestKeepsOtherReasoningEffort(t *testing.T) {
	for _, effort := range []string{"high", "medium", ""} {
		t.Run(effort, func(t *testing.T) {
			c := newCommandConvertContext()
			request := &dto.GeneralOpenAIRequest{Model: Model, ReasoningEffort: effort}
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCommand, UpstreamModelName: Model},
			}

			converted, err := (&Adaptor{}).ConvertOpenAIRequest(c, info, request)
			require.NoError(t, err)
			convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
			require.True(t, ok)
			require.Equal(t, effort, convertedRequest.ReasoningEffort)
			require.False(t, common.GetContextKeyBool(c, constant.ContextKeyCommandHideThinking))
		})
	}
}

func TestConvertOpenAIRequestRewritesNoneForMappedModel(t *testing.T) {
	c := newCommandConvertContext()
	request := &dto.GeneralOpenAIRequest{Model: "custom-upstream-model", ReasoningEffort: "none"}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCommand, IsModelMapped: true, UpstreamModelName: "custom-upstream-model"},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(c, info, request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, "high", convertedRequest.ReasoningEffort)
	require.True(t, common.GetContextKeyBool(c, constant.ContextKeyCommandHideThinking))
}
