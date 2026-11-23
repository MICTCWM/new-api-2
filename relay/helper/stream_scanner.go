package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// streamWriteTimeout bounds a single blocked write to a slow client so the
	// unconditional wg.Wait() in cleanup can always finish. Without it, a slow
	// but connected client (full TCP buffer, no server WriteTimeout) could hang
	// the handler forever.
	streamWriteTimeout = 30 * time.Second
)

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

func NewStreamScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, InitialScannerBufferSize), getScannerBufferSize())
	return scanner
}

func copyCodexSSEHeaders(c *gin.Context, resp *http.Response) {
	if c == nil || c.Writer == nil || resp == nil {
		return
	}
	// codex
	for _, name := range []string{"X-Reasoning-Included", "X-Codex-Turn-State"} {
		values := resp.Header.Values(name)
		if !service.ShouldCopyUpstreamHeader(c, name, values) {
			continue
		}
		for _, value := range values {
			if value != "" {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

// ExtendWriteDeadline pushes the connection write deadline forward before each
// stream write. Best-effort: writers that don't support deadlines (e.g.
// httptest recorders) are silently ignored.
func ExtendWriteDeadline(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) *types.NewAPIError {

	if resp == nil || dataHandler == nil {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("upstream returned an empty streaming response"),
			types.ErrorCodeEmptyResponse,
			http.StatusBadGateway,
		)
	}
	if info == nil {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("relay info is nil"),
			types.ErrorCodeEmptyResponse,
			http.StatusInternalServerError,
		)
	}

	// 无条件新建 StreamStatus
	info.StreamStatus = relaycommon.NewStreamStatus()

	ctx, cancel := context.WithCancel(context.Background())

	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second

	var (
		stopChan    = make(chan bool, 3) // 增加缓冲区避免阻塞
		scanner     = NewStreamScanner(resp.Body)
		ticker      = time.NewTicker(streamingTimeout)
		pingTicker  *time.Ticker
		writeMutex  sync.Mutex     // Mutex to protect concurrent writes
		wg          sync.WaitGroup // 用于等待所有 goroutine 退出
		cleanupOnce sync.Once
		stopOnce    sync.Once
	)

	stop := func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
	}

	generalSettings := operation_setting.GetGeneralSetting()
	pingEnabled := generalSettings.PingIntervalEnabled && !info.DisablePing
	pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}

	if pingEnabled {
		pingTicker = time.NewTicker(pingInterval)
	}

	logger.LogDebug(c, "relay timeout seconds: %d", common.RelayTimeout)
	logger.LogDebug(c, "relay max idle conns: %d", common.RelayMaxIdleConns)
	logger.LogDebug(c, "relay max idle conns per host: %d", common.RelayMaxIdleConnsPerHost)
	logger.LogDebug(c, "streaming timeout seconds: %d", int64(streamingTimeout.Seconds()))
	logger.LogDebug(c, "ping interval seconds: %d", int64(pingInterval.Seconds()))

	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			stop()
			if resp.Body != nil {
				_ = resp.Body.Close()
			}

			ticker.Stop()
			if pingTicker != nil {
				pingTicker.Stop()
			}

			wg.Wait()
		})
	}
	// Ensure gin.Context is not returned to Gin's pool while any stream goroutine can still use it.
	defer cleanup()

	scanner.Split(bufio.ScanLines)
	copyCodexSSEHeaders(c, resp)
	SetEventStreamHeaders(c)

	ctx = context.WithValue(ctx, "stop_chan", stopChan)

	// Handle ping data sending with improved error handling
	if pingEnabled && pingTicker != nil {
		wg.Add(1)
		gopool.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.LogError(c, fmt.Sprintf("ping goroutine panic: %v", r))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("ping panic: %v", r))
					stop()
				}
				logger.LogDebug(c, "ping goroutine exited")
				wg.Done()
			}()

			// 添加超时保护，防止 goroutine 无限运行
			maxPingDuration := 30 * time.Minute // 最大 ping 持续时间
			pingTimeout := time.NewTimer(maxPingDuration)
			defer pingTimeout.Stop()

			for {
				select {
				case <-pingTicker.C:
					var err error
					func() {
						writeMutex.Lock()
						defer writeMutex.Unlock()
						ExtendWriteDeadline(c)
						err = PingData(c)
					}()
					if err != nil {
						logger.LogError(c, "ping data error: "+err.Error())
						info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
						return
					}
					logger.LogDebug(c, "ping data sent")
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				case <-c.Request.Context().Done():
					// 监听客户端断开连接
					return
				case <-pingTimeout.C:
					logger.LogError(c, "ping goroutine max duration reached")
					return
				}
			}
		})
	}

	dataChan := make(chan string, 10)

	wg.Add(1)
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("data handler goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("handler panic: %v", r))
			}
			stop()
			wg.Done()
		}()
		sr := newStreamResult(info.StreamStatus)
		for data := range dataChan {
			sr.reset()
			func() {
				writeMutex.Lock()
				defer writeMutex.Unlock()
				ExtendWriteDeadline(c)
				dataHandler(data, sr)
			}()
			if sr.IsStopped() {
				return
			}
		}
	})

	// Scanner goroutine with improved error handling
	wg.Add(1)
	common.RelayCtxGo(ctx, func() {
		defer func() {
			close(dataChan)
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("scanner goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", r))
			}
			stop()
			logger.LogDebug(c, "scanner goroutine exited")
			wg.Done()
		}()

		for scanner.Scan() {
			// 检查是否需要停止
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			default:
			}

			ticker.Reset(streamingTimeout)
			data := scanner.Text()
			logger.LogDebug(c, "stream scanner data: %s", data)

			if len(data) < 6 {
				continue
			}
			if data[:5] != "data:" && data[:6] != "[DONE]" {
				continue
			}
			data = data[5:]
			data = strings.TrimSpace(data)
			if data == "" {
				continue
			}
			if !strings.HasPrefix(data, "[DONE]") {
				hasContent, hasReasoning := classifyStreamPayload(data)
				if hasContent {
					info.StreamStatus.MarkContentDelta()
				}
				if hasReasoning {
					info.StreamStatus.MarkReasoningDelta()
				}
				if info.SetFirstResponseTime() {
					if info.IsStream {
						// 连接日志：首 token 到达，状态从「等待首 token」切到「正在流式输出」。
						service.MarkConnectionFirstToken(info.RequestId)
					}
					if info.IsStream && info.GetChannelID() > 0 {
						group := info.UsingGroup
						if group == "" {
							group = common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
						}
						_ = service.ObserveSpeedProtectionRequest(info.GetChannelID(), group, time.Since(info.StartTime).Milliseconds())
					}
				}
				info.ReceivedResponseCount++
				service.AddConnectionChunk(info.RequestId)

				select {
				case dataChan <- data:
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				}
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				logger.LogDebug(c, "received [DONE], stopping scanner")
				return
			}
		}

		if err := scanner.Err(); err != nil {
			if err != io.EOF {
				logger.LogError(c, "scanner error: "+err.Error())
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			}
		}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	})

	// 主循环等待完成或超时
	select {
	case <-ticker.C:
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
	case <-stopChan:
		// EndReason already set by the goroutine that triggered stopChan
	case <-c.Request.Context().Done():
		// 客户端断开：立即 cleanup 关闭上游 resp.Body，解除 scanner 阻塞并让上游停止生成，
		// 避免为已放弃的请求继续消费上游 token。
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
	}

	cleanup()

	// An upstream stream that ended normally (EOF or data: [DONE]) without any
	// content or reasoning delta is a failed request, not an empty success.
	// Decide before flushing the response rewriter: flushing a buffered
	// role/finish frame would commit a 200 response and make a retry impossible.
	if emptyErr := emptyStreamResponseError(c, info); emptyErr != nil {
		logger.LogError(c, fmt.Sprintf("stream ended: %s, received=%d, empty_response_error=%s",
			info.StreamStatus.Summary(), info.ReceivedResponseCount, emptyErr.Error()))
		return emptyErr
	}

	// Flush any text still held back by the response rewriter. Protocols whose
	// terminal event triggers a flush (Claude / Responses / Gemini) already
	// drained the buffer; this covers OpenAI chat and truncated streams. The
	// call is idempotent and skipped once the client is gone.
	if !requestContextDone(c) {
		if frames := service.FlushResponseRewrite(c); len(frames) > 0 {
			writeResponseRewriteFrames(c, frames)
		}
	}

	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, fmt.Sprintf("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, fmt.Sprintf("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}
	return nil
}

