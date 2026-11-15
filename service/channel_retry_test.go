package service

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
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

func TestTryStartChannelRetryBackoffAndLimit(t *testing.T) {
	param := &RetryParam{}
	channel := &model.Channel{Id: 7}

	for attempt := 1; attempt <= 3; attempt++ {
		delay, ok := param.TryStartChannelRetry(channel, 3)
		require.True(t, ok)
		require.Equal(t, time.Duration(attempt)*time.Second, delay)
	}

	_, ok := param.TryStartChannelRetry(channel, 3)
	require.False(t, ok)
	require.Equal(t, 3, param.ChannelRetryCount())

	// Every channel keeps its own retry budget within the same request.
	delay, ok := param.TryStartChannelRetry(&model.Channel{Id: 8}, 2)
	require.True(t, ok)
	require.Equal(t, time.Second, delay)
	require.Equal(t, 4, param.ChannelRetryCount())
}

func TestTryStartChannelRetryRejectsZeroLimit(t *testing.T) {
	param := &RetryParam{}
	_, ok := param.TryStartChannelRetry(&model.Channel{Id: 7}, 0)
	require.False(t, ok)
	require.Equal(t, 0, param.ChannelRetryCount())
}
