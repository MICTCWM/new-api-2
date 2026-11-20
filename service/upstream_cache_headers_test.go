package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMshTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	return c, recorder
}

func newMshTestInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatOpenAI,
	}
}

func TestCaptureUpstreamMshCacheHeaders(t *testing.T) {
	c, recorder := newMshTestContext()
	info := newMshTestInfo()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
	}
	resp.Header.Set(MshCacheWrite5mHeader, "0")
	resp.Header.Set(MshCacheWrite1hHeader, "3584")
	resp.Header.Set("Access-Control-Expose-Headers", "Msh-Trace-Mode")

	captureUpstreamMshCacheHeadersChecked(c, info, resp, func(*relaycommon.RelayInfo) bool { return false })

	require.True(t, info.MshCacheWriteHeadersPresent)
	assert.Equal(t, 0, info.MshCacheWrite5mTokens)
	assert.Equal(t, 3584, info.MshCacheWrite1hTokens)
	assert.Equal(t, "0", recorder.Header().Get(MshCacheWrite5mHeader))
	assert.Equal(t, "3584", recorder.Header().Get(MshCacheWrite1hHeader))
	assert.Contains(t, recorder.Header().Get("Access-Control-Expose-Headers"), MshCacheWrite5mHeader)
	assert.Contains(t, recorder.Header().Get("Access-Control-Expose-Headers"), MshCacheWrite1hHeader)
	assert.Contains(t, recorder.Header().Get("Access-Control-Expose-Headers"), "Msh-Trace-Mode")
}

func TestCaptureUpstreamMshCacheHeadersIgnoredBySimulation(t *testing.T) {
	c, recorder := newMshTestContext()
	info := newMshTestInfo()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
	}
	resp.Header.Set(MshCacheWrite1hHeader, "3584")
	resp.Header.Set("Access-Control-Expose-Headers", MshCacheWrite5mHeader+", Msh-Trace-Mode")

	captureUpstreamMshCacheHeadersChecked(c, info, resp, func(*relaycommon.RelayInfo) bool { return true })

	assert.False(t, info.MshCacheWriteHeadersPresent)
	assert.Empty(t, resp.Header.Get(MshCacheWrite1hHeader), "simulation must strip upstream headers")
	assert.Empty(t, recorder.Header().Get(MshCacheWrite1hHeader))
	assert.NotContains(t, resp.Header.Get("Access-Control-Expose-Headers"), MshCacheWrite5mHeader)
	assert.Contains(t, resp.Header.Get("Access-Control-Expose-Headers"), "Msh-Trace-Mode")
}

func TestCaptureUpstreamMshCacheHeadersIgnoresInvalidValue(t *testing.T) {
	c, recorder := newMshTestContext()
	info := newMshTestInfo()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
	}
	resp.Header.Set(MshCacheWrite5mHeader, "not-a-number")
	resp.Header.Set(MshCacheWrite1hHeader, "3584")

	captureUpstreamMshCacheHeadersChecked(c, info, resp, func(*relaycommon.RelayInfo) bool { return false })

	require.True(t, info.MshCacheWriteHeadersPresent)
	assert.Equal(t, 0, info.MshCacheWrite5mTokens)
	assert.Equal(t, 3584, info.MshCacheWrite1hTokens)
	assert.Empty(t, recorder.Header().Get(MshCacheWrite5mHeader), "invalid upstream value must not be forwarded")
	assert.Equal(t, "3584", recorder.Header().Get(MshCacheWrite1hHeader))
}

func TestCaptureUpstreamMshCacheHeadersIgnoresBothInvalidValues(t *testing.T) {
	c, recorder := newMshTestContext()
	info := newMshTestInfo()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
	}
	resp.Header.Set(MshCacheWrite5mHeader, "x")
	resp.Header.Set(MshCacheWrite1hHeader, "-1")

	captureUpstreamMshCacheHeadersChecked(c, info, resp, func(*relaycommon.RelayInfo) bool { return false })

	assert.False(t, info.MshCacheWriteHeadersPresent)
	assert.Empty(t, recorder.Header().Get(MshCacheWrite5mHeader))
	assert.Empty(t, recorder.Header().Get(MshCacheWrite1hHeader))
}

