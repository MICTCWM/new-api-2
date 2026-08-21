package opencode

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
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
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: 61},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, Model, convertedRequest.Model)
}

func TestConvertOpenAIRequestRejectsUnmappedModel(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "unsupported-model"}

	_, err := (&Adaptor{}).ConvertOpenAIRequest(nil, nil, request)
	require.ErrorContains(t, err, "unsupported model")
}

func TestGetRequestURLDoesNotDuplicateVersionPath(t *testing.T) {
	url, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions,
	})
	require.NoError(t, err)
	require.Equal(t, CanonicalURL, url)
}
