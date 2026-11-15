package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestChannelLimitRejectErrorMessages(t *testing.T) {
	concurrencyErr := channelLimitRejectError("concurrency")
	require.Equal(t, http.StatusTooManyRequests, concurrencyErr.StatusCode)
	require.Equal(t, types.ErrorCodeChannelRateLimited, concurrencyErr.GetErrorCode())
	require.Equal(t, constant.ChannelConcurrencyLimitMessage, concurrencyErr.Error())

	tpmErr := channelLimitRejectError("tpm")
	require.Equal(t, http.StatusTooManyRequests, tpmErr.StatusCode)
	require.Equal(t, constant.ChannelTPMLimitMessage, tpmErr.Error())

	// Unknown reasons fall back to the concurrency wording.
	require.Equal(t, constant.ChannelConcurrencyLimitMessage, channelLimitRejectError("").Error())
}
