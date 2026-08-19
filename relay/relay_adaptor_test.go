package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/stretchr/testify/require"
)

func TestGetAdaptorForChannelUsesOpencodeCanonicalURL(t *testing.T) {
	adaptor := GetAdaptorForChannel(constant.APITypeOpenAI, constant.ChannelTypeOpencode)
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions,
	}

	requestURL, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://opencode.ai/zen/go/v1/chat/completions", requestURL)
}
