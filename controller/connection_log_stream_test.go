package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// sseRecorder is a concurrency-safe ResponseWriter: an open SSE handler writes
// from its own goroutine while the test inspects (and cancels) it.
type sseRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
	status int
	wrote  chan struct{}
}

func newSSERecorder() *sseRecorder {
	return &sseRecorder{
		header: make(http.Header),
		status: http.StatusOK,
		wrote:  make(chan struct{}, 1),
	}
}

func (recorder *sseRecorder) Header() http.Header {
	return recorder.header
}

func (recorder *sseRecorder) WriteHeader(status int) {
	recorder.mu.Lock()
	recorder.status = status
	recorder.mu.Unlock()
}

func (recorder *sseRecorder) Write(payload []byte) (int, error) {
	recorder.mu.Lock()
	written, err := recorder.body.Write(payload)
	recorder.mu.Unlock()
	select {
	case recorder.wrote <- struct{}{}:
	default:
	}
	return written, err
}

func (recorder *sseRecorder) Flush() {}

func (recorder *sseRecorder) waitForWrite(t *testing.T) {
	t.Helper()
	select {
	case <-recorder.wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream wrote no frame")
	}
}

func (recorder *sseRecorder) bodyText() string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.body.String()
}

func TestStreamConnectionLogsStartsWithSnapshotFrame(t *testing.T) {
	requestID := "conn-sse-first-frame"
	service.RegisterConnection(newConnectionTestRelayInfo(requestID), "carol", 320)
	t.Cleanup(func() { service.FinishConnection(requestID) })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/connection/logs/stream", StreamConnectionLogs)

	recorder := newSSERecorder()
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/connection/logs/stream", nil).
		WithContext(ctx)
	served := make(chan struct{})
	go func() {
		defer close(served)
		router.ServeHTTP(recorder, request)
	}()

	recorder.waitForWrite(t)
	cancel()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream handler did not stop after the client disconnected")
	}

	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
	// nginx must not buffer the frames, otherwise nothing arrives until close.
	require.Equal(t, "no", recorder.Header().Get("X-Accel-Buffering"))

	body := recorder.bodyText()
	require.True(t, bytes.HasPrefix([]byte(body), []byte("data: ")))
	require.True(t, bytes.HasSuffix([]byte(body), []byte("\n\n")))
	require.Contains(t, body, requestID, "the first frame must already list the live connection")
}

func TestStreamConnectionLogsPushesNewConnections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/connection/logs/stream", StreamConnectionLogs)

	recorder := newSSERecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/connection/logs/stream", nil).
		WithContext(ctx)
	served := make(chan struct{})
	go func() {
		defer close(served)
		router.ServeHTTP(recorder, request)
	}()

	// The initial frame has no reason to mention the new request; only the
	// pushed frame may carry it.
	recorder.waitForWrite(t)
	requestID := "conn-sse-pushed-frame"
	service.RegisterConnection(newConnectionTestRelayInfo(requestID), "erin", 77)
	t.Cleanup(func() { service.FinishConnection(requestID) })

	require.Eventually(t, func() bool {
		return bytes.Contains([]byte(recorder.bodyText()), []byte(requestID))
	}, 5*time.Second, 10*time.Millisecond,
		"a new connection must be pushed without the client asking for it")

	cancel()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream handler did not stop after the client disconnected")
	}
}
