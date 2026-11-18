package openai

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func OaiChatToResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	var chatResp dto.OpenAITextResponse
	if err := common.Unmarshal(body, &chatResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := chatResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	// Command 渠道请求 reasoning_effort=none 时上游被改写为 high，客户端要求隐藏
	// 思维链。出站剥离（service.RewriteResponseBody）只识别 chat 形态，这里在
	// chat→Responses 转换前先清空 reasoning，避免转换器把它变成 Responses 的
	// reasoning output item 而绕过剥离、泄漏思维链。
	if service.ShouldHideCommandThinking(c) {
		for i := range chatResp.Choices {
			chatResp.Choices[i].Message.ReasoningContent = nil
			chatResp.Choices[i].Message.Reasoning = nil
		}
	}

	if responseID := helper.GetResponseID(c); responseID != "" {
		chatResp.Id = responseID
	}
	convertResult, err := relayconvert.ConvertResponse(c, info, types.RelayFormatOpenAIResponses, &chatResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	responsesResp, ok := convertResult.Value.(*dto.OpenAIResponsesResponse)
	if !ok {
		return nil, types.NewOpenAIError(fmt.Errorf("expected OpenAI responses response, got %T", convertResult.Value), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	usage := convertResult.Usage
	if usage == nil || usage.TotalTokens == 0 {
		text := service.ExtractOutputTextFromResponses(responsesResp)
		usage = service.ResponseText2Usage(c, text, info.UpstreamModelName, info.GetEstimatePromptTokens())
		responsesResp.Usage = relayconvert.UsageFromChatUsage(usage)
	} else if plan := service.PlanSimulatedCacheCreation(info,
		usage.PromptTokens,
		usage.PromptTokensDetails.CachedTokens,
		usage.PromptTokensDetails.CacheCreationTokensTotal(),
		usage.ClaudeCacheCreation5mTokens,
		usage.ClaudeCacheCreation1hTokens); plan != nil {
		// Chat→Responses 转换路径：plan 写入 Responses usage 后再序列化响应。
		service.ApplyPlanToResponsesUsage(plan, usage)
		responsesResp.Usage = relayconvert.UsageFromChatUsage(usage)
	}

	responseBody, err := common.Marshal(responsesResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	if cleaned, cleanErr := relaycommon.ReplaceResponsesUsageRawJSONClean(responseBody, usage); cleanErr == nil {
		responseBody = cleaned
	}

	service.IOCopyBytesGracefully(c, resp, responseBody)
	return usage, nil
}

func OaiChatToResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	responseID := helper.GetResponseID(c)
	state, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, relayconvert.ResponseStreamOptions{
		ID:    responseID,
		Model: info.UpstreamModelName,
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	streamErr := (*types.NewAPIError)(nil)
	// 标记是否已向客户端写过 SSE 数据：一旦写过，HTTP 200 即已提交，
	// 之后任何提前退出都必须先补发终止事件，否则客户端会报
	// "stream disconnected before completion"。
	streamBegan := false

	sendEvent := func(event relayconvert.ChatToResponsesStreamEvent) bool {
		data, err := common.Marshal(event.Payload)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
			return false
		}
		// 转换器生成的 completed/incomplete 事件会把 dto.Usage 全量序列化，
		// 从而带出 Chat 的 prompt_tokens 等字段。这里在发送前用干净的官方
		// Responses usage 形态覆盖，避免客户端看到混合 schema。
		switch event.Type {
		case "response.completed", "response.incomplete", "response.done":
			if event.Payload.Response != nil && event.Payload.Response.Usage != nil {
				if cleaned, cleanErr := relaycommon.ReplaceResponsesEventUsageRawJSONClean(string(data), event.Payload.Response.Usage); cleanErr == nil {
					data = []byte(cleaned)
				} else {
					streamErr = types.NewOpenAIError(cleanErr, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
					return false
				}
			}
		}
		helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: event.Type}, string(data))
		streamBegan = true
		return true
	}

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if streamErr != nil {
			sr.Stop(streamErr)
			return
		}

		var errorResp dto.OpenAITextResponse
		if err := common.UnmarshalJsonStr(data, &errorResp); err == nil {
			if oaiError := errorResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
				streamErr = types.WithOpenAIError(*oaiError, resp.StatusCode)
				sr.Stop(streamErr)
				return
			}
		}

		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			logger.LogError(c, "failed to unmarshal chat stream response: "+err.Error())
			sr.Error(err)
			return
		}

		// Command 渠道 hide 生效时，在 chat→Responses 转换前剥离思考链，避免转换器
		// 产出 Responses 形态的 reasoning 事件绕过出站剥离而泄漏。
		if service.ShouldHideCommandThinking(c) {
			stripChatChunkReasoning(&chunk)
		}

		results, err := relayconvert.ConvertStreamResponseChunk(c, info, state, &chunk)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			sr.Stop(streamErr)
			return
		}
		for _, result := range results {
			event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
			if !ok {
				streamErr = types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
				sr.Stop(streamErr)
				return
			}
			if !sendEvent(event) {
				sr.Stop(streamErr)
				return
			}
		}
	})

	if streamErr != nil {
		// 流已经写过数据时（HTTP 200 已提交，无法再改状态码/错误体），必须先
		// 补发 response.failed 终止事件，否则客户端会报 "stream disconnected
		// before completion" 而不是看到真实错误。
		if streamBegan {
			_ = emitFailureTerminalEvent(c, info, state, streamErr)
		}
		return nil, streamErr
	}

	usage := state.Usage()
	if usage == nil || usage.TotalTokens == 0 {
		usage = service.ResponseText2Usage(c, state.UsageText(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		state.SetUsage(usage)
	}
	if plan := service.PlanSimulatedCacheCreation(info,
		usage.PromptTokens,
		usage.PromptTokensDetails.CachedTokens,
		usage.PromptTokensDetails.CacheCreationTokensTotal(),
		usage.ClaudeCacheCreation5mTokens,
		usage.ClaudeCacheCreation1hTokens); plan != nil {
		service.ApplyPlanToResponsesUsage(plan, usage)
		state.SetUsage(usage)
	}

	finalResults, err := relayconvert.FinalizeStreamResponse(c, info, state)
	if err != nil {
		if streamBegan {
			_ = emitFailureTerminalEvent(c, info, state, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError))
		}
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	for _, result := range finalResults {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			return nil, types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		if !sendEvent(event) {
			return nil, streamErr
		}
	}

	return usage, nil
}

// emitFailureTerminalEvent 在流已向客户端写过数据、但后续出错要提前退出时，
// 先补发一个 response.failed 终止事件：尽量复用 FinalizeStreamResponse 把已
// 累积内容的 done 事件与最终 response 对象生成出来，再把终止事件从
// completed/incomplete 改为 failed（status 置 "failed" 并携带真实错误），
// 保证 codex/OpenAI SDK 总能收到终止信号；若复用 finalize 失败，则退回最小
// response.failed 事件。这样客户端看到的是真实错误而不是 "stream disconnected
// before completion"。
func emitFailureTerminalEvent(c *gin.Context, info *relaycommon.RelayInfo, state *relayconvert.ResponseStreamState, streamErr *types.NewAPIError) error {
	if streamErr == nil {
		return nil
	}
	oaiErr := streamErr.ToOpenAIError()

	// 复用 finalize：已累积内容的事件（含 done 事件）全部照常补发。
	if finalResults, err := relayconvert.FinalizeStreamResponse(c, info, state); err == nil {
		terminalSent := false
		for _, result := range finalResults {
			event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
			if !ok {
				continue
			}
			payload := event.Payload
			switch payload.Type {
			case "response.completed", "response.incomplete":
				payload.Type = "response.failed"
				if payload.Response != nil {
					payload.Response.Status = []byte(`"failed"`)
					payload.Response.Error = oaiErr
				}
				terminalSent = true
			}
			data, err := common.Marshal(payload)
			if err != nil {
				return err
			}
			helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: payload.Type}, string(data))
		}
		if terminalSent {
			return nil
		}
	}

	// 兜底：直接补发最小 response.failed 终止事件。
	minimal := dto.ResponsesStreamResponse{
		Type: "response.failed",
		Response: &dto.OpenAIResponsesResponse{
			ID:        helper.GetResponseID(c),
			CreatedAt: int(time.Now().Unix()),
			Model:     info.UpstreamModelName,
			Status:    []byte(`"failed"`),
			Error:     oaiErr,
		},
	}
	data, err := common.Marshal(minimal)
	if err != nil {
		return err
	}
	helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: minimal.Type}, string(data))
	return nil
}

