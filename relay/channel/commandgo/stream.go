package commandgo

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const (
	defaultMaxEnvelopeLineBytes = 4 * 1024 * 1024
	emptyResponseMessage        = "Empty response from upstream (zero output tokens)"
)

type upstreamInputTokenDetails struct {
	NoCacheTokens    *int64 `json:"noCacheTokens"`
	CacheReadTokens  *int64 `json:"cacheReadTokens"`
	CacheWriteTokens *int64 `json:"cacheWriteTokens"`
}

type upstreamUsage struct {
	InputTokens       *int64                     `json:"inputTokens"`
	OutputTokens      *int64                     `json:"outputTokens"`
	CachedInputTokens *int64                     `json:"cachedInputTokens"`
	InputTokenDetails *upstreamInputTokenDetails `json:"inputTokenDetails"`
}

type upstreamEventError struct {
	Message     string `json:"message"`
	Code        any    `json:"code"`
	StatusCode  *int   `json:"statusCode"`
	IsRetryable bool   `json:"isRetryable"`
}

type upstreamEvent struct {
	Type            string              `json:"type"`
	Text            string              `json:"text"`
	Delta           string              `json:"delta"`
	ToolCallId      string              `json:"toolCallId"`
	ToolName        string              `json:"toolName"`
	Input           json.RawMessage     `json:"input"`
	FinishReason    any                 `json:"finishReason"`
	RawFinishReason any                 `json:"rawFinishReason"`
	Usage           *upstreamUsage      `json:"usage"`
	TotalUsage      *upstreamUsage      `json:"totalUsage"`
	Error           *upstreamEventError `json:"error"`
	Message         string              `json:"message"`
	Code            any                 `json:"code"`
}

// ndjsonTranslator converts Command Code NDJSON events into OpenAI chat
// completion chunks / a final chat.completion object.
type ndjsonTranslator struct {
	id      string
	model   string
	created int64

	chunkIndex    int
	toolCallIndex int

	sawFinish    bool
	finishReason string
	usage        *dto.Usage

	outputTokens int
	text         strings.Builder
	reasoning    strings.Builder

	toolCalls   []dto.ToolCallResponse
	upstreamErr *types.NewAPIError

	// wireToClient restores client tool names that were rewritten to the wire
	// protocol names on the request side. It is scoped to this request.
	wireToClient map[string]string
}

func newTranslator(c *gin.Context, model string) *ndjsonTranslator {
	translator := &ndjsonTranslator{
		id:      helper.GetResponseID(c),
		model:   model,
		created: time.Now().Unix(),
	}
	if c != nil {
		if reverse, ok := c.Get(toolNameReverseContextKey); ok {
			translator.wireToClient, _ = reverse.(map[string]string)
		}
	}
	return translator
}

// clientToolName restores the client-visible tool name for a wire name.
func (t *ndjsonTranslator) clientToolName(name string) string {
	name = strings.TrimSpace(name)
	if t == nil || len(t.wireToClient) == 0 {
		return name
	}
	if client, ok := t.wireToClient[name]; ok {
		return client
	}
	return name
}