// emptyStreamResponseError returns a failure when the upstream finished with
// done/eof/timeout but produced no visible content or reasoning. If nothing has
// been written downstream yet, the caller can still return 502/504 and let the
// relay retry on another attempt. If the response is already committed, an SSE
// error event is emitted and the error is marked as non-retryable, because a
// second upstream attempt cannot replace the bytes the client already received.
func emptyStreamResponseError(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if info == nil || info.StreamStatus == nil || info.StreamStatus.IsEmptyResponse() {
		return nil
	}
	// Binary/media stream handlers do not return a retryable error and can
	// legitimately carry no text delta (raw TTS audio, image generation
	// progress). Leave their existing finalization semantics untouched.
	switch info.RelayFormat {
	case types.RelayFormatOpenAIAudio, types.RelayFormatOpenAIImage, types.RelayFormatOpenAIRealtime:
		return nil
	}
	if !isEmptyStreamEnd(info.StreamStatus) {
		return nil
	}

	statusCode := http.StatusBadGateway
	if info.StreamStatus.EndReason == relaycommon.StreamEndReasonTimeout {
		statusCode = http.StatusGatewayTimeout
	}
	err := fmt.Errorf("upstream stream ended without content or reasoning output")

	info.StreamStatus.MarkEmptyResponse()
	if c != nil && c.Writer != nil && c.Writer.Written() {
		writeEmptyStreamSSEError(c, info, statusCode, err)
		return types.NewErrorWithStatusCode(
			err,
			types.ErrorCodeEmptyResponse,
			statusCode,
			types.ErrOptionWithSkipRetry(),
		)
	}

	resetUncommittedStreamHeaders(c)
	return types.NewErrorWithStatusCode(err, types.ErrorCodeEmptyResponse, statusCode)
}

