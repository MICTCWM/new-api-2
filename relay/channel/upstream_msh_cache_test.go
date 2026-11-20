package channel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoRequestCapturesUpstreamMshCacheHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(service.MshCacheWrite5mHeader, "0")
		w.Header().Set(service.MshCacheWrite1hHeader, "3584")
		w.Header().Set("Access-Control-Expose-Headers", "Msh-Trace-Mode")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	req, err := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader(`{}`))
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
		ChannelMeta:             &relaycommon.ChannelMeta{},
	}
	info.ChannelMeta.ChannelSetting.PassThroughBodyEnabled = true

	resp, err := doRequest(c, req, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	require.True(t, info.MshCacheWriteHeadersPresent)
	assert.Equal(t, 0, info.MshCacheWrite5mTokens)
	assert.Equal(t, 3584, info.MshCacheWrite1hTokens)
	assert.Equal(t, "0", recorder.Header().Get(service.MshCacheWrite5mHeader))
	assert.Equal(t, "3584", recorder.Header().Get(service.MshCacheWrite1hHeader))
	assert.Contains(t, recorder.Header().Get("Access-Control-Expose-Headers"), "Msh-Trace-Mode")
}
