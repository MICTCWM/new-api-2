package commandgo

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedInitRequest struct {
	path   string
	header http.Header
	body   []byte
}

type initServer struct {
	mu       sync.Mutex
	requests []recordedInitRequest
	status   int
	delay    time.Duration
	notify   chan struct{}
}

func newInitServer(t *testing.T, status int) (*initServer, *httptest.Server) {
	t.Helper()
	h := &initServer{status: status, notify: make(chan struct{}, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if h.delay > 0 {
			time.Sleep(h.delay)
		}
		h.mu.Lock()
		h.requests = append(h.requests, recordedInitRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})
		h.mu.Unlock()
		select {
		case h.notify <- struct{}{}:
		default:
		}
		w.WriteHeader(h.status)
	}))
	t.Cleanup(srv.Close)
	return h, srv
}

func (h *initServer) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

func (h *initServer) snapshot() []recordedInitRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]recordedInitRequest, len(h.requests))
	copy(out, h.requests)
	return out
}

func (h *initServer) waitCount(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if h.count() >= n {
			return
		}
		select {
		case <-h.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %d init requests, saw %d", n, h.count())
		}
	}
}

func installFakeClock(t *testing.T, start time.Time) func(time.Time) {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(start.UnixNano())
	prevNow := fingerprintNow
	prevJitter := fingerprintJitter
	prevHTTPDo := fingerprintHTTPDo
	fingerprintNow = func() time.Time { return time.Unix(0, clock.Load()) }
	fingerprintJitter = func(time.Duration) time.Duration { return 0 }
	t.Cleanup(func() {
		fingerprintNow = prevNow
		fingerprintJitter = prevJitter
		fingerprintHTTPDo = prevHTTPDo
	})
	return func(next time.Time) { clock.Store(next.UnixNano()) }
}

func stateBusy(key string) bool {
	v, ok := fingerprintStateStore.Load(key)
	if !ok {
		return false
	}
	state := v.(*keyInitState)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.busy
}

