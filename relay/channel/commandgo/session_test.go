package commandgo

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureSessionReusesAndRotates(t *testing.T) {
	ClearSessions()
	defer ClearSessions()
	originalNow := sessionNow
	defer func() { sessionNow = originalNow }()

	base := time.Unix(1_700_000_000, 0)
	current := base
	sessionNow = func() time.Time { return current }

	first := ensureSession("account-a")
	require.True(t, isUUID(first))
	assert.Equal(t, first, ensureSession("account-a"))
	assert.NotEqual(t, first, ensureSession("account-b"))

	// Beyond the maximum lifetime (12h + 1h jitter) the session must rotate.
	current = base.Add(13*time.Hour + time.Minute)
	second := ensureSession("account-a")
	require.True(t, isUUID(second))
	assert.NotEqual(t, first, second)
}

func TestResetSession(t *testing.T) {
	ClearSessions()
	defer ClearSessions()
	first := ensureSession("account-reset")
	ResetSession("account-reset")
	second := ensureSession("account-reset")
	assert.NotEqual(t, first, second)
}

func TestResolveSessionIDPrefersClientHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Request.Header.Set("x-session-id", "client-session-123")

	assert.Equal(t, "client-session-123", ResolveSessionID(c, "account", ""))
}

func TestResolveSessionIDUsesPromptCacheKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	assert.Equal(t, "cache-key-123456", ResolveSessionID(c, "account", "cache-key-123456"))
}

func TestUUIDValidation(t *testing.T) {
	assert.True(t, isUUID(newUUID()))
	assert.False(t, isUUID("not-a-uuid"))
	assert.False(t, isUUID(""))
}

func TestSlugifyProjectPath(t *testing.T) {
	assert.Equal(t, "c-users-dev-projects-app", SlugifyProjectPath(`C:\Users\dev\projects\app`))
	assert.Equal(t, "root", SlugifyProjectPath(""))
	assert.Equal(t, "a-b-c", SlugifyProjectPath("  A__B///C  "))
}

func TestEnsureSessionReplacesExpiredEntry(t *testing.T) {
	ClearSessions()
	previousNow := sessionNow
	now := time.Now()
	sessionNow = func() time.Time { return now }
	t.Cleanup(func() {
		sessionNow = previousNow
		ClearSessions()
	})

	first := ensureSession("user_expiry01")
	require.NotEmpty(t, first)

	now = now.Add(sessionDuration + sessionJitter + time.Minute)
	second := ensureSession("user_expiry01")
	require.NotEmpty(t, second)
	assert.NotEqual(t, first, second)
}

func TestSweepExpiredSessionsDropsStaleEntries(t *testing.T) {
	ClearSessions()
	now := time.Now()
	sessionStore.Store("user_stale0001", sessionEntry{sessionID: "stale", expiresAt: now.Add(-time.Hour)})
	sessionStore.Store("user_live00001", sessionEntry{sessionID: "live", expiresAt: now.Add(time.Hour)})

	sessionReadCount.Store(sessionSweepInterval - 1)
	t.Cleanup(func() { sessionReadCount.Store(0) })
	sweepExpiredSessions(now)

	_, staleFound := sessionStore.Load("user_stale0001")
	assert.False(t, staleFound)
	_, liveFound := sessionStore.Load("user_live00001")
	assert.True(t, liveFound)
	ClearSessions()
}