// OaiChatToResponsesBufferedStreamHandler converts an upstream OpenAI Chat
// Completions SSE stream into a single (non-streamed) OpenAI Responses JSON
// response. It is used when the client asked for a non-streaming responses
// response but the chat-only upstream streamed anyway.
func OaiChatToResponsesBufferedStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	responseID := helper.GetResponseID(c)
	state, err := relayconvert.NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, relayconvert.ResponseStreamOptions{
		ID:    responseID,
		Model: info.UpstreamModelName,
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	accumulator := relayconvert.NewResponsesBufferedAccumulator()
	var streamErr *types.NewAPIError

	scanner := helper.NewStreamScanner(resp.Body)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 6 || line[:5] != "data:" {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" || data == "[DONE]" {
			continue
		}

		var chunk dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			break
		}
		// Command 渠道 hide 生效时，在 chat→Responses 转换前剥离思考链，避免转换器
		// 产出 Responses 形态的 reasoning 事件绕过出站剥离而泄漏。
		if service.ShouldHideCommandThinking(c) {
			stripChatChunkReasoning(&chunk)
		}

		var errorResp dto.OpenAITextResponse
		if err := common.UnmarshalJsonStr(data, &errorResp); err == nil {
			if oaiError := errorResp.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
				streamErr = types.WithOpenAIError(*oaiError, resp.StatusCode)
				break
			}
		}

		results, err := relayconvert.ConvertStreamResponseChunk(c, info, state, &chunk)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
			break
		}
		for _, result := range results {
			event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
			if !ok {
				streamErr = types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
				break
			}
			accumulator.ProcessEvent(&event.Payload)
		}
		if streamErr != nil {
			break
		}
	}
	if streamErr != nil {
		return nil, streamErr
	}
	if err := scanner.Err(); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	finalResults, err := relayconvert.FinalizeStreamResponse(c, info, state)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	for _, result := range finalResults {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			return nil, types.NewOpenAIError(fmt.Errorf("expected OAI responses stream event, got %T", result.Value), types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
		accumulator.ProcessEvent(&event.Payload)
	}

	usage := state.Usage()
	if usage == nil || usage.TotalTokens == 0 {
		usage = service.ResponseText2Usage(c, state.UsageText(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		state.SetUsage(usage)
	}
	if plan := service.PlanSimulatedCacheCreation(info,
		usage.PromptTokens,
		usage.PromptTokensDetails.CachedTokens,
		usage.PromptTokensDetails.CacheCreationTokensTotal(),
		usage.ClaudeCacheCreation5mTokens,
		usage.ClaudeCacheCreation1hTokens); plan != nil {
		service.ApplyPlanToResponsesUsage(plan, usage)
		state.SetUsage(usage)
	}

	responsesResp := &dto.OpenAIResponsesResponse{
		ID:        responseID,
		CreatedAt: int(time.Now().Unix()),
		Model:     info.UpstreamModelName,
		Status:    []byte(`"completed"`),
	}
	accumulator.SupplementResponseOutput(responsesResp)
	if usage != nil {
		responsesResp.Usage = relayconvert.UsageFromChatUsage(usage)
	}

	responseBody, err := common.Marshal(responsesResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	if cleaned, cleanErr := relaycommon.ReplaceResponsesUsageRawJSONClean(responseBody, usage); cleanErr == nil {
		responseBody = cleaned
	}
	service.IOCopyBytesGracefully(c, resp, responseBody)
	return usage, nil
}

// stripChatChunkReasoning 清空 chat 流式分片的 reasoning 字段，使 chat→Responses
// 转换器不会产出任何 reasoning 事件（response.reasoning_summary_text.delta 与
// reasoning output item）。只在 Command 渠道 hide 生效时调用：出站剥离
// （service.RewriteStreamChunk）只识别 chat 形态，reasoning 必须在转换前丢弃，
// 否则会被转换成 Responses 事件绕过剥离而泄漏思维链。
func stripChatChunkReasoning(chunk *dto.ChatCompletionsStreamResponse) {
	if chunk == nil {
		return
	}
	for i := range chunk.Choices {
		chunk.Choices[i].Delta.ReasoningContent = nil
		chunk.Choices[i].Delta.Reasoning = nil
	}
}
