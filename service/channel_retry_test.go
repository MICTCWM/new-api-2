package service

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRecordChannelRetryFailureKeepsAttemptAndSanitizedReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Set("use_channel", []string{"12"})

	retryError := types.NewErrorWithStatusCode(
		errors.New("upstream quota exceeded"),
		types.ErrorCodeDoRequestFailed,
		429,
	)
	RecordChannelRetryFailure(context, 12, "primary", 41, retryError)

	context.Set("use_channel", []string{"12", "27"})
	adminInfo := make(map[string]interface{})
	AppendChannelRetryAdminInfo(context, adminInfo)

	failures, ok := adminInfo["channel_retry_failures"].([]ChannelRetryFailure)
	require.True(t, ok)
	require.Len(t, failures, 1)
	require.Equal(t, 0, failures[0].AttemptIndex)
	require.Equal(t, 12, failures[0].ChannelID)
	require.Equal(t, "primary", failures[0].ChannelName)
	require.Equal(t, 429, failures[0].StatusCode)
	require.Equal(t, string(types.ErrorCodeDoRequestFailed), failures[0].ErrorCode)
	require.Contains(t, failures[0].Reason, "upstream quota exceeded")
}