func waitStateIdle(t *testing.T, key string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for stateBusy(key) {
		if time.Now().After(deadline) {
			t.Fatalf("init report for %q still in flight", key)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitStatePointerIdle(t *testing.T, state *keyInitState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		state.mu.Lock()
		busy := state.busy
		state.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("detached init state still in flight")
		}
		time.Sleep(time.Millisecond)
	}
}

func okInitResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

// assertNoInitStarted verifies a non-due call was rejected synchronously. A
// claim always sets busy before spawning the reporter, so if a report had
// started, busy would already be true here; the count guard also catches a
// goroutine that finished before the check.
func assertNoInitStarted(t *testing.T, key string, server *initServer, want int) {
	t.Helper()
	assert.False(t, stateBusy(key), "non-due call must not claim the report slot")
	assert.Equal(t, want, server.count(), "non-due call must not send reports")
}

func TestEnsureInitializedReportsFingerprintAndLifecycle(t *testing.T) {
	const key = "user_report_shape_01"
	ResetFingerprintState(key)
	t.Cleanup(func() { ResetFingerprintState(key) })

	server, srv := newInitServer(t, http.StatusOK)
	installFakeClock(t, time.Now())

	// Pass a BaseURL that already ends in /alpha to exercise the de-duplication.
	EnsureInitialized(srv.URL+"/alpha", key, nil)
	server.waitCount(t, 2)
	waitStateIdle(t, key, 2*time.Second)

	requests := server.snapshot()
	var fpReq, lifeReq *recordedInitRequest
	for i := range requests {
		switch requests[i].path {
		case "/alpha/fingerprint/record":
			fpReq = &requests[i]
		case "/alpha/lifecycle-events":
			lifeReq = &requests[i]
		}
	}
	require.NotNil(t, fpReq, "missing fingerprint report")
	require.NotNil(t, lifeReq, "missing lifecycle report")

	for _, req := range []*recordedInitRequest{fpReq, lifeReq} {
		assert.Equal(t, "application/json", req.header.Get("Content-Type"))
		assert.Equal(t, "cli", req.header.Get("User-Agent"))
		assert.Equal(t, "production", req.header.Get("x-cli-environment"))
		assert.Equal(t, "Bearer "+key, req.header.Get("Authorization"))
		assert.Equal(t, ProtocolVersion, req.header.Get("x-command-code-version"))
	}

	var fp map[string]any
	require.NoError(t, common.Unmarshal(fpReq.body, &fp))
	components, ok := fp["components"].(map[string]any)
	require.True(t, ok, "fingerprint body missing components")
	expected := GenerateFingerprint(key)
	assert.Equal(t, expected.Thumbmark, fp["thumbmark"])
	for _, field := range []string{
		"machineIdHash", "macHashes", "osUserHash", "hostnameHash", "gitEmailHash",
		"platform", "arch", "osRelease", "cpuModel", "cpuCount", "memGiB",
		"isContainer", "timezone", "runtime", "collectorVersion",
	} {
		assert.Contains(t, components, field)
	}
	assert.Equal(t, "cli", components["runtime"])
	assert.Equal(t, float64(1), components["collectorVersion"])
	assert.Equal(t, "win32", components["platform"])
	assert.Equal(t, "x64", components["arch"])
	assert.Contains(t, components, "isContainer")

	var life map[string]any
	require.NoError(t, common.Unmarshal(lifeReq.body, &life))
	assert.Equal(t, "cli_session_exists", life["eventType"])
	meta, ok := life["metadata"].(map[string]any)
	require.True(t, ok)
	sessionID, _ := meta["sessionId"].(string)
	assert.True(t, len(sessionID) == len("sess_")+16, "sessionId %q", sessionID)
	assert.Equal(t, "sess_", sessionID[:len("sess_")])
	assert.Equal(t, ProtocolVersion, meta["cliVersion"])
	assert.Equal(t, "interactive", meta["mode"])
	assert.Equal(t, "win32-x64", meta["os"])
}

func TestEnsureInitializedThrottlesSuccessForEightHours(t *testing.T) {
	const key = "user_report_throttle_01"
	ResetFingerprintState(key)
	t.Cleanup(func() { ResetFingerprintState(key) })

	server, srv := newInitServer(t, http.StatusOK)
	start := time.Unix(1_700_000_000, 0)
	setNow := installFakeClock(t, start)

	EnsureInitialized(srv.URL, key, nil)
	server.waitCount(t, 2)
	waitStateIdle(t, key, 2*time.Second)

	// Still inside the 8h window (jitter disabled in tests) -> no new reports.
	setNow(start.Add(time.Hour))
	EnsureInitialized(srv.URL, key, nil)
	assertNoInitStarted(t, key, server, 2)

	// Past the window -> reports again.
	setNow(start.Add(initRefreshDuration + time.Minute))
	EnsureInitialized(srv.URL, key, nil)
	server.waitCount(t, 4)
	waitStateIdle(t, key, 2*time.Second)
}

func TestEnsureInitializedRetriesSoonAfterFailure(t *testing.T) {
	const key = "user_report_retry_01"
	ResetFingerprintState(key)
	t.Cleanup(func() { ResetFingerprintState(key) })

	server, srv := newInitServer(t, http.StatusInternalServerError)
	start := time.Unix(1_700_000_000, 0)
	setNow := installFakeClock(t, start)

	EnsureInitialized(srv.URL, key, nil)
	server.waitCount(t, 2)
	waitStateIdle(t, key, 2*time.Second)

	// Inside the 1min retry window -> nothing.
	setNow(start.Add(30 * time.Second))
	EnsureInitialized(srv.URL, key, nil)
	assertNoInitStarted(t, key, server, 2)

	// After the retry window -> retries.
	setNow(start.Add(initRetryDuration + time.Second))
	EnsureInitialized(srv.URL, key, nil)
	server.waitCount(t, 4)
	waitStateIdle(t, key, 2*time.Second)

	state := getOrCreateFingerprintState(key)
	state.mu.Lock()
	fpReport := state.fingerprintReport
	lifeReport := state.lifecycleReport
	state.mu.Unlock()
	require.NotNil(t, fpReport)
	require.NotNil(t, lifeReport)
	assert.False(t, fpReport.OK)
	assert.Equal(t, http.StatusInternalServerError, fpReport.Status)
	assert.False(t, lifeReport.OK)
}

// TestEnsureInitializedConcurrentCallsClaimAtomically holds the first report
// pair open and proves that concurrent callers cannot start a second pair, then
// that a still-throttled caller does not restart after the first finishes.
func TestEnsureInitializedConcurrentCallsClaimAtomically(t *testing.T) {
	const key = "user_report_concurrent_01"
	ResetFingerprintState(key)
	t.Cleanup(func() { ResetFingerprintState(key) })

	installFakeClock(t, time.Now())
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseReports := func() { releaseOnce.Do(func() { close(release) }) }
	// Ensure blocked report goroutines are released even if an assertion fails
	// or the test returns early.
	t.Cleanup(releaseReports)
	var sent atomic.Int32
	prevHTTPDo := fingerprintHTTPDo
	fingerprintHTTPDo = func(*http.Request) (*http.Response, error) {
		sent.Add(1)
		started <- struct{}{}
		<-release
		return okInitResponse(), nil
	}
	t.Cleanup(func() { fingerprintHTTPDo = prevHTTPDo })

	EnsureInitialized("http://report.invalid", key, nil)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first report did not start")
	}

	// While the first pair is in flight, every other caller must be rejected.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			EnsureInitialized("http://report.invalid", key, nil)
		}()
	}
	wg.Wait()

	releaseReports()
	waitStateIdle(t, key, 2*time.Second)
	assert.Equal(t, int32(2), sent.Load(), "concurrent calls must share one report pair")

	// First pair completed and nextInitAt is in the future -> no restart.
	EnsureInitialized("http://report.invalid", key, nil)
	assert.False(t, stateBusy(key))
	assert.Equal(t, int32(2), sent.Load())
}