func isEmptyStreamEnd(status *relaycommon.StreamStatus) bool {
	if status == nil || status.HasErrors() {
		return false
	}
	if status.HasContentDelta() || status.HasReasoningDelta() {
		return false
	}
	switch status.EndReason {
	case relaycommon.StreamEndReasonDone, relaycommon.StreamEndReasonEOF, relaycommon.StreamEndReasonTimeout:
		return true
	default:
		return false
	}
}

func resetUncommittedStreamHeaders(c *gin.Context) {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return
	}
	for _, name := range []string{
		"Content-Type",
		"Cache-Control",
		"Connection",
		"Transfer-Encoding",
		"X-Accel-Buffering",
	} {
		c.Writer.Header().Del(name)
	}
	// SetEventStreamHeaders() uses this key to avoid setting headers twice on
	// the same gin.Context. A retry reuses the context, so clear the marker.
	delete(c.Keys, "event_stream_headers_set")
}

func writeEmptyStreamSSEError(c *gin.Context, info *relaycommon.RelayInfo, statusCode int, cause error) {
	if c == nil || c.Writer == nil {
		return
	}
	ExtendWriteDeadline(c)
	apiErr := types.NewErrorWithStatusCode(cause, types.ErrorCodeEmptyResponse, statusCode)
	openAIError := apiErr.ToOpenAIError()

	writeError := func(payload any) {
		body, err := common.Marshal(payload)
		if err != nil {
			logger.LogError(c, "failed to marshal empty stream error: "+err.Error())
			return
		}
		c.Render(-1, common.CustomEvent{Data: "data: " + string(body)})
		_ = FlushWriter(c)
	}

	switch info.RelayFormat {
	case types.RelayFormatClaude:
		c.Render(-1, common.CustomEvent{Data: "event: error\n"})
		body, err := common.Marshal(map[string]any{
			"type":  "error",
			"error": apiErr.ToClaudeError(),
		})
		if err != nil {
			logger.LogError(c, "failed to marshal claude empty stream error: "+err.Error())
			return
		}
		c.Render(-1, common.CustomEvent{Data: "data: " + string(body)})
		_ = FlushWriter(c)
	case types.RelayFormatGemini:
		writeError(map[string]any{
			"error": map[string]any{
				"code":    statusCode,
				"message": cause.Error(),
				"status":  "UNAVAILABLE",
			},
		})
	case types.RelayFormatOpenAIResponses:
		c.Render(-1, common.CustomEvent{Data: "event: error\n"})
		writeError(map[string]any{
			"type":  "error",
			"error": openAIError,
		})
	default:
		writeError(map[string]any{"error": openAIError})
	}
}
