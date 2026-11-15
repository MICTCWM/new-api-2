package controller

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldApplyNonStreamTimeout(t *testing.T) {
	old := common.NonStreamTimeout
	common.NonStreamTimeout = 300
	t.Cleanup(func() { common.NonStreamTimeout = old })

	nonStream := func(mode int) *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{RelayMode: mode}
	}

	tests := []struct {
		name   string
		format types.RelayFormat
		info   *relaycommon.RelayInfo
		want   bool
	}{
		{name: "openai non-stream", format: types.RelayFormatOpenAI, info: nonStream(0), want: true},
		{name: "claude non-stream", format: types.RelayFormatClaude, info: nonStream(0), want: true},
		{name: "gemini non-stream", format: types.RelayFormatGemini, info: nonStream(0), want: true},
		{name: "responses non-stream", format: types.RelayFormatOpenAIResponses, info: nonStream(0), want: true},
		{name: "responses compaction", format: types.RelayFormatOpenAIResponsesCompaction, info: nonStream(0), want: true},
		{name: "openai stream", format: types.RelayFormatOpenAI, info: &relaycommon.RelayInfo{IsStream: true}, want: false},
		{name: "embeddings", format: types.RelayFormatOpenAI, info: nonStream(relayconstant.RelayModeEmbeddings), want: false},
		{name: "gemini embeddings", format: types.RelayFormatGemini, info: nonStream(relayconstant.RelayModeEmbeddings), want: false},
		{name: "alpha search", format: types.RelayFormatOpenAIAlphaSearch, info: nonStream(relayconstant.RelayModeAlphaSearch), want: false},
		{name: "image", format: types.RelayFormatOpenAIImage, info: nonStream(0), want: false},
		{name: "audio", format: types.RelayFormatOpenAIAudio, info: nonStream(0), want: false},
		{name: "task", format: types.RelayFormatTask, info: nonStream(0), want: false},
		{name: "nil info", format: types.RelayFormatOpenAI, info: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldApplyNonStreamTimeout(tt.format, tt.info))
		})
	}

	common.NonStreamTimeout = 0
	assert.False(t, shouldApplyNonStreamTimeout(types.RelayFormatOpenAI, nonStream(0)))
}

func TestApplyNonStreamTimeoutError(t *testing.T) {
	old := common.NonStreamTimeout
	common.NonStreamTimeout = 300
	t.Cleanup(func() { common.NonStreamTimeout = old })

	readErr := types.NewOpenAIError(
		errors.New("read tcp: connection reset by peer"),
		types.ErrorCodeReadResponseBodyFailed,
		http.StatusInternalServerError,
	)

	t.Run("missing timeout context keeps original error", func(t *testing.T) {
		assert.Same(t, readErr, applyNonStreamTimeoutError(nil, readErr))
		assert.Same(t, readErr, applyNonStreamTimeoutError(&relaycommon.RelayInfo{}, readErr))
	})

	t.Run("active timeout context keeps original error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		info := &relaycommon.RelayInfo{UpstreamContext: ctx}
		assert.Same(t, readErr, applyNonStreamTimeoutError(info, readErr))
	})

	t.Run("expired timeout context returns 504", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		info := &relaycommon.RelayInfo{UpstreamContext: ctx}

		got := applyNonStreamTimeoutError(info, readErr)
		require.NotNil(t, got)
		assert.Equal(t, http.StatusGatewayTimeout, got.StatusCode)
		assert.Equal(t, types.ErrorCodeDoRequestFailed, got.GetErrorCode())
		assert.True(t, types.IsSkipRetryError(got))
		assert.Contains(t, got.Error(), "timed out after 300 seconds")
	})
}
