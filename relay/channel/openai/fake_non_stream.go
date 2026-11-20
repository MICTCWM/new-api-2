package openai

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// readSSEData 按 SSE 规范把 data: 行拼成一条完整消息。Chat 流式上游通常
// 一帧只有一行 data，但代理/合规层可能把同一帧拆成多行，这里统一处理。
func readSSEData(body io.Reader, onData func([]byte) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(payload) == "" {
			return nil
		}
		return onData([]byte(payload))
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func fakeNonStreamJSONResponse(resp *http.Response, body []byte) *http.Response {
	clone := *resp
	clone.Header = resp.Header.Clone()
	clone.Header.Set("Content-Type", "application/json")
	clone.Header.Del("Content-Length")
	clone.Header.Del("Content-Encoding")
	clone.Header.Del("Transfer-Encoding")
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	return &clone
}

type fakeChatChoice struct {
	index        int
	role         string
	content      strings.Builder
	reasoning    strings.Builder
	toolCalls    map[int]*dto.ToolCallResponse
	toolOrder    []int
	finishReason string
}

// OaiChatBufferedStreamHandler 接收上游 Chat SSE，聚合成一个普通
// chat.completion JSON，再复用 OpenaiHandler 完成 usage 归一化、Msh 头、
// 模拟缓存、协议转换和结算。计费字段完全沿用上游最后一帧 usage。
func OaiChatBufferedStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("empty upstream response"), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	choices := make(map[int]*fakeChatChoice)
	var responseID, model string
	var created int64
	var systemFingerprint string
	var usage dto.Usage
	hasUsage := false

	err := readSSEData(resp.Body, func(data []byte) error {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nil
		}
		var chunk dto.ChatCompletionsStreamResponse
		if err := common.Unmarshal(data, &chunk); err != nil {
			return fmt.Errorf("parse chat stream chunk: %w", err)
		}
		if chunk.Id != "" {
			responseID = chunk.Id
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Created != 0 {
			created = chunk.Created
		}
		if chunk.SystemFingerprint != nil {
			systemFingerprint = *chunk.SystemFingerprint
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
			hasUsage = true
		}
		for _, choice := range chunk.Choices {
			agg := choices[choice.Index]
			if agg == nil {
				agg = &fakeChatChoice{index: choice.Index, role: "assistant", toolCalls: make(map[int]*dto.ToolCallResponse)}
				choices[choice.Index] = agg
			}
			if choice.Delta.Role != "" {
				agg.role = choice.Delta.Role
			}
			if choice.Delta.Content != nil {
				agg.content.WriteString(*choice.Delta.Content)
			}
			if reasoning := choice.Delta.GetReasoningContent(); reasoning != "" {
				agg.reasoning.WriteString(reasoning)
			}
			for _, call := range choice.Delta.ToolCalls {
				callIdx := len(agg.toolOrder)
				if call.Index != nil {
					callIdx = *call.Index
				}
				existing := agg.toolCalls[callIdx]
				if existing == nil {
					existing = &dto.ToolCallResponse{}
					agg.toolCalls[callIdx] = existing
					agg.toolOrder = append(agg.toolOrder, callIdx)
				}
				if call.ID != "" {
					existing.ID = call.ID
				}
				if call.Type != nil {
					existing.Type = call.Type
				}
				existing.Function.Name += call.Function.Name
				existing.Function.Arguments += call.Function.Arguments
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				agg.finishReason = *choice.FinishReason
			}
		}
		return nil
	})
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}

	if len(choices) == 0 {
		return nil, types.NewOpenAIError(fmt.Errorf("upstream chat stream contained no choices"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if model == "" {
		model = info.UpstreamModelName
	}
	if created == 0 {
		created = common.GetTimestamp()
	}

	choiceIndexes := make([]int, 0, len(choices))
	for idx := range choices {
		choiceIndexes = append(choiceIndexes, idx)
	}
	sort.Ints(choiceIndexes)
	finalChoices := make([]dto.OpenAITextResponseChoice, 0, len(choiceIndexes))
	for _, idx := range choiceIndexes {
		agg := choices[idx]
		message := dto.Message{Role: agg.role, Content: agg.content.String()}
		if reasoning := agg.reasoning.String(); reasoning != "" {
			message.ReasoningContent = &reasoning
		}
		if len(agg.toolCalls) > 0 {
			ordered := make([]dto.ToolCallResponse, 0, len(agg.toolCalls))
			for _, callIdx := range agg.toolOrder {
				call := agg.toolCalls[callIdx]
				call.Index = nil
				if call.Type == nil {
					call.Type = "function"
				}
				ordered = append(ordered, *call)
			}
			raw, err := common.Marshal(ordered)
			if err != nil {
				return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			}
			message.ToolCalls = raw
		}
		finishReason := agg.finishReason
		if finishReason == "" {
			if len(agg.toolCalls) > 0 {
				finishReason = "tool_calls"
			} else {
				finishReason = "stop"
			}
		}
		finalChoices = append(finalChoices, dto.OpenAITextResponseChoice{
			Index:        idx,
			Message:      message,
			FinishReason: finishReason,
		})
	}

	final := dto.OpenAITextResponse{
		Id:      responseID,
		Model:   model,
		Object:  "chat.completion",
		Created: created,
		Choices: finalChoices,
	}
	if hasUsage {
		final.Usage = usage
	}
	if systemFingerprint != "" {
		// OpenAITextResponse 没有 system_fingerprint 字段；上游该字段只用于
		// 观测，不参与 usage/计费，聚合响应里省略是安全的。
		_ = systemFingerprint
	}
	body, err := common.Marshal(final)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	// 复用正常非流式 handler：usage、Msh 分档、模拟缓存、下游协议转换和
	// 响应写出都只保留一条实现路径。
	return OpenaiHandler(c, info, fakeNonStreamJSONResponse(resp, body))
}

// OaiResponsesBufferedStreamHandler 接收 /v1/responses SSE，取
// response.completed / response.done 里的完整 response 对象，聚合成普通
// JSON 后复用 OaiResponsesHandler 结算与写出。
func OaiResponsesBufferedStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("empty upstream response"), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	var completed *dto.OpenAIResponsesResponse
	err := readSSEData(resp.Body, func(data []byte) error {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nil
		}
		var event dto.ResponsesStreamResponse
		if err := common.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("parse responses stream event: %w", err)
		}
		if event.Response == nil {
			return nil
		}
		switch event.Type {
		case "response.completed", "response.done", "response.incomplete":
			completed = event.Response
		case "response.failed", "error":
			if oaiErr := event.Response.GetOpenAIError(); oaiErr != nil && oaiErr.Type != "" {
				return types.WithOpenAIError(*oaiErr, http.StatusBadGateway)
			}
			return fmt.Errorf("upstream responses stream failed")
		}
		return nil
	})
	if err != nil {
		if apiErr, ok := err.(*types.NewAPIError); ok {
			return nil, apiErr
		}
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if completed == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("upstream responses stream contained no completed response"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	body, err := common.Marshal(completed)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	return OaiResponsesHandler(c, info, fakeNonStreamJSONResponse(resp, body))
}
