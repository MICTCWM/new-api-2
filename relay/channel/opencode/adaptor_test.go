package opencode

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIRequestPreservesMappedUpstreamModel(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "custom-upstream-model"}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{IsModelMapped: true},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, "custom-upstream-model", convertedRequest.Model)
}

func TestConvertOpenAIRequestUsesFixedModelWithoutMapping(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: 61},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, Model, convertedRequest.Model)
}

func TestConvertOpenAIRequestAcceptsGlmModel(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: GlmModel}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: 61},
	}

	converted, err := (&Adaptor{}).ConvertOpenAIRequest(nil, info, request)
	require.NoError(t, err)
	convertedRequest, ok := converted.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Equal(t, GlmModel, convertedRequest.Model)
}

func TestConvertOpenAIRequestRejectsUnmappedModel(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{Model: "unsupported-model"}

	_, err := (&Adaptor{}).ConvertOpenAIRequest(nil, nil, request)
	require.ErrorContains(t, err, "unsupported model")
}

func TestGetRequestURLDoesNotDuplicateVersionPath(t *testing.T) {
	url, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions,
	})
	require.NoError(t, err)
	require.Equal(t, CanonicalURL, url)
}

func TestSetupRequestHeaderInjectsSessionAndCodingAgentUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", "python-requests/2.32.0")
	c.Request = req

	info := &relaycommon.RelayInfo{
		TokenId:         9,
		UserId:          3,
		OriginModelName: Model,
		IsStream:        true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey:    "test-key",
			ChannelId: 7,
		},
	}

	header := http.Header{}
	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	require.Equal(t, "Bearer test-key", header.Get("Authorization"))
	require.NotEmpty(t, header.Get(SessionHeader))
	require.Regexp(t, `^[0-9a-f]{32}$`, header.Get(SessionHeader))
	require.Contains(t, header.Get("User-Agent"), UserAgent)
	require.Equal(t, "text/event-stream", header.Get("Accept"))
}

func TestSetupRequestHeaderDoesNotForwardClientSessionHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-OpenCode-Session", "client-session-123")
	req.Header.Set("User-Agent", "claude-code/1.0")
	c.Request = req

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "test-key"},
	}
	header := http.Header{}
	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	session := header.Get(SessionHeader)
	require.NotEmpty(t, session)
	require.Regexp(t, `^[0-9a-f]{32}$`, session)
	require.NotEqual(t, "client-session-123", session)
	require.Equal(t, "claude-code/1.0", header.Get("User-Agent"))
	// SetupRequestHeader must not inject alternate session spellings into the
	// upstream header; alternate-spelling stripping runs later, in
	// SanitizeUpstreamHeaders (after header overrides are applied).
	require.Empty(t, header.Get("X-OpenCode-Session-Id"))
}

func TestResolveSessionIDStablePerConversation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := `{"model":"deepseek-v4-flash","prompt_cache_key":"cache-abc"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	storage, err := common.CreateBodyStorage([]byte(body))
	require.NoError(t, err)
	c.Set(common.KeyBodyStorage, storage)

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7}}
	session := ResolveSessionID(c, info)
	require.NotEmpty(t, session)
	require.Regexp(t, `^[0-9a-f]{32}$`, session)
	require.NotEqual(t, "cache-abc", session)
	require.Equal(t, session, ResolveSessionID(c, info))
}

func TestSanitizeUpstreamHeadersStripsAlternateSpellings(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Id", "s1")
	h.Set("Session-Id", "s2")
	h.Set("Session_id", "s3")
	h.Set("X-OpenCode-Session-Id", "s4")
	h.Set(SessionHeader, "canonical-session")

	(&Adaptor{}).SanitizeUpstreamHeaders(&h)

	require.Equal(t, "canonical-session", h.Get(SessionHeader), "canonical session header must be preserved")
	require.Empty(t, h.Get("X-OpenCode-Session-Id"), "alternate spelling must be stripped")
	require.Empty(t, h.Get("X-Session-Id"), "alternate spelling must be stripped")
	require.Empty(t, h.Get("Session-Id"), "alternate spelling must be stripped")
	require.Empty(t, h.Get("Session_id"), "alternate spelling must be stripped")
}

