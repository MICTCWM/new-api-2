package controller

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldRetryAllowsCodeProviderAffinityFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("channel_affinity_skip_retry_on_failure", true)
	common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeCommand)

	err := types.NewErrorWithStatusCode(errors.New("upstream failure"), types.ErrorCodeDoRequestFailed, 500)
	require.True(t, shouldRetry(ctx, err, 1))
}

func TestShouldRetryKeepsAffinitySkipForNonCodeProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("channel_affinity_skip_retry_on_failure", true)
	common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)

	err := types.NewErrorWithStatusCode(errors.New("upstream failure"), types.ErrorCodeDoRequestFailed, 500)
	require.False(t, shouldRetry(ctx, err, 1))
}
