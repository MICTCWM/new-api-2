package channel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type timeoutTestAdaptor struct {
	Adaptor
	baseURL string
}

func (a *timeoutTestAdaptor) GetRequestURL(*relaycommon.RelayInfo) (string, error) {
	return a.baseURL + "/v1/chat/completions", nil
}

func (a *timeoutTestAdaptor) SetupRequestHeader(*gin.Context, *http.Header, *relaycommon.RelayInfo) error {
	return nil
}

func newTimeoutTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	require.NotNil(t, c)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	return c
}

func TestDoApiRequestStopsWaitingForHeadersWhenUpstreamContextExpires(t *testing.T) {
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-serverDone:
		}
	}))
	t.Cleanup(func() {
		close(serverDone)
		server.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	t.Cleanup(cancel)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, UpstreamContext: ctx}

	_, err := DoApiRequest(&timeoutTestAdaptor{baseURL: server.URL}, newTimeoutTestContext(t), info, strings.NewReader("{}"))
	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, ctx.Err())
}

func TestDoApiRequestStopsReadingBodyWhenUpstreamContextExpires(t *testing.T) {
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-serverDone:
		}
	}))
	t.Cleanup(func() {
		close(serverDone)
		server.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	t.Cleanup(cancel)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, UpstreamContext: ctx}

	resp, err := DoApiRequest(&timeoutTestAdaptor{baseURL: server.URL}, newTimeoutTestContext(t), info, strings.NewReader("{}"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	readDone := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(resp.Body)
		readDone <- readErr
	}()

	select {
	case readErr := <-readDone:
		require.Error(t, readErr)
	case <-time.After(5 * time.Second):
		t.Fatal("response body read did not unblock after the upstream context expired")
	}
	require.Equal(t, context.DeadlineExceeded, ctx.Err())
}

func TestDoApiRequestWithoutUpstreamContextKeepsPreviousBehavior(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	resp, err := DoApiRequest(&timeoutTestAdaptor{baseURL: server.URL}, newTimeoutTestContext(t), &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}, strings.NewReader("{}"))
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(body))
}