// parseLine parses one NDJSON line and returns any user-visible chunks.
func (t *ndjsonTranslator) parseLine(line string) []*dto.ChatCompletionsStreamResponse {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed == "[DONE]" || strings.HasPrefix(trimmed, ":") {
		return nil
	}
	var event upstreamEvent
	if err := common.UnmarshalJsonStr(trimmed, &event); err != nil {
		return nil
	}
	if event.Type == "" {
		return nil
	}

	switch event.Type {
	case "text-delta":
		text := event.Text
		if text == "" {
			text = event.Delta
		}
		if text == "" {
			return nil
		}
		t.text.WriteString(text)
		t.outputTokens++
		return []*dto.ChatCompletionsStreamResponse{t.makeTextChunk(text, nil)}
	case "reasoning-delta":
		text := event.Text
		if text == "" {
			text = event.Delta
		}
		if text == "" {
			return nil
		}
		t.reasoning.WriteString(text)
		t.outputTokens++
		return []*dto.ChatCompletionsStreamResponse{t.makeTextChunk("", &text)}
	case "tool-call":
		index := t.toolCallIndex
		id := event.ToolCallId
		if id == "" {
			id = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), index)
		}
		call := dto.ToolCallResponse{
			Index: &index,
			ID:    id,
			Type:  "function",
			Function: dto.FunctionResponse{
				Name:      t.clientToolName(event.ToolName),
				Arguments: toolCallArguments(event.Input),
			},
		}
		t.toolCallIndex++
		t.outputTokens += 20
		t.toolCalls = append(t.toolCalls, call)
		return []*dto.ChatCompletionsStreamResponse{t.makeToolCallChunk(call)}
	case "finish-step":
		// finish-step only closes one agent step; it is not the terminal event.
		// Mark completion on "finish" so a clean EOF after finish-step is still
		// treated as truncation.
		if reason := mapFinishReason(effectiveFinishReason(&event)); reason != "" {
			t.finishReason = reason
		}
		if event.Usage != nil {
			t.setUsage(event.Usage.toDTO())
		}
		return nil
	case "finish":
		t.sawFinish = true
		if reason := mapFinishReason(effectiveFinishReason(&event)); reason != "" {
			t.finishReason = reason
		}
		if event.TotalUsage != nil {
			t.setUsage(event.TotalUsage.toDTO())
		} else if event.Usage != nil {
			t.setUsage(event.Usage.toDTO())
		}
		return nil
	case "error":
		t.upstreamErr = mapEventError(&event)
		return nil
	default:
		return nil
	}
}

func (t *ndjsonTranslator) makeTextChunk(text string, reasoning *string) *dto.ChatCompletionsStreamResponse {
	delta := dto.ChatCompletionsStreamResponseChoiceDelta{}
	if t.chunkIndex == 0 {
		delta.Role = "assistant"
	}
	if reasoning != nil {
		delta.ReasoningContent = reasoning
	} else {
		delta.Content = &text
	}
	t.chunkIndex++
	return t.makeChunk(delta, "", nil)
}

func (t *ndjsonTranslator) makeToolCallChunk(call dto.ToolCallResponse) *dto.ChatCompletionsStreamResponse {
	delta := dto.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []dto.ToolCallResponse{call}}
	if t.chunkIndex == 0 {
		delta.Role = "assistant"
	}
	t.chunkIndex++
	return t.makeChunk(delta, "", nil)
}

func (t *ndjsonTranslator) makeChunk(delta dto.ChatCompletionsStreamResponseChoiceDelta, finishReason string, usage *dto.Usage) *dto.ChatCompletionsStreamResponse {
	choice := dto.ChatCompletionsStreamResponseChoice{Delta: delta, Index: 0}
	if finishReason != "" {
		reason := finishReason
		choice.FinishReason = &reason
	}
	return &dto.ChatCompletionsStreamResponse{
		Id:      t.id,
		Object:  "chat.completion.chunk",
		Created: t.created,
		Model:   t.model,
		Choices: []dto.ChatCompletionsStreamResponseChoice{choice},
		Usage:   usage,
	}
}

func (t *ndjsonTranslator) makeFinishChunk(finishReason string) *dto.ChatCompletionsStreamResponse {
	return t.makeChunk(dto.ChatCompletionsStreamResponseChoiceDelta{}, finishReason, nil)
}

func (t *ndjsonTranslator) makeUsageChunk(usage *dto.Usage) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:      t.id,
		Object:  "chat.completion.chunk",
		Created: t.created,
		Model:   t.model,
		Choices: []dto.ChatCompletionsStreamResponseChoice{},
		Usage:   usage,
	}
}

func (t *ndjsonTranslator) setUsage(u *dto.Usage) {
	if u == nil {
		return
	}
	t.usage = u
	if u.CompletionTokens > 0 {
		t.outputTokens = u.CompletionTokens
	}
}

