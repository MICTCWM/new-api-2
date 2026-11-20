package claude

import (
	"bufio"
	"bytes"
	"errors"
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

var errClaudeSSEComplete = errors.New("fake non-stream claude sse complete")

func readClaudeSSEData(body io.Reader, onData func([]byte) error) error {
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

func closeClaudeBodyWhenRequestDone(c *gin.Context, body io.ReadCloser) func() {
	if c == nil || c.Request == nil || body == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-c.Request.Context().Done():
			_ = body.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func fakeClaudeJSONResponse(resp *http.Response, body []byte) *http.Response {
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

type fakeClaudeBlock struct {
	index   int
	block   dto.ClaudeMediaMessage
	partial strings.Builder
}

// ClaudeBufferedStreamHandler 接收 Anthropic Messages SSE，聚合成一个
// 普通 message JSON，再复用 ClaudeHandler 完成 usage/Msh/模拟缓存/结算。
func ClaudeBufferedStreamHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewError(fmt.Errorf("empty upstream response"), types.ErrorCodeBadResponseBody)
	}
	defer service.CloseResponseBodyGracefully(resp)
	defer closeClaudeBodyWhenRequestDone(c, resp.Body)()

	var (
		responseID    string
		model         string
		role          = "assistant"
		stopReason    string
		startUsage    dto.ClaudeUsage
		deltaUsage    dto.ClaudeUsage
		hasDeltaUsage bool
		blocks        = make(map[int]*fakeClaudeBlock)
	)
	err := readClaudeSSEData(resp.Body, func(data []byte) error {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nil
		}
		var event dto.ClaudeResponse
		if err := common.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("parse claude stream event: %w", err)
		}
		if claudeErr := event.GetClaudeError(); claudeErr != nil && claudeErr.Type != "" {
			return types.WithClaudeError(*claudeErr, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				responseID = event.Message.Id
				if event.Message.Model != "" {
					model = event.Message.Model
					info.UpstreamModelName = event.Message.Model
				}
				if event.Message.Role != "" {
					role = event.Message.Role
				}
				if event.Message.Usage != nil {
					startUsage = *event.Message.Usage
				}
			}
		case "content_block_start":
			if event.ContentBlock != nil {
				idx := event.GetIndex()
				block := *event.ContentBlock
				blocks[idx] = &fakeClaudeBlock{index: idx, block: block}
			}
		case "content_block_delta":
			if event.Delta == nil {
				break
			}
			idx := event.GetIndex()
			agg := blocks[idx]
			if agg == nil {
				agg = &fakeClaudeBlock{index: idx, block: dto.ClaudeMediaMessage{Type: "text"}}
				blocks[idx] = agg
			}
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != nil {
					text := agg.block.GetText() + *event.Delta.Text
					agg.block.Text = &text
				}
			case "thinking_delta":
				if event.Delta.Thinking != nil {
					thinking := ""
					if agg.block.Thinking != nil {
						thinking = *agg.block.Thinking
					}
					thinking += *event.Delta.Thinking
					agg.block.Thinking = &thinking
				}
			case "input_json_delta":
				if event.Delta.PartialJson != nil {
					agg.partial.WriteString(*event.Delta.PartialJson)
				}
			case "signature_delta":
				if event.Delta.Signature != "" {
					agg.block.Signature += event.Delta.Signature
				}
			default:
				// 兼容上游把文本放在 delta.text 而不是 type 上的情况。
				if event.Delta.Text != nil {
					text := agg.block.GetText() + *event.Delta.Text
					agg.block.Text = &text
				}
			}
		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != nil {
				stopReason = *event.Delta.StopReason
			}
			if event.StopReason != "" {
				stopReason = event.StopReason
			}
			if event.Usage != nil {
				deltaUsage = *event.Usage
				hasDeltaUsage = true
			}
		case "message_stop":
			// 结束标记；聚合已完成，无需额外字段。
			return errClaudeSSEComplete
		case "error":
			if claudeErr := event.GetClaudeError(); claudeErr != nil && claudeErr.Type != "" {
				return types.WithClaudeError(*claudeErr, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			}
			return fmt.Errorf("upstream claude stream failed")
		}
		return nil
	})
	if err != nil && !errors.Is(err, errClaudeSSEComplete) {
		if apiErr, ok := err.(*types.NewAPIError); ok {
			return nil, apiErr
		}
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if len(blocks) == 0 && responseID == "" {
		return nil, types.NewError(fmt.Errorf("upstream claude stream contained no message"), types.ErrorCodeBadResponseBody)
	}

	indexes := make([]int, 0, len(blocks))
	for idx := range blocks {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	content := make([]dto.ClaudeMediaMessage, 0, len(indexes))
	for _, idx := range indexes {
		agg := blocks[idx]
		if agg.block.Type == "tool_use" && agg.partial.Len() > 0 {
			var input any
			if err := common.Unmarshal([]byte(agg.partial.String()), &input); err == nil {
				agg.block.Input = input
			} else {
				agg.block.Input = agg.partial.String()
			}
		}
		agg.block.PartialJson = nil
		content = append(content, agg.block)
	}

	finalUsage := startUsage
	if hasDeltaUsage {
		if deltaUsage.InputTokens > 0 {
			finalUsage.InputTokens = deltaUsage.InputTokens
		}
		if deltaUsage.OutputTokens > 0 {
			finalUsage.OutputTokens = deltaUsage.OutputTokens
		}
		if deltaUsage.CacheCreationInputTokens > 0 {
			finalUsage.CacheCreationInputTokens = deltaUsage.CacheCreationInputTokens
		}
		if deltaUsage.CacheReadInputTokens > 0 {
			finalUsage.CacheReadInputTokens = deltaUsage.CacheReadInputTokens
		}
		if deltaUsage.CacheCreation != nil {
			finalUsage.CacheCreation = deltaUsage.CacheCreation
		}
		if deltaUsage.ClaudeCacheCreation5mTokens > 0 {
			finalUsage.ClaudeCacheCreation5mTokens = deltaUsage.ClaudeCacheCreation5mTokens
		}
		if deltaUsage.ClaudeCacheCreation1hTokens > 0 {
			finalUsage.ClaudeCacheCreation1hTokens = deltaUsage.ClaudeCacheCreation1hTokens
		}
		if deltaUsage.ServerToolUse != nil {
			finalUsage.ServerToolUse = deltaUsage.ServerToolUse
		}
	}
	if finalUsage.InputTokens == 0 && finalUsage.OutputTokens == 0 && finalUsage.GetCacheCreationTotalTokens() == 0 {
		// 上游没有返回任何 usage 时，按文本估算兜底；否则 ClaudeHandler 会判为
		// 无账单用量并整单退款。正常路径仍使用上游 usage。
		var text strings.Builder
		for _, block := range content {
			text.WriteString(block.GetText())
		}
		estimated := service.ResponseText2Usage(c, text.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		finalUsage.InputTokens = estimated.PromptTokens
		finalUsage.OutputTokens = estimated.CompletionTokens
	}

	final := dto.ClaudeResponse{
		Id:         responseID,
		Type:       "message",
		Role:       role,
		Content:    content,
		StopReason: stopReason,
		Model:      model,
		Usage:      &finalUsage,
	}
	body, err := common.Marshal(final)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	return ClaudeHandler(c, fakeClaudeJSONResponse(resp, body), info)
}