func TestSweepExpiredSessionsResetsFingerprintState(t *testing.T) {
	const key = "user_sweep_fp_0001"
	ResetFingerprintState(key)
	t.Cleanup(func() {
		ResetFingerprintState(key)
		ClearSessions()
	})

	getOrCreateFingerprintState(key)
	now := time.Now()
	sessionStore.Store(key, sessionEntry{sessionID: "stale", expiresAt: now.Add(-time.Hour)})

	sessionReadCount.Store(sessionSweepInterval - 1)
	t.Cleanup(func() { sessionReadCount.Store(0) })
	sweepExpiredSessions(now)

	_, ok := fingerprintStateStore.Load(key)
	assert.False(t, ok, "expired session must drop its fingerprint state")
}

// TestInFlightReportDoesNotResurrectResetState ensures a report that was already
// running when ResetFingerprintState ran only writes its detached state object.
func TestInFlightReportDoesNotResurrectResetState(t *testing.T) {
	const key = "user_report_reset_inflight"
	ResetFingerprintState(key)
	t.Cleanup(func() { ResetFingerprintState(key) })

	installFakeClock(t, time.Now())
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseReports := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseReports)
	prevHTTPDo := fingerprintHTTPDo
	fingerprintHTTPDo = func(*http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release
		return okInitResponse(), nil
	}
	t.Cleanup(func() { fingerprintHTTPDo = prevHTTPDo })

	detached := getOrCreateFingerprintState(key)
	EnsureInitialized("http://report.invalid", key, nil)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first report did not start")
	}

	ResetFingerprintState(key)
	releaseReports()
	waitStatePointerIdle(t, detached, 2*time.Second)

	_, ok := fingerprintStateStore.Load(key)
	assert.False(t, ok, "an in-flight report must not resurrect a reset state")
}
