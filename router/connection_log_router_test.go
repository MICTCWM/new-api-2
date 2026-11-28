package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The live connection log is pushed over SSE, so its routes must stay outside
// the global API rate limit: a panel that polls (or a stream that reconnects)
// used to exhaust the per-IP quota and turn every dashboard request into a 429.
func TestConnectionLogRoutesBypassGlobalAPIRateLimit(t *testing.T) {
	previousEnable := common.GlobalApiRateLimitEnable
	previousNum := common.GlobalApiRateLimitNum
	previousDuration := common.GlobalApiRateLimitDuration
	previousRedisEnabled := common.RedisEnabled
	common.GlobalApiRateLimitEnable = true
	common.GlobalApiRateLimitNum = 3
	common.GlobalApiRateLimitDuration = 180
	// The in-memory limiter needs no server, so the assertion does not depend on
	// a reachable Redis instance.
	common.RedisEnabled = false
	t.Cleanup(func() {
		common.GlobalApiRateLimitEnable = previousEnable
		common.GlobalApiRateLimitNum = previousNum
		common.GlobalApiRateLimitDuration = previousDuration
		common.RedisEnabled = previousRedisEnabled
	})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)

	// A dedicated client address keeps this test from sharing the limiter
	// bucket with any other router test.
	statusFor := func(path string, attempts int) []int {
		statuses := make([]int, 0, attempts)
		for range attempts {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.RemoteAddr = "198.51.100.77:12345"
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			statuses = append(statuses, recorder.Code)
		}
		return statuses
	}

	// The unauthenticated requests stop at the auth middleware, which proves the
	// handler chain ran without the limiter rejecting them first.
	for _, status := range statusFor("/api/connection/logs", 10) {
		require.NotEqual(t, http.StatusTooManyRequests, status,
			"the connection log snapshot must not consume the global API rate limit")
	}
	// Leaving the apiRouter group must not have dropped admin auth.
	require.Equal(t, http.StatusUnauthorized,
		statusFor("/api/connection/logs", 1)[0])
	require.Equal(t, http.StatusUnauthorized,
		statusFor("/api/connection/logs/stream", 1)[0])

	// The same client is still limited on an ordinary dashboard route, so the
	// bypass above is scoped to the connection log routes.
	regular := statusFor("/api/notice", 10)
	require.Equal(t, http.StatusOK, regular[0])
	require.Equal(t, http.StatusTooManyRequests, regular[len(regular)-1],
		"the global API rate limit must still apply to ordinary /api routes")
}
