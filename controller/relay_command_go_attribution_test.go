package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAttributionContext(t *testing.T, canceled bool) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if canceled {
		ctx, cancel := context.WithCancel(req.Context())
		cancel()
		req = req.WithContext(ctx)
	}
	c.Request = req
	return c
}

func TestShouldAttributeCommandGoFailure(t *testing.T) {
	assert.False(t, shouldAttributeCommandGoFailure(newAttributionContext(t, false), nil))
	assert.True(t, shouldAttributeCommandGoFailure(newAttributionContext(t, false),
		types.NewErrorWithStatusCode(errors.New("boom"), types.ErrorCodeDoRequestFailed, http.StatusBadGateway)))

	skip := types.NewErrorWithStatusCode(errors.New("skip"), types.ErrorCodeDoRequestFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	assert.False(t, shouldAttributeCommandGoFailure(newAttributionContext(t, false), skip))

	convert := types.NewError(errors.New("bad request"), types.ErrorCodeConvertRequestFailed)
	assert.False(t, shouldAttributeCommandGoFailure(newAttributionContext(t, false), convert))

	invalid := types.NewError(errors.New("invalid"), types.ErrorCodeInvalidRequest)
	assert.False(t, shouldAttributeCommandGoFailure(newAttributionContext(t, false), invalid))

	assert.False(t, shouldAttributeCommandGoFailure(newAttributionContext(t, true),
		types.NewErrorWithStatusCode(errors.New("client gone"), types.ErrorCodeDoRequestFailed, http.StatusBadGateway)))
}

func TestCommandGoAttemptRecordFailureNilSafeOnlyWhenNoAccount(t *testing.T) {
	// A nil attempt account must remain a no-op (non-Command Go channels).
	require.NotPanics(t, func() {
		var account *commandGoAttemptAccount
		account.recordFailure(nil, 0)
		account.recordSuccess()
	})
}
