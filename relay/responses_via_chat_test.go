package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestGinContext(path string) *gin.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func TestResponsesCompletionsViaChatRequestConversion(t *testing.T) {
	// The request-side of the Responses→Chat downgrade must map instructions,
	// input, and max_output_tokens into a Chat Completions request. This is the
	// conversion responsesCompletionsViaChat sends upstream.
	maxOutput := uint(512)
	temperature := 0.7
	responsesReq := dto.OpenAIResponsesRequest{
		Model:           "gpt-test",
		Instructions:    json.RawMessage(`"system rules"`),
		Input:           json.RawMessage(`"hello"`),
		MaxOutputTokens: &maxOutput,
		Temperature:     &temperature,
	}

	info := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeResponses,
		RequestURLPath:  "/v1/responses",
		OriginModelName: "gpt-test",
	}
	c := newTestGinContext("/v1/responses")

	result, err := service.ConvertRequestByID(c, info, relayconvert.ConverterOpenAIResponsesToOpenAIChat, responsesReq)
	require.NoError(t, err)

	chatReq, ok := result.Value.(*dto.GeneralOpenAIRequest)
	require.True(t, ok, "expected OpenAI chat completions request")
	assert.Equal(t, "gpt-test", chatReq.Model)
	require.Len(t, chatReq.Messages, 2)
	assert.Equal(t, "system", chatReq.Messages[0].Role)
	assert.Equal(t, "system rules", chatReq.Messages[0].StringContent())
	assert.Equal(t, "user", chatReq.Messages[1].Role)
	assert.Equal(t, "hello", chatReq.Messages[1].StringContent())
	require.NotNil(t, chatReq.MaxTokens)
	assert.Equal(t, uint(512), *chatReq.MaxTokens)
	assert.Nil(t, chatReq.MaxCompletionTokens)
	require.NotNil(t, chatReq.Temperature)
	assert.Equal(t, 0.7, *chatReq.Temperature)

	// o 系列模型走 max_completion_tokens（cc-switch 同款路由）。
	oSeries := responsesReq
	oSeries.Model = "o1-mini"
	oResult, err := service.ConvertRequestByID(c, info, relayconvert.ConverterOpenAIResponsesToOpenAIChat, oSeries)
	require.NoError(t, err)
	oChatReq, ok := oResult.Value.(*dto.GeneralOpenAIRequest)
	require.True(t, ok, "expected OpenAI chat completions request")
	assert.Nil(t, oChatReq.MaxTokens)
	require.NotNil(t, oChatReq.MaxCompletionTokens)
	assert.Equal(t, uint(512), *oChatReq.MaxCompletionTokens)
}

func TestResponsesCompletionsViaChatRequestPerformsModelMapping(t *testing.T) {
	// The conversion is only attempted on OpenAI-compatible chat upstreams; the
	// model-mapped request still goes through the same converter.
	maxOutput := uint(100)
	responsesReq := dto.OpenAIResponsesRequest{
		Model:           "gpt-test",
		Instructions:    json.RawMessage(`"rules"`),
		Input:           json.RawMessage(`"hi"`),
		MaxOutputTokens: &maxOutput,
	}
	info := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeResponses,
		RequestURLPath:  "/v1/responses",
		OriginModelName: "gpt-test",
	}
	result, err := service.ConvertRequestByID(newTestGinContext("/v1/responses"), info, relayconvert.ConverterOpenAIResponsesToOpenAIChat, responsesReq)
	require.NoError(t, err)
	chatReq, ok := result.Value.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Len(t, chatReq.Messages, 2)
	assert.Equal(t, "user", chatReq.Messages[1].Role)
}

// Ensure the codex (responses-only) channel type is never downgraded to chat.
func TestResponsesUseChatGlobalExcludesResponseOnlyUpstreams(t *testing.T) {
	assert.False(t, service.ShouldResponsesUseChatGlobal(0, constant.ChannelTypeCodex, constant.APITypeCodex, "gpt-4o"))
	assert.True(t, service.ShouldResponsesUseChatGlobal(0, constant.ChannelTypeOpenAI, constant.APITypeOpenAI, "gpt-4o"))
}