// incompleteDetail returns a human-readable reason when the upstream stream did
// not complete normally, or "" when it finished cleanly.
//
// A "finish" event is the terminal signal; an empty or "other" finish reason is
// a normal completion (the upstream simply did not classify it). Only an error
// event or an explicit network/upstream error reason marks the stream as
// truncated.
func (t *ndjsonTranslator) incompleteDetail() string {
	if !t.sawFinish {
		return "no finish event"
	}
	if t.upstreamErr != nil {
		return "provider reported an error"
	}
	if t.finishReason == "upstream_error" {
		return "provider reported an upstream connection failure"
	}
	return ""
}

func (t *ndjsonTranslator) hasVisibleOutput() bool {
	return t.text.Len() > 0 || t.reasoning.Len() > 0 || len(t.toolCalls) > 0 || t.outputTokens > 0
}

func (t *ndjsonTranslator) finalUsage(c *gin.Context, info *relaycommon.RelayInfo) *dto.Usage {
	if t.usage != nil && service.ValidUsage(t.usage) {
		return t.usage
	}
	text := t.text.String() + t.reasoning.String()
	if text == "" && len(t.toolCalls) == 0 {
		if t.usage != nil {
			return t.usage
		}
		return &dto.Usage{}
	}
	usage := service.ResponseText2Usage(c, text, info.UpstreamModelName, info.GetEstimatePromptTokens())
	if len(t.toolCalls) > 0 {
		usage.CompletionTokens += len(t.toolCalls) * 7
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}

func (t *ndjsonTranslator) recordToolCalls(info *relaycommon.RelayInfo) {
	for _, call := range t.toolCalls {
		info.CountBillableToolCall(dto.BuildInCallFunctionCall, call.Function.Name)
	}
}

func (u *upstreamUsage) toDTO() *dto.Usage {
	if u == nil {
		return nil
	}
	input := pointerInt64(u.InputTokens)
	output := pointerInt64(u.OutputTokens)
	cached := pointerInt64(u.CachedInputTokens)
	if u.CachedInputTokens == nil && u.InputTokenDetails != nil {
		cached = pointerInt64(u.InputTokenDetails.CacheReadTokens)
	}
	if output == 0 {
		// Anti false billing: an upstream that reports no output cannot have
		// billed input either.
		input = 0
		cached = 0
	}
	usage := &dto.Usage{
		PromptTokens:         int(input),
		CompletionTokens:     int(output),
		TotalTokens:          int(input + output),
		InputTokens:          int(input),
		OutputTokens:         int(output),
		PromptCacheHitTokens: int(cached),
	}
	usage.PromptTokensDetails.CachedTokens = int(cached)
	return usage
}

func pointerInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func toolCallArguments(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	if raw[0] == '"' {
		var s string
		if err := common.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	return string(raw)
}

func effectiveFinishReason(event *upstreamEvent) string {
	raw := anyToString(event.RawFinishReason)
	fin := anyToString(event.FinishReason)
	if raw == "" && fin == "other" {
		return ""
	}
	if raw == "" {
		return fin
	}
	return raw
}

func anyToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func mapFinishReason(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	if r == "" {
		return ""
	}
	switch r {
	case "tool-calls", "tool_calls", "tool_use":
		return "tool_calls"
	case "length", "max_tokens", "max_output_tokens", "model_context_window_exceeded":
		return "length"
	case "network-error", "network_error", "connection-error", "connection_error", "upstream-error", "upstream_error":
		return "upstream_error"
	default:
		return r
	}
}

func toOpenAIFinishReason(reason string) string {
	if reason == "pause_turn" {
		return "length"
	}
	return reason
}

func mapEventError(event *upstreamEvent) *types.NewAPIError {
	message := ""
	code := any(nil)
	status := 0
	if event.Error != nil {
		message = event.Error.Message
		code = event.Error.Code
		status = pointerInt(event.Error.StatusCode)
	}
	if message == "" {
		message = event.Message
	}
	if code == nil {
		code = event.Code
	}
	if status == 0 {
		status = statusFromMessage(message)
	}
	if status == 0 {
		status = http.StatusBadGateway
	}
	statusCode, errorType := mapUpstreamStatus(status)
	if strings.TrimSpace(message) == "" {
		message = "unknown upstream error"
	}
	apiErr := types.WithOpenAIError(types.OpenAIError{
		Message: message,
		Type:    errorType,
		Code:    code,
	}, statusCode)
	// Keep the upstream's raw status (e.g. 402) so pool-account failure
	// classification is not confused by downstream status mapping.
	apiErr.SourceStatusCode = status
	return apiErr
}

func pointerInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

var statusMessagePatterns = []*regexp.Regexp{
	regexp.MustCompile(`<[[:space:]]*([0-9]{3})[[:space:]]*>`),
	regexp.MustCompile(`(?i)status[^0-9]{0,16}([0-9]{3})`),
	regexp.MustCompile(`(?i)http[^0-9]{0,8}([0-9]{3})`),
	regexp.MustCompile(`(?:^|[^0-9])([0-9]{3})[[:space:]]+[A-Za-z]`),
}

// statusFromMessage extracts an HTTP status from common upstream message
// formats: "<402> ...", "status 402", "HTTP 401", "429 Too Many Requests".
// It returns 0 when no plausible status can be parsed.
func statusFromMessage(message string) int {
	message = strings.TrimSpace(message)
	if message == "" {
		return 0
	}
	for _, pattern := range statusMessagePatterns {
		match := pattern.FindStringSubmatch(message)
		if len(match) < 2 {
			continue
		}
		status, err := strconv.Atoi(match[1])
		if err == nil && status >= 100 && status <= 599 {
			return status
		}
	}
	return 0
}

func mapUpstreamStatus(status int) (int, string) {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return http.StatusBadRequest, "invalid_request_error"
	case http.StatusUnauthorized, http.StatusForbidden:
		return http.StatusUnauthorized, "authentication_error"
	case http.StatusPaymentRequired:
		return http.StatusTooManyRequests, "rate_limit_error"
	case http.StatusNotFound:
		return http.StatusNotFound, "not_found"
	case http.StatusTooManyRequests:
		return http.StatusTooManyRequests, "rate_limit_error"
	case http.StatusServiceUnavailable:
		return http.StatusServiceUnavailable, "temporarily_unavailable"
	default:
		return http.StatusBadGateway, "upstream_error"
	}
}

func upstreamIncompleteError(detail string) *types.NewAPIError {
	return types.NewErrorWithStatusCode(
		fmt.Errorf("upstream stream ended without a completion finish (%s) - response was truncated", detail),
		types.ErrorCodeBadResponse,
		http.StatusBadGateway,
	)
}

func emptyUpstreamError() *types.NewAPIError {
	return types.NewErrorWithStatusCode(
		fmt.Errorf("%s", emptyResponseMessage),
		types.ErrorCodeEmptyResponse,
		http.StatusTooManyRequests,
	)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)
	if info.IsStream {
		return a.handleStream(c, resp, info)
	}
	return a.handleNonStream(c, resp, info)
}

