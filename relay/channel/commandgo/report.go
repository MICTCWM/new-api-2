package commandgo

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
)

// This file ports src/protocol/init.mjs + keystate.mjs: every upstream account
// key keeps its own device fingerprint and initialization schedule. On first
// use (and every 8h + up to 2h of jitter) the adaptor reports the fingerprint
// and a lifecycle event exactly like the CLI would, so the upstream sees a real
// client machine rather than a bare API key.
const (
	initRefreshDuration     = 8 * time.Hour
	initJitterDuration      = 2 * time.Hour
	initRetryDuration       = time.Minute
	initRetryJitterDuration = 4 * time.Minute
	initReportTimeout       = 10 * time.Second
)

// Report is the last outcome of one pre-request report POST. Field names mirror
// the prototype ({ts, ok, status?, error?}); zero status/error are omitted.
type Report struct {
	TS     int64  `json:"ts"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// keyInitState is the per-account initialization state. mu guards every field
// because the reporting goroutine writes it while request handlers may read it.
// The same mutex covers the due check and the busy claim so concurrent callers
// can never both start a report.
type keyInitState struct {
	mu                sync.Mutex
	fingerprint       Fingerprint
	createdAt         time.Time
	nextInitAt        time.Time
	busy              bool
	fingerprintReport *Report
	lifecycleReport   *Report
}

// tryClaim atomically checks whether a report is due and, if so, reserves it.
// It is the single place where `due` and `busy` are observed together.
func (s *keyInitState) tryClaim(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	if !s.nextInitAt.IsZero() && now.Before(s.nextInitAt) {
		return false
	}
	s.busy = true
	return true
}

var (
	fingerprintStateStore sync.Map // apiKey -> *keyInitState

	// Replaceable for tests.
	fingerprintNow    = time.Now
	fingerprintJitter = func(max time.Duration) time.Duration {
		if max <= 0 {
			return 0
		}
		return time.Duration(randomInt63n(int64(max)))
	}
	fingerprintHTTPDo = func(req *http.Request) (*http.Response, error) {
		return http.DefaultClient.Do(req)
	}
)

// getOrCreateFingerprintState lazily derives the fingerprint for an account,
// matching getOrCreateKeyState in keystate.mjs.
func getOrCreateFingerprintState(apiKey string) *keyInitState {
	if v, ok := fingerprintStateStore.Load(apiKey); ok {
		return v.(*keyInitState)
	}
	state := &keyInitState{
		fingerprint: GenerateFingerprint(apiKey),
		createdAt:   fingerprintNow(),
	}
	actual, _ := fingerprintStateStore.LoadOrStore(apiKey, state)
	return actual.(*keyInitState)
}

// claimFingerprintInit returns the state for apiKey with its report slot
// atomically claimed. A fresh state is created (with a deterministic
// fingerprint) and, if another goroutine won the insertion race, the existing
// state is used for the claim instead.
func claimFingerprintInit(apiKey string) (*keyInitState, bool) {
	now := fingerprintNow()
	if v, ok := fingerprintStateStore.Load(apiKey); ok {
		state := v.(*keyInitState)
		return state, state.tryClaim(now)
	}
	created := &keyInitState{
		fingerprint: GenerateFingerprint(apiKey),
		createdAt:   now,
	}
	actual, _ := fingerprintStateStore.LoadOrStore(apiKey, created)
	state := actual.(*keyInitState)
	return state, state.tryClaim(now)
}

// ResetFingerprintState drops an account's fingerprint and throttle state so
// the next EnsureInitialized derives and reports afresh. Intended for tests and
// for session-cache sweeping. Any report already in flight writes only to its
// own detached state object and therefore cannot resurrect the deleted entry.
func ResetFingerprintState(apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return
	}
	fingerprintStateStore.Delete(apiKey)
}

// clearFingerprintStates drops every cached fingerprint. Used when the
// derivation salt changes so cached identities never outlive their salt.
func clearFingerprintStates() {
	fingerprintStateStore.Range(func(key, _ any) bool {
		fingerprintStateStore.Delete(key)
		return true
	})
}

// EnsureInitialized schedules the fingerprint + lifecycle reports for an
// account if they are due. It never blocks the request path and never inherits
// the caller's context; the work runs in a detached goroutine with its own
// timeout. Concurrent requests for the same key collapse into one report.
// client, when non-nil, is used for the report POSTs so they honor the channel's
// proxy/transport settings; tests may leave it nil and inject fingerprintHTTPDo.
func EnsureInitialized(baseURL, apiKey string, client *http.Client) {
	// A nil client is the test injection path: reports fall back to
	// fingerprintHTTPDo, which tests replace. Production entries pass
	// requireClient=true so a nil client is rejected rather than sent direct.
	ensureInitializedWithProvider(baseURL, apiKey, false, func() (*http.Client, error) {
		return client, nil
	})
}

// EnsureInitializedWithProxySettings is the relay-path entry point. It defers
// building the channel's proxy-aware client until a report is actually due and
// claimed, so non-due requests never pay for client construction or spam logs
// when proxy resolution is broken. A client-build failure abandons this round
// (the claim is released for a later retry) and NEVER falls back to the default
// client: that would leak the account key past the configured proxy.
func EnsureInitializedWithProxySettings(baseURL, apiKey, proxy string, settings dto.ChannelSettings) {
	ensureInitializedWithProvider(baseURL, apiKey, true, func() (*http.Client, error) {
		return service.GetHttpClientWithProxySettings(proxy, settings)
	})
}

// ensureInitializedWithProvider claims the report slot synchronously (keeping
// the B1 atomic-claim guarantee) and resolves the client lazily inside the
// detached goroutine.
func ensureInitializedWithProvider(baseURL, apiKey string, requireClient bool, clientProvider func() (*http.Client, error)) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return
	}
	state, claimed := claimFingerprintInit(apiKey)
	if !claimed {
		return
	}
	go func() {
		// A panic in the detached reporter must never surface in the relay path.
		// Release the claim too, so a panicking client provider cannot pin busy.
		defer func() {
			if r := recover(); r != nil {
				common.SysError("commandgo: fingerprint init goroutine panicked")
				state.mu.Lock()
				state.busy = false
				state.mu.Unlock()
			}
		}()
		client, err := clientProvider()
		if err == nil && client == nil && requireClient {
			// A production provider that returns (nil, nil) must never fall back
			// to the default client: that would send the Bearer key past the
			// configured proxy. Treat it as a build failure.
			err = errors.New("report http client provider returned nil")
		}
		if err != nil {
			// Only the key prefix is logged; the account key is never exposed.
			reportInitFailure(apiKey, "failed to build report http client: "+err.Error())
			// Back off exactly like a failed report so a persistently broken
			// proxy config retries at most once per retry window instead of
			// rebuilding a client and logging on every request.
			now := fingerprintNow()
			state.mu.Lock()
			state.busy = false
			state.nextInitAt = now.Add(initRetryDuration + fingerprintJitter(initRetryJitterDuration))
			state.mu.Unlock()
			return
		}
		runFingerprintInit(baseURL, apiKey, state, client)
	}()
}

// runFingerprintInit performs both reports in parallel and records the result
// and next-due time. Mirrors ensureInitialized in init.mjs.
func runFingerprintInit(baseURL, apiKey string, state *keyInitState, client *http.Client) {
	state.mu.Lock()
	fp := state.fingerprint
	state.mu.Unlock()

	// Release the claim only after every sub-goroutine has returned (wg.Wait
	// below). Sub-goroutine panics are recovered locally so Wait still returns.
	defer func() {
		state.mu.Lock()
		state.busy = false
		state.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), initReportTimeout)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	var fingerprintOK, lifecycleOK bool
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				common.SysError("commandgo: fingerprint report goroutine panicked")
			}
		}()
		rep, ok := postFingerprintReport(ctx, client, baseURL, apiKey, fp)
		state.mu.Lock()
		state.fingerprintReport = rep
		state.mu.Unlock()
		fingerprintOK = ok
	}()
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				common.SysError("commandgo: lifecycle report goroutine panicked")
			}
		}()
		rep, ok := postLifecycleReport(ctx, client, baseURL, apiKey, fp)
		state.mu.Lock()
		state.lifecycleReport = rep
		state.mu.Unlock()
		lifecycleOK = ok
	}()
	wg.Wait()

	now := fingerprintNow()
	state.mu.Lock()
	defer state.mu.Unlock()
	if fingerprintOK && lifecycleOK {
		state.nextInitAt = now.Add(initRefreshDuration + fingerprintJitter(initJitterDuration))
		return
	}
	// Any failure retries soon (1min + 0..4min) instead of waiting a full 8h,
	// so a transient network blip does not hide the fingerprint for a day.
	state.nextInitAt = now.Add(initRetryDuration + fingerprintJitter(initRetryJitterDuration))
}

func postFingerprintReport(ctx context.Context, client *http.Client, baseURL, apiKey string, fp Fingerprint) (*Report, bool) {
	return postInitJSON(ctx, client, reportEndpoint(baseURL, "fingerprint/record"), apiKey, fp)
}

type lifecycleEvent struct {
	EventType string            `json:"eventType"`
	Metadata  lifecycleMetadata `json:"metadata"`
}

type lifecycleMetadata struct {
	SessionID  string `json:"sessionId"`
	CLIVersion string `json:"cliVersion"`
	Mode       string `json:"mode"`
	OS         string `json:"os"`
}

func postLifecycleReport(ctx context.Context, client *http.Client, baseURL, apiKey string, fp Fingerprint) (*Report, bool) {
	body := lifecycleEvent{
		EventType: "cli_session_exists",
		Metadata: lifecycleMetadata{
			SessionID:  "sess_" + randomHex(8),
			CLIVersion: ProtocolVersion,
			Mode:       "interactive",
			OS:         fp.Components.Platform + "-" + fp.Components.Arch,
		},
	}
	return postInitJSON(ctx, client, reportEndpoint(baseURL, "lifecycle-events"), apiKey, body)
}

func postInitJSON(ctx context.Context, client *http.Client, url, apiKey string, body any) (*Report, bool) {
	payload, err := common.Marshal(body)
	if err != nil {
		reportInitFailure(apiKey, "marshal report body: "+err.Error())
		return &Report{TS: fingerprintNow().UnixMilli(), OK: false, Error: err.Error()}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		reportInitFailure(apiKey, "build report request: "+err.Error())
		return &Report{TS: fingerprintNow().UnixMilli(), OK: false, Error: err.Error()}, false
	}
	req.Header.Set("Content-Type", "application/json")
	// Match the generate request: the upstream CLI never sends Go's default UA.
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-command-code-version", ProtocolVersion)

	do := fingerprintHTTPDo
	if client != nil {
		do = client.Do
	}
	resp, err := do(req)
	if err != nil {
		// Close whenever a response object exists, even if Do also returned an
		// error; a nil resp must not be dereferenced.
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		reportInitFailure(apiKey, "send report: "+err.Error())
		return &Report{TS: fingerprintNow().UnixMilli(), OK: false, Error: err.Error()}, false
	}
	// A successful Do must yield a readable body; treat nil as a failed report
	// rather than dereferencing it in io.Copy.
	if resp == nil || resp.Body == nil {
		reportInitFailure(apiKey, "empty report response")
		return &Report{TS: fingerprintNow().UnixMilli(), OK: false, Error: "empty report response"}, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	ok := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
	rep := &Report{TS: fingerprintNow().UnixMilli(), OK: ok}
	if !ok {
		rep.Status = resp.StatusCode
		reportInitFailure(apiKey, "unexpected status "+resp.Status)
	}
	return rep, ok
}

// reportInitFailure logs a report failure without ever exposing the account
// key or its Authorization header. Only the first eight characters are logged.
func reportInitFailure(apiKey, detail string) {
	prefix := apiKey
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	common.SysError("commandgo: init report failed (key " + prefix + "): " + detail)
}

// reportEndpoint builds <base>/alpha/<path> without duplicating "/alpha" when
// the caller passes the channel BaseURL that already ends in "/alpha".
func reportEndpoint(baseURL, path string) string {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = BaseURL
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/alpha") {
		base = strings.TrimSuffix(base, "/alpha")
	}
	return base + "/alpha/" + path
}

func randomHex(n int) string {
	b := make([]byte, n)
	readRandomOrTime(b, "lifecycle session id")
	return hex.EncodeToString(b)
}
