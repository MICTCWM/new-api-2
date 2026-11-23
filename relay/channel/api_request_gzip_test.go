package channel

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gzipTestAdaptor struct {
	Adaptor
	baseURL string
}

func (a *gzipTestAdaptor) GetRequestURL(*relaycommon.RelayInfo) (string, error) {
	return a.baseURL + "/v1/chat/completions", nil
}

func (a *gzipTestAdaptor) SetupRequestHeader(*gin.Context, *http.Header, *relaycommon.RelayInfo) error {
	return nil
}

type gzipCapturedRequest struct {
	encoding string
	body     []byte
}

func newGzipTestContext(t *testing.T, contentType string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	c.Request.Header.Set("Content-Type", contentType)
	return c
}

func gzipTestPayload() []byte {
	return []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"` + strings.Repeat("gzip compression pays off for long prompts. ", 200) + `"}],"stream":true}`)
}

func decodeCapturedGzip(t *testing.T, captured gzipCapturedRequest) []byte {
	t.Helper()
	require.Equal(t, "gzip", captured.encoding)
	reader, err := gzip.NewReader(bytes.NewReader(captured.body))
	require.NoError(t, err)
	defer reader.Close()
	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	return decoded
}

func TestDoApiRequestGzipCompressesJSONAndFallsBackOnHTTPError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := gzipTestPayload()

	var mu sync.Mutex
	var requests []gzipCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		requests = append(requests, gzipCapturedRequest{encoding: r.Header.Get("Content-Encoding"), body: body})
		index := len(requests)
		mu.Unlock()
		if index == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported content encoding"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	body, closer, err := relaycommon.NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()

	c := newGzipTestContext(t, "application/json")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelSetting: dto.ChannelSettings{GzipEnabled: true},
	}}
	resp, err := DoApiRequest(&gzipTestAdaptor{baseURL: server.URL}, c, info, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2, "gzip attempt plus one uncompressed replay")

	require.Equal(t, "gzip", requests[0].encoding)
	assert.JSONEq(t, string(payload), string(decodeCapturedGzip(t, requests[0])))
	assert.Less(t, len(requests[0].body), len(payload), "test payload should compress")

	assert.Empty(t, requests[1].encoding)
	assert.Equal(t, payload, requests[1].body)
}

func TestDoApiRequestGzipKeepsOriginalWhenCompressionIsLarger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := []byte(`{"a":1}`)

	var mu sync.Mutex
	var requests []gzipCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		requests = append(requests, gzipCapturedRequest{encoding: r.Header.Get("Content-Encoding"), body: body})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	body, closer, err := relaycommon.NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()

	c := newGzipTestContext(t, "application/json")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelSetting: dto.ChannelSettings{GzipEnabled: true},
	}}
	resp, err := DoApiRequest(&gzipTestAdaptor{baseURL: server.URL}, c, info, body)
	require.NoError(t, err)
	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1, "a larger compressed body must not be sent")
	assert.Empty(t, requests[0].encoding)
	assert.Equal(t, payload, requests[0].body)
}

func TestDoApiRequestGzipStaysDisabledWhenChannelSwitchIsOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := gzipTestPayload()

	var mu sync.Mutex
	var requests []gzipCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		requests = append(requests, gzipCapturedRequest{encoding: r.Header.Get("Content-Encoding"), body: body})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	body, closer, err := relaycommon.NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()

	c := newGzipTestContext(t, "application/json")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelSetting: dto.ChannelSettings{GzipEnabled: false},
	}}
	resp, err := DoApiRequest(&gzipTestAdaptor{baseURL: server.URL}, c, info, body)
	require.NoError(t, err)
	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1)
	assert.Empty(t, requests[0].encoding)
	assert.Equal(t, payload, requests[0].body)
}

func TestDoApiRequestGzipDoesNotReplaySuccessfulResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := gzipTestPayload()

	var mu sync.Mutex
	var requests []gzipCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		requests = append(requests, gzipCapturedRequest{encoding: r.Header.Get("Content-Encoding"), body: body})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	body, closer, err := relaycommon.NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()

	c := newGzipTestContext(t, "application/json")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelSetting: dto.ChannelSettings{GzipEnabled: true},
	}}
	resp, err := DoApiRequest(&gzipTestAdaptor{baseURL: server.URL}, c, info, body)
	require.NoError(t, err)
	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1)
	assert.Equal(t, "gzip", requests[0].encoding)
	assert.JSONEq(t, string(payload), string(decodeCapturedGzip(t, requests[0])))
}

func TestDoApiRequestGzipSkipsNonJSONRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := gzipTestPayload()

	var mu sync.Mutex
	var requests []gzipCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		requests = append(requests, gzipCapturedRequest{encoding: r.Header.Get("Content-Encoding"), body: body})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	body, closer, err := relaycommon.NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()

	c := newGzipTestContext(t, "text/plain")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelSetting: dto.ChannelSettings{GzipEnabled: true},
	}}
	resp, err := DoApiRequest(&gzipTestAdaptor{baseURL: server.URL}, c, info, body)
	require.NoError(t, err)
	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1)
	assert.Empty(t, requests[0].encoding)
	assert.Equal(t, payload, requests[0].body)
}

func TestDoApiRequestGzipFallsBackAfterNetworkError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := gzipTestPayload()

	var mu sync.Mutex
	var requests []gzipCapturedRequest
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		index := attempts
		mu.Unlock()
		if index == 1 {
			hijacker, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hijacker.Hijack()
			require.NoError(t, err)
			_ = conn.Close()
			return
		}
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		mu.Lock()
		requests = append(requests, gzipCapturedRequest{encoding: r.Header.Get("Content-Encoding"), body: body})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	body, closer, err := relaycommon.NewOutboundJSONBody(payload)
	require.NoError(t, err)
	defer closer.Close()

	c := newGzipTestContext(t, "application/json")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelSetting: dto.ChannelSettings{GzipEnabled: true},
	}}
	resp, err := DoApiRequest(&gzipTestAdaptor{baseURL: server.URL}, c, info, body)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1, "the failed gzip attempt is not captured and the replay succeeds")
	assert.Empty(t, requests[0].encoding)
	assert.Equal(t, payload, requests[0].body)
}

func TestPrepareAPIRequestBodyRejectsInvalidJSONHeadersOnlyByContentType(t *testing.T) {
	// Guardrail: the compressor keys off the declared content type, not the
	// payload contents, so non-JSON relay bodies are never rewritten.
	c := newGzipTestContext(t, "application/x-www-form-urlencoded")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelSetting: dto.ChannelSettings{GzipEnabled: true},
	}}
	payload := []byte(`{"model":"gpt-test"}`)
	prepared, err := prepareAPIRequestBody(c, info, bytes.NewReader(payload))
	require.NoError(t, err)
	assert.False(t, prepared.compressed)
	assert.Equal(t, int64(len(payload)), prepared.firstSize)
}

func TestGzipCompressedBodyIsValidJSONAfterDecode(t *testing.T) {
	payload := gzipTestPayload()
	compressed, err := compressBytes(payload)
	require.NoError(t, err)
	decoded := decodeCapturedGzip(t, gzipCapturedRequest{encoding: "gzip", body: compressed})
	var value map[string]any
	require.NoError(t, json.Unmarshal(decoded, &value))
	assert.Equal(t, "gpt-test", value["model"])
}