func (a *Adaptor) handleStream(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	info.StreamStatus = relaycommon.NewStreamStatus()
	model := responseModel(info)
	translator := newTranslator(c, model)
	started := false

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), defaultMaxEnvelopeLineBytes)
	for scanner.Scan() {
		chunks := translator.parseLine(scanner.Text())
		if translator.upstreamErr != nil {
			break
		}
		for _, chunk := range chunks {
			if !started {
				started = true
				markFirstResponse(c, info)
			}
			if err := helper.ObjectData(c, chunk); err != nil {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, err)
				return translator.finalUsage(c, info), nil
			}
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
		if !started {
			return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusBadGateway)
		}
		streamErr := types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusBadGateway)
		writeStreamError(c, streamErr)
		markStreamFailure(c, streamErr)
		return translator.finalUsage(c, info), nil
	}

	return a.finishStream(c, info, translator, started)
}

func (a *Adaptor) finishStream(c *gin.Context, info *relaycommon.RelayInfo, translator *ndjsonTranslator, started bool) (*dto.Usage, *types.NewAPIError) {
	translator.recordToolCalls(info)

	if translator.upstreamErr != nil {
		if !started {
			return nil, translator.upstreamErr
		}
		writeStreamError(c, translator.upstreamErr)
		markStreamFailure(c, translator.upstreamErr)
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
		return translator.finalUsage(c, info), nil
	}
	if detail := translator.incompleteDetail(); detail != "" {
		err := upstreamIncompleteError(detail)
		if !started {
			return nil, err
		}
		writeStreamError(c, err)
		markStreamFailure(c, err)
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
		return translator.finalUsage(c, info), nil
	}
	if !translator.hasVisibleOutput() {
		if !started {
			return nil, emptyUpstreamError()
		}
		emptyErr := emptyUpstreamError()
		writeStreamError(c, emptyErr)
		markStreamFailure(c, emptyErr)
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
		return translator.finalUsage(c, info), nil
	}

	usage := translator.finalUsage(c, info)
	if !started {
		markFirstResponse(c, info)
	}
	if translator.finishReason != "" {
		if err := helper.ObjectData(c, translator.makeFinishChunk(toOpenAIFinishReason(translator.finishReason))); err != nil {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, err)
			return usage, nil
		}
	}
	if info.ShouldIncludeUsage && service.ValidUsage(usage) {
		if err := helper.ObjectData(c, translator.makeUsageChunk(usage)); err != nil {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, err)
			return usage, nil
		}
	}
	helper.Done(c)
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	return usage, nil
}

