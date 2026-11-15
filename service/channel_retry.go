package service

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const channelRetryFailuresContextKey = "channel_retry_failures"

// ChannelRetryCountContextKey stores how many same-channel retries the request
// spent before it succeeded or gave up.
const ChannelRetryCountContextKey = "channel_retry_count"

// ChannelRetryFailure describes a channel attempt that failed before the
// request moved to the next channel. It is written under admin_info so the
// upstream failure details stay out of regular user-facing logs.
type ChannelRetryFailure struct {
	AttemptIndex int    `json:"attempt_index"`
	ChannelID    int    `json:"channel_id"`
	ChannelName  string `json:"channel_name,omitempty"`
	ChannelType  int    `json:"channel_type"`
	StatusCode   int    `json:"status_code,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorType    string `json:"error_type,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// RecordChannelRetryFailure keeps the failure attached to the current
// request. The current channel is already present in use_channel when relay
// errors are processed, so AttemptIndex identifies the next channel in the
// retry chain without changing the existing chain format.
func RecordChannelRetryFailure(c *gin.Context, channelID int, channelName string, channelType int, err *types.NewAPIError) {
	if c == nil || channelID <= 0 || err == nil {
		return
	}

	failures := getChannelRetryFailures(c)
	attemptIndex := len(c.GetStringSlice("use_channel")) - 1
	if attemptIndex < 0 {
		attemptIndex = 0
	}
	reason := strings.TrimSpace(err.MaskSensitiveError())
	if len(reason) > 2048 {
		reason = reason[:2048]
	}
	failures = append(failures, ChannelRetryFailure{
		AttemptIndex: attemptIndex,
		ChannelID:    channelID,
		ChannelName:  channelName,
		ChannelType:  channelType,
		StatusCode:   err.StatusCode,
		ErrorCode:    string(err.GetErrorCode()),
		ErrorType:    string(err.GetErrorType()),
		Reason:       reason,
	})
	c.Set(channelRetryFailuresContextKey, failures)
}

func getChannelRetryFailures(c *gin.Context) []ChannelRetryFailure {
	if c == nil {
		return nil
	}
	value, exists := c.Get(channelRetryFailuresContextKey)
	if !exists || value == nil {
		return nil
	}
	failures, ok := value.([]ChannelRetryFailure)
	if !ok {
		return nil
	}
	return append([]ChannelRetryFailure(nil), failures...)
}

// AppendChannelRetryAdminInfo adds retry failure details to an existing
// administrator-only log payload. The frontend uses AttemptIndex to resolve
// the destination channel from use_channel.
func AppendChannelRetryAdminInfo(c *gin.Context, adminInfo map[string]interface{}) {
	if c == nil || adminInfo == nil {
		return
	}
	if count, ok := c.Get(ChannelRetryCountContextKey); ok {
		if retries, ok := count.(int); ok && retries > 0 {
			adminInfo["channel_retry_count"] = retries
		}
	}
	failures := getChannelRetryFailures(c)
	if len(failures) == 0 {
		return
	}
	adminInfo["channel_retry_failures"] = failures
}