func TestApplyUpstreamMshCacheUsage(t *testing.T) {
	info := newMshTestInfo()
	info.MshCacheWriteHeadersPresent = true
	info.MshCacheWrite1hTokens = 3584

	usage := &dto.Usage{
		PromptTokens:     3584,
		CompletionTokens: 8,
		UsageSemantic:    dto.BillingUsageSemanticOpenAI,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedCreationTokens: 3584,
			CacheWriteTokens:     3584,
		},
		BillingUsage: dto.NewOpenAIChatBillingUsage(&dto.Usage{
			PromptTokens:     3584,
			CompletionTokens: 8,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedCreationTokens: 3584,
				CacheWriteTokens:     3584,
			},
		}),
	}

	applied := ApplyUpstreamMshCacheUsage(info, usage)
	require.True(t, applied)
	assert.Equal(t, 0, usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 3584, usage.ClaudeCacheCreation1hTokens)
	assert.Equal(t, 3584, usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 3584, usage.PromptTokensDetails.CacheWriteTokens)
	require.NotNil(t, usage.BillingUsage)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, 3584, usage.BillingUsage.OpenAIUsage.ClaudeCacheCreation1hTokens)
	assert.Equal(t, 3584, usage.BillingUsage.OpenAIUsage.PromptTokensDetails.CachedCreationTokens)
}

func TestApplyUpstreamMshCacheUsageMarksOpenAISemanticWhenMissing(t *testing.T) {
	info := newMshTestInfo()
	info.MshCacheWriteHeadersPresent = true
	info.MshCacheWrite1hTokens = 1000
	usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 10}

	require.True(t, ApplyUpstreamMshCacheUsage(info, usage))
	assert.Equal(t, dto.BillingUsageSemanticOpenAI, usage.UsageSemantic)
	assert.Equal(t, 1000, usage.ClaudeCacheCreation1hTokens)
	assert.Equal(t, 1000, usage.PromptTokensDetails.CachedCreationTokens)
}

func TestApplyUpstreamMshCacheUsageFillsResponsesInputDetails(t *testing.T) {
	info := newMshTestInfo()
	info.MshCacheWriteHeadersPresent = true
	info.MshCacheWrite1hTokens = 1000
	usage := &dto.Usage{InputTokens: 1000, OutputTokens: 10}

	require.True(t, ApplyUpstreamMshCacheUsage(info, usage))
	require.NotNil(t, usage.InputTokensDetails)
	assert.Equal(t, 1000, usage.InputTokensDetails.CacheWriteTokens)
	assert.Equal(t, 1000, usage.InputTokensDetails.CachedCreationTokens)
}

func TestApplyUpstreamMshCacheUsageSkipsSimulatedPlan(t *testing.T) {
	info := newMshTestInfo()
	info.MshCacheWriteHeadersPresent = true
	info.MshCacheWrite1hTokens = 3584
	info.SimulatedCachePlan = &relaycommon.SimulatedCachePlan{
		Applied:    true,
		Creation5m: 100,
		Creation1h: 200,
	}

	usage := &dto.Usage{
		PromptTokens:                3584,
		CompletionTokens:            8,
		ClaudeCacheCreation5mTokens: 100,
		ClaudeCacheCreation1hTokens: 200,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedCreationTokens: 300,
			CacheWriteTokens:     300,
		},
	}

	assert.False(t, ApplyUpstreamMshCacheUsage(info, usage))
	assert.Equal(t, 100, usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 200, usage.ClaudeCacheCreation1hTokens)
	assert.Equal(t, 300, usage.PromptTokensDetails.CachedCreationTokens)
}

func TestApplyUpstreamMshCacheToClaudeUsage(t *testing.T) {
	info := newMshTestInfo()
	info.MshCacheWriteHeadersPresent = true
	info.MshCacheWrite5mTokens = 0
	info.MshCacheWrite1hTokens = 3845

	usage := &dto.ClaudeUsage{
		InputTokens:              9,
		CacheCreationInputTokens: 3845,
		CacheCreation:            &dto.ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 3845},
	}

	applied := ApplyUpstreamMshCacheToClaudeUsage(info, usage)
	require.True(t, applied)
	assert.Equal(t, 3845, usage.CacheCreationInputTokens)
	assert.Equal(t, 0, usage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 3845, usage.ClaudeCacheCreation1hTokens)
	require.NotNil(t, usage.CacheCreation)
	assert.Equal(t, 0, usage.CacheCreation.Ephemeral5mInputTokens)
	assert.Equal(t, 3845, usage.CacheCreation.Ephemeral1hInputTokens)
}

func TestIOCopyBytesGracefullyMergesMshExposeHeaders(t *testing.T) {
	c, recorder := newMshTestContext()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
	}
	resp.Header.Set(MshCacheWrite1hHeader, "3584")
	resp.Header.Set("Access-Control-Expose-Headers", "Msh-Trace-Mode")
	resp.Header.Set("Content-Type", "application/json")

	IOCopyBytesGracefully(c, resp, []byte(`{"ok":true}`))

	assert.Equal(t, "3584", recorder.Header().Get(MshCacheWrite1hHeader))
	assert.Contains(t, recorder.Header().Get("Access-Control-Expose-Headers"), "Msh-Trace-Mode")
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
}