func writeStreamError(c *gin.Context, err *types.NewAPIError) {
	openAIError := err.ToOpenAIError()
	_ = helper.ObjectData(c, map[string]any{"error": openAIError})
}

// markStreamFailure records that this attempt failed after output was already
// written downstream. The relay attributes it to the pool account but must not
// retry, since the client already received partial content.
func markStreamFailure(c *gin.Context, err *types.NewAPIError) {
	if err != nil {
		common.SetContextKey(c, constant.ContextKeyCommandGoStreamFailure, err)
	}
}

func (a *Adaptor) handleNonStream(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	model := responseModel(info)
	translator := newTranslator(c, model)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), defaultMaxEnvelopeLineBytes)
	for scanner.Scan() {
		translator.parseLine(scanner.Text())
		if translator.upstreamErr != nil {
			break
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusBadGateway)
	}
	translator.recordToolCalls(info)

	if translator.upstreamErr != nil {
		return nil, translator.upstreamErr
	}
	if detail := translator.incompleteDetail(); detail != "" {
		return nil, upstreamIncompleteError(detail)
	}
	if !translator.hasVisibleOutput() {
		return nil, emptyUpstreamError()
	}

	usage := translator.finalUsage(c, info)
	message := dto.Message{Role: "assistant"}
	if text := translator.text.String(); text != "" {
		message.SetStringContent(text)
	}
	if reasoning := translator.reasoning.String(); reasoning != "" {
		message.ReasoningContent = &reasoning
	}
	if len(translator.toolCalls) > 0 {
		raw, err := common.Marshal(translator.toolCalls)
		if err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
		}
		message.ToolCalls = raw
	}

	response := dto.OpenAITextResponse{
		Id:      translator.id,
		Object:  "chat.completion",
		Created: translator.created,
		Model:   model,
		Choices: []dto.OpenAITextResponseChoice{{
			Index:        0,
			Message:      message,
			FinishReason: toOpenAIFinishReason(translator.finishReason),
		}},
		Usage: *usage,
	}
	body, err := common.Marshal(response)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	resp.Header.Set("Content-Type", "application/json")
	service.IOCopyBytesGracefully(c, resp, body)
	return usage, nil
}

func markFirstResponse(c *gin.Context, info *relaycommon.RelayInfo) {
	if info.SetFirstResponseTime() {
		service.MarkConnectionFirstToken(info.RequestId)
	}
	info.ReceivedResponseCount++
	service.AddConnectionChunk(info.RequestId)
}

func responseModel(info *relaycommon.RelayInfo) string {
	if info == nil {
		return Model
	}
	if model := strings.TrimSpace(info.UpstreamModelName); model != "" {
		return model
	}
	return info.OriginModelName
}
