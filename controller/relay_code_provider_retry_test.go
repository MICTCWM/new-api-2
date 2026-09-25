package controller

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newShouldRetryTestContext(t *testing.T, channelType int, specificChannel bool) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	require.NotNil(t, ctx)
	if channelType != 0 {
		common.SetContextKey(ctx, constant.ContextKeyChannelType, channelType)
	}
	if specificChannel {
		ctx.Set("specific_channel_id", 123)
	}
	return ctx
}

func newUpstreamRetryError(code types.ErrorCode, status int, ops ...types.NewAPIErrorOptions) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New("upstream failure"), code, status, ops...)
}

// code provider（Opencode / Command）对上游的任何错误都要转移，不再按错误码过滤，
// 只保留重试次数、specific_channel_id 与客户端已取消三个护栏。
func TestShouldRetry_CodeProviderTransfersEveryError(t *testing.T) {
	tests := []struct {
		name            string
		channelType     int
		err             *types.NewAPIError
		retryTimes      int
		specificChannel bool
		want            bool
	}{
		{
			name:        "command 400 transfers",
			channelType: constant.ChannelTypeCommand,
			err:         newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 400),
			retryTimes:  1,
			want:        true,
		},
		{
			name:        "opencode 408 transfers",
			channelType: constant.ChannelTypeOpencode,
			err:         newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 408),
			retryTimes:  1,
			want:        true,
		},
		{
			name:        "opencode 504 transfers",
			channelType: constant.ChannelTypeOpencode,
			err:         newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 504),
			retryTimes:  1,
			want:        true,
		},
		{
			name:        "opencode 524 transfers",
			channelType: constant.ChannelTypeOpencode,
			err:         newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 524),
			retryTimes:  1,
			want:        true,
		},
		{
			name:        "opencode 429 transfers",
			channelType: constant.ChannelTypeOpencode,
			err:         newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 429),
			retryTimes:  1,
			want:        true,
		},
		{
			name:        "opencode bad_response_body transfers",
			channelType: constant.ChannelTypeOpencode,
			err:         newUpstreamRetryError(types.ErrorCodeBadResponseBody, 500),
			retryTimes:  1,
			want:        true,
		},
		{
			// 本地校验类错误（如模型不支持）换渠道可能有救，必须继续转移，
			// 不得恢复 IsSkipRetryError 过滤。
			name:        "opencode local validation skip_retry error still transfers",
			channelType: constant.ChannelTypeOpencode,
			err:         newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 500, types.ErrOptionWithSkipRetry()),
			retryTimes:  1,
			want:        true,
		},
		{
			name:        "opencode 200 transfers",
			channelType: constant.ChannelTypeOpencode,
			err:         newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 200),
			retryTimes:  1,
			want:        true,
		},
		{
			name:        "command 500 transfers",
			channelType: constant.ChannelTypeCommand,
			err:         newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 500),
			retryTimes:  1,
			want:        true,
		},
		{
			name:        "opencode retry budget exhausted",
			channelType: constant.ChannelTypeOpencode,
			err:         newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 500),
			retryTimes:  0,
			want:        false,
		},
		{
			name:            "opencode specific channel guard",
			channelType:     constant.ChannelTypeOpencode,
			err:             newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 500),
			retryTimes:      1,
			specificChannel: true,
			want:            false,
		},
		{
			name:        "opencode nil error",
			channelType: constant.ChannelTypeOpencode,
			err:         nil,
			retryTimes:  1,
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newShouldRetryTestContext(t, tt.channelType, tt.specificChannel)
			assert.Equal(t, tt.want, shouldRetry(ctx, tt.err, tt.retryTimes))
		})
	}
}

// 客户端已取消或上下文结束时换渠道毫无意义，code provider 也必须停止重试。
func TestShouldRetry_CodeProviderStopsWhenClientContextCanceled(t *testing.T) {
	ctx := newShouldRetryTestContext(t, constant.ChannelTypeOpencode, false)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	canceledCtx, cancel := context.WithCancel(req.Context())
	cancel()
	ctx.Request = req.WithContext(canceledCtx)

	err := newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 500)
	assert.False(t, shouldRetry(ctx, err, 3), "canceled client context must not trigger failover retries")
}

// 普通渠道不得受 code provider 改动影响，仍按原有错误码规则过滤。
func TestShouldRetry_NonCodeProviderKeepsStatusCodeFiltering(t *testing.T) {
	tests := []struct {
		name       string
		err        *types.NewAPIError
		retryTimes int
		want       bool
	}{
		{
			name:       "400 not retried",
			err:        newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 400),
			retryTimes: 1,
			want:       false,
		},
		{
			name:       "504 not retried",
			err:        newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 504),
			retryTimes: 1,
			want:       false,
		},
		{
			name:       "429 retried",
			err:        newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 429),
			retryTimes: 1,
			want:       true,
		},
		{
			name:       "200 not retried",
			err:        newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 200),
			retryTimes: 1,
			want:       false,
		},
		{
			name:       "bad_response_body not retried",
			err:        newUpstreamRetryError(types.ErrorCodeBadResponseBody, 500),
			retryTimes: 1,
			want:       false,
		},
		{
			name:       "skip_retry option not retried",
			err:        newUpstreamRetryError(types.ErrorCodeDoRequestFailed, 500, types.ErrOptionWithSkipRetry()),
			retryTimes: 1,
			want:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newShouldRetryTestContext(t, constant.ChannelTypeOpenAI, false)
			assert.Equal(t, tt.want, shouldRetry(ctx, tt.err, tt.retryTimes))
		})
	}
}
