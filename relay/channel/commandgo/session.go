package commandgo

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
)

// Session management mirrors src/protocol/session.mjs: each upstream account
// keeps one thread id for 12h plus 0..1h jitter, then rotates. The store is
// process-local and keyed by account key so different pool accounts never
// share a conversation identity.
const (
	sessionDuration = 12 * time.Hour
	sessionJitter   = time.Hour
)

type sessionEntry struct {
	sessionID string
	expiresAt time.Time
}

const sessionSweepInterval = 256

var (
	sessionStore     sync.Map // accountKey -> sessionEntry
	sessionStoreMu   sync.Mutex
	sessionNow       = time.Now
	sessionReadCount atomic.Uint64
)

// sweepExpiredSessions runs occasionally to drop entries for accounts that are
// never used again, so the process-local store does not grow without bound.
func sweepExpiredSessions(now time.Time) {
	if sessionReadCount.Add(1)%sessionSweepInterval != 0 {
		return
	}
	sessionStore.Range(func(key, value any) bool {
		if s, ok := value.(sessionEntry); ok && !now.Before(s.expiresAt) {
			sessionStore.Delete(key)
			// Mirror the prototype: when a session expires, drop its
			// fingerprint state too so the store does not grow forever.
			if accountKey, ok := key.(string); ok {
				ResetFingerprintState(accountKey)
			}
		}
		return true
	})
}

// ensureSession returns the live session for an account, creating one when
// missing or expired.
func ensureSession(accountKey string) string {
	accountKey = strings.TrimSpace(accountKey)
	if accountKey == "" {
		return newUUID()
	}
	now := sessionNow()
	if entry, ok := sessionStore.Load(accountKey); ok {
		s := entry.(sessionEntry)
		if now.Before(s.expiresAt) {
			return s.sessionID
		}
		sessionStore.Delete(accountKey)
		// Rotating in place bypasses the periodic sweep; clear the fingerprint
		// state here too so it cannot outlive the session it belongs to.
		ResetFingerprintState(accountKey)
	}
	sweepExpiredSessions(now)

	sessionStoreMu.Lock()
	defer sessionStoreMu.Unlock()
	if entry, ok := sessionStore.Load(accountKey); ok {
		s := entry.(sessionEntry)
		if now.Before(s.expiresAt) {
			return s.sessionID
		}
		sessionStore.Delete(accountKey)
		ResetFingerprintState(accountKey)
	}
	jitter := time.Duration(0)
	if sessionJitter > 0 {
		jitter = time.Duration(randomInt63n(int64(sessionJitter)))
	}
	s := sessionEntry{sessionID: newUUID(), expiresAt: now.Add(sessionDuration + jitter)}
	sessionStore.Store(accountKey, s)
	return s.sessionID
}

// ResolveSessionID prefers a client-supplied conversation identifier (at least
// 8 chars) and otherwise falls back to the account-scoped session. A returned
// client value is used verbatim as x-session-id, but the envelope's threadId is
// only populated when the value is a valid UUID.
func ResolveSessionID(c *gin.Context, accountKey, promptCacheKey string) string {
	if c != nil && c.Request != nil {
		for _, name := range []string{"x-session-id", "x-claude-code-session-id", "session_id"} {
			if v := strings.TrimSpace(c.Request.Header.Get(name)); len(v) >= 8 {
				return v
			}
		}
	}
	if v := strings.TrimSpace(promptCacheKey); len(v) >= 8 {
		return v
	}
	return ensureSession(accountKey)
}

// ResetSession drops the stored session for an account so the next request
// starts a fresh conversation. The relay layer calls this when an upstream
// failure should not keep the failed thread alive.
func ResetSession(accountKey string) {
	sessionStore.Delete(strings.TrimSpace(accountKey))
}

// ClearSessions drops every process-local session; intended for tests.
func ClearSessions() {
	sessionStore.Range(func(key, _ any) bool {
		sessionStore.Delete(key)
		return true
	})
}

func isUUID(value string) bool {
	v := strings.TrimSpace(value)
	if len(v) != 36 {
		return false
	}
	for i, ch := range v {
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		}
	}
	return true
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is effectively fatal for identity generation; fall
		// back to a time-derived value rather than returning an empty id.
		common.SysError("commandgo: crypto/rand failed generating session uuid: " + err.Error())
		ts := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			b[i] = byte(ts >> (8 * i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func randomInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		common.SysError("commandgo: crypto/rand failed generating session jitter: " + err.Error())
		return 0
	}
	var v uint64
	for i := 0; i < 8; i++ {
		v = (v << 8) | uint64(b[i])
	}
	return int64(v % uint64(n))
}
