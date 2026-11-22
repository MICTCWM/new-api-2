// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const responseRewriterContextKey = "response_rewriter"

// ResponseRewriteFrame is one already-converted client protocol payload ready to
// be written to the client. Event is the SSE event name for protocols that emit
// named events (Claude / OpenAI Responses); it is empty for the plain
// "data: ..." protocols.
type ResponseRewriteFrame struct {
	JSON  string
	Event string
}

// ResponseRewriter rewrites the client-facing model name and text fields of a
// response after it has been converted into the client protocol. It never
// touches the request sent upstream, and because it runs on the outbound copy
// only, billing and logs keep the real upstream model.
type ResponseRewriter struct {
	protocol     types.RelayFormat
	rewriteModel bool
	model        string
	rules        []operation_setting.ResponseRewriteRule
	maxFindLen   int

	content  rewriteBufferPool
	thinking rewriteBufferPool
}

type rewriteTextBuffer struct {
	pending     string
	hasTemplate bool
	template    rewriteChunkTemplate
}

// rewriteBufferPool keeps one text buffer per stream branch (choice or candidate
// index) so parallel branches never share held-back text or a flush template.
type rewriteBufferPool struct {
	byIndex map[int]*rewriteTextBuffer
	order   []int
}

func (p *rewriteBufferPool) get(index int) *rewriteTextBuffer {
	if buffer, ok := p.byIndex[index]; ok {
		return buffer
	}
	if p.byIndex == nil {
		p.byIndex = make(map[int]*rewriteTextBuffer)
	}
	buffer := &rewriteTextBuffer{}
	p.byIndex[index] = buffer
	p.order = append(p.order, index)
	return buffer
}

func (p *rewriteBufferPool) flushFrames(rules []operation_setting.ResponseRewriteRule) []ResponseRewriteFrame {
	var frames []ResponseRewriteFrame
	for _, index := range p.order {
		if frame := p.byIndex[index].flushFrame(rules); frame != nil {
			frames = append(frames, *frame)
		}
	}
	return frames
}

type rewriteChunkTemplate struct {
	json  string
	path  string
	event string
}

// InstallResponseRewriter builds the per-attempt response rewriter from the
// request context and stores it for the outbound write hooks. A fresh instance
// is installed on every channel attempt so buffered stream state never leaks
// across retries. Callers that cannot be rewritten store nil and the hooks
// become no-ops.
func InstallResponseRewriter(c *gin.Context, info *relaycommon.RelayInfo) {
	if c == nil || info == nil {
		return
	}
	c.Set(responseRewriterContextKey, buildResponseRewriter(c, info))
}

func buildResponseRewriter(c *gin.Context, info *relaycommon.RelayInfo) *ResponseRewriter {
	switch info.RelayFormat {
	case types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude,
		types.RelayFormatGemini, types.RelayFormatEmbedding:
	default:
		return nil
	}

	rewriter := &ResponseRewriter{protocol: info.RelayFormat}
	// The rewrite target is the model the downstream client asked for. Prefer the
	// frozen context value because relay code may later mutate OriginModelName
	// (e.g. Gemini appends a -nothinking suffix) purely to pick upstream routing
	// and pricing; that mutation must never leak back to the client.
	originalModel := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)
	if originalModel == "" {
		originalModel = info.OriginModelName
	}
	// A model-split hit must always hide the real split target, independent of
	// the model id rewrite switch.
	if RequestModelSplitTarget(c) != "" {
		rewriter.rewriteModel = originalModel != ""
	} else if operation_setting.ResponseRewriteModelIdAppliesToGroups(ResolveRequestCandidateGroups(c)) {
		rewriter.rewriteModel = originalModel != ""
	}
	if rewriter.rewriteModel {
		rewriter.model = originalModel
	}

	rewriter.rules = operation_setting.MatchResponseRewriteRulesForGroups(ResolveRequestCandidateGroups(c))
	for _, rule := range rewriter.rules {
		if len(rule.Find) > rewriter.maxFindLen {
			rewriter.maxFindLen = len(rule.Find)
		}
	}

	if !rewriter.rewriteModel && len(rewriter.rules) == 0 {
		return nil
	}
	return rewriter
}

func getResponseRewriter(c *gin.Context) *ResponseRewriter {
	if c == nil {
		return nil
	}
	value, ok := c.Get(responseRewriterContextKey)
	if !ok {
		return nil
	}
	rewriter, _ := value.(*ResponseRewriter)
	return rewriter
}

// RewriteStreamChunk rewrites one already-converted streaming chunk. handled
// reports whether the outbound payload is owned by this hook: when true the
// caller must write exactly the returned frames (which may include a flushed
// tail emitted before the current chunk, or be empty); when false the caller
// writes the original payload untouched.
//
// The hook also performs the outbound internal-field strip (see
// sanitizeInternalResponseFields) and the Command-channel thinking strip (see
// ShouldHideCommandThinking): it removes gateway-only keys, and reasoning_content
// / reasoning from a chat-shaped chunk, even when no response rewriter is
// installed. When both a strip and the rewriter apply, the rewriter runs on the
// stripped payload.
func RewriteStreamChunk(c *gin.Context, data string) ([]ResponseRewriteFrame, bool) {
	if data == "" || !gjson.Valid(data) {
		return nil, false
	}
	owned := false
	if sanitized, changed := sanitizeInternalResponseFields(data); changed {
		data = sanitized
		owned = true
	}
	hideThinking := ShouldHideCommandThinking(c)
	if hideThinking {
		stripped, changed := stripChatThinkingFields(data)
		if changed {
			data = stripped
		} else {
			hideThinking = false
		}
	}
	rewriter := getResponseRewriter(c)
	if rewriter == nil {
		if owned || hideThinking {
			return []ResponseRewriteFrame{{JSON: data}}, true
		}
		return nil, false
	}
	switch rewriter.protocol {
	case types.RelayFormatOpenAI:
		return rewriter.rewriteChatChunk(data), true
	case types.RelayFormatOpenAIResponses:
		return rewriter.rewriteResponsesChunk(data), true
	case types.RelayFormatClaude:
		return rewriter.rewriteClaudeChunk(data), true
	case types.RelayFormatGemini:
		return rewriter.rewriteGeminiChunk(data), true
	default:
		// The rewriter does not own this protocol, but a strip still changed the
		// payload: emit the stripped frame so the caller never writes the original
		// internal-field or reasoning-bearing bytes back to the client.
		if owned || hideThinking {
			return []ResponseRewriteFrame{{JSON: data}}, true
		}
		return nil, false
	}
}

// FlushResponseRewrite emits any text still held back by the streaming-safe
// buffer. It is idempotent: once the buffer is empty it returns no frames.
func FlushResponseRewrite(c *gin.Context) []ResponseRewriteFrame {
	rewriter := getResponseRewriter(c)
	if rewriter == nil {
		return nil
	}
	return rewriter.flushFrames()
}

// RewriteResponseBody rewrites a non-streaming response body that is already in
// the client protocol. It also strips gateway-only internal fields, and performs
// the Command-channel thinking strip (see ShouldHideCommandThinking) for a
// chat-shaped body.
func RewriteResponseBody(c *gin.Context, data []byte) []byte {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return data
	}
	if sanitized, changed := sanitizeInternalResponseBytes(data); changed {
		data = sanitized
	}
	rewriter := getResponseRewriter(c)
	if ShouldHideCommandThinking(c) {
		stripped, changed := stripChatThinkingFields(string(data))
		if changed {
			data = common.StringToByteSlice(stripped)
		} else if rewriter == nil {
			return data
		}
	}
	if rewriter == nil {
		return data
	}
	return rewriter.rewriteFullBody(data)
}

// ShouldHideCommandThinking reports whether the current outbound response must
// hide thinking content. It only applies to the Command channel and only when
// the request side saw reasoning_effort=none (which was rewritten to high
// upstream). Checking the live channel type here means a cross-channel retry to
// a non-Command channel automatically disables the hiding.
//
// It is exported so the streaming relay path can skip any upstream-side
// reasoning-to-content folding while hiding is active, leaving the reasoning
// fields intact for the outbound hook (here) to strip.
func ShouldHideCommandThinking(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if common.GetContextKeyInt(c, constant.ContextKeyChannelType) != constant.ChannelTypeCommand && common.GetContextKeyInt(c, constant.ContextKeyChannelType) != constant.ChannelTypeCommandGo {
		return false
	}
	return common.GetContextKeyBool(c, constant.ContextKeyCommandHideThinking)
}

// stripChatThinkingFields removes the thinking fields from an OpenAI chat-shaped
// JSON payload (choices[].delta.reasoning_content / delta.reasoning /
// message.reasoning_content / message.reasoning). Only the reasoning fields
// themselves are removed. A channel configured with ThinkingToContent would fold
// the reasoning into the body as <think> text upstream; the streaming relay
// path skips that folding while hiding is active (see
// relay/channel/openai.sendStreamData), so the reasoning stays in its own field
// here and this strip is sufficient. It returns the (possibly updated) payload
// plus whether a field was actually deleted; payloads without a choices array
// are returned untouched.
func stripChatThinkingFields(data string) (string, bool) {
	choices := gjson.Get(data, "choices")
	if !choices.IsArray() {
		return data, false
	}
	changed := false
	index := -1
	choices.ForEach(func(_, choice gjson.Result) bool {
		index++
		for _, path := range []string{
			fmt.Sprintf("choices.%d.delta.reasoning_content", index),
			fmt.Sprintf("choices.%d.delta.reasoning", index),
			fmt.Sprintf("choices.%d.message.reasoning_content", index),
			fmt.Sprintf("choices.%d.message.reasoning", index),
		} {
			if !gjson.Get(data, path).Exists() {
				continue
			}
			updated, err := sjson.Delete(data, path)
			if err != nil {
				continue
			}
			data = updated
			changed = true
		}
		return true
	})
	return data, changed
}

func (rw *ResponseRewriter) flushFrames() []ResponseRewriteFrame {
	frames := rw.content.flushFrames(rw.rules)
	frames = append(frames, rw.thinking.flushFrames(rw.rules)...)
	return frames
}

func (rw *ResponseRewriter) rewriteFullBody(data []byte) []byte {
	out := string(data)
	if rw.rewriteModel {
		path := "model"
		if rw.protocol == types.RelayFormatGemini {
			path = "modelVersion"
		}
		if gjson.Get(out, path).Exists() {
			if updated, err := sjson.Set(out, path, rw.model); err == nil {
				out = updated
			}
		}
	}
	if len(rw.rules) == 0 {
		return common.StringToByteSlice(out)
	}
	switch rw.protocol {
	case types.RelayFormatOpenAI:
		out = rw.rewriteChatMessage(out)
	case types.RelayFormatOpenAIResponses:
		out = rw.rewriteResponsesOutput(out)
	case types.RelayFormatClaude:
		out = rw.rewriteClaudeContent(out)
	case types.RelayFormatGemini:
		out = rw.rewriteGeminiParts(out)
	}
	return common.StringToByteSlice(out)
}

func (rw *ResponseRewriter) rewriteChatMessage(data string) string {
	choices := gjson.Get(data, "choices")
	if !choices.IsArray() {
		return data
	}
	index := -1
	choices.ForEach(func(_, choice gjson.Result) bool {
		index++
		message := choice.Get("message")
		content := message.Get("content")
		if content.IsArray() {
			data = rw.rewriteTextElements(data, fmt.Sprintf("choices.%d.message.content", index), "text")
		} else {
			data = rw.replaceStringField(data, fmt.Sprintf("choices.%d.message.content", index), content)
		}
		data = rw.replaceStringField(data, fmt.Sprintf("choices.%d.message.reasoning_content", index), message.Get("reasoning_content"))
		data = rw.replaceStringField(data, fmt.Sprintf("choices.%d.message.reasoning", index), message.Get("reasoning"))
		// Legacy /v1/completions responses carry the text directly on the choice.
		data = rw.replaceStringField(data, fmt.Sprintf("choices.%d.text", index), choice.Get("text"))
		return true
	})
	return data
}

func (rw *ResponseRewriter) rewriteResponsesOutput(data string) string {
	output := gjson.Get(data, "output")
	if !output.IsArray() {
		return data
	}
	index := -1
	output.ForEach(func(_, item gjson.Result) bool {
		index++
		data = rw.rewriteTextElements(data, fmt.Sprintf("output.%d.content", index), "text")
		data = rw.rewriteTextElements(data, fmt.Sprintf("output.%d.summary", index), "text")
		return true
	})
	return data
}

func (rw *ResponseRewriter) rewriteClaudeContent(data string) string {
	data = rw.rewriteTextElements(data, "content", "text")
	return rw.rewriteTextElements(data, "content", "thinking")
}

func (rw *ResponseRewriter) rewriteGeminiParts(data string) string {
	candidates := gjson.Get(data, "candidates")
	if !candidates.IsArray() {
		return data
	}
	index := -1
	candidates.ForEach(func(_, candidate gjson.Result) bool {
		index++
		data = rw.rewriteTextElements(data, fmt.Sprintf("candidates.%d.content.parts", index), "text")
		return true
	})
	return data
}

// rewriteTextElements applies the rules to every string value found at
// arrayPath.N[field] inside data.
func (rw *ResponseRewriter) rewriteTextElements(data string, arrayPath string, field string) string {
	array := gjson.Get(data, arrayPath)
	if !array.IsArray() {
		return data
	}
	index := -1
	array.ForEach(func(_, element gjson.Result) bool {
		index++
		data = rw.replaceStringField(data, fmt.Sprintf("%s.%d.%s", arrayPath, index, field), element.Get(field))
		return true
	})
	return data
}

func (rw *ResponseRewriter) rewriteChatChunk(data string) []ResponseRewriteFrame {
	out := rw.rewriteModelField(data, "model")
	if len(rw.rules) > 0 {
		choices := gjson.Get(out, "choices")
		if choices.IsArray() {
			index := -1
			choices.ForEach(func(_, choice gjson.Result) bool {
				index++
				content := rw.content.get(index)
				thinking := rw.thinking.get(index)
				out = rw.feedStreamField(content, out, fmt.Sprintf("choices.%d.delta.content", index), choice.Get("delta.content"))
				// Legacy /v1/completions streams carry the text directly on the choice.
				out = rw.feedStreamField(content, out, fmt.Sprintf("choices.%d.text", index), choice.Get("text"))
				if reasoning := choice.Get("delta.reasoning_content"); reasoning.Type == gjson.String {
					out = rw.feedStreamField(thinking, out, fmt.Sprintf("choices.%d.delta.reasoning_content", index), reasoning)
				} else if reasoning := choice.Get("delta.reasoning"); reasoning.Type == gjson.String {
					out = rw.feedStreamField(thinking, out, fmt.Sprintf("choices.%d.delta.reasoning", index), reasoning)
				}
				return true
			})
		}
	}
	return []ResponseRewriteFrame{{JSON: out}}
}

func (rw *ResponseRewriter) rewriteClaudeChunk(data string) []ResponseRewriteFrame {
	eventType := gjson.Get(data, "type").String()
	frames := make([]ResponseRewriteFrame, 0, 2)
	if eventType == "message_delta" || eventType == "message_stop" {
		frames = append(frames, rw.flushFrames()...)
	}
	out := rw.rewriteModelField(data, "message.model")
	if len(rw.rules) > 0 {
		content := rw.content.get(0)
		thinking := rw.thinking.get(0)
		out = rw.feedStreamField(content, out, "delta.text", gjson.Get(out, "delta.text"))
		out = rw.feedStreamField(thinking, out, "delta.thinking", gjson.Get(out, "delta.thinking"))
	}
	frames = append(frames, ResponseRewriteFrame{JSON: out, Event: eventType})
	return frames
}

func (rw *ResponseRewriter) rewriteResponsesChunk(data string) []ResponseRewriteFrame {
	eventType := gjson.Get(data, "type").String()
	frames := make([]ResponseRewriteFrame, 0, 2)
	switch eventType {
	case "response.output_text.done", "response.reasoning_summary_text.done",
		"response.completed", "response.incomplete", "response.failed":
		frames = append(frames, rw.flushFrames()...)
	}
	out := rw.rewriteModelField(data, "response.model")
	if len(rw.rules) > 0 {
		switch eventType {
		case "response.output_text.delta":
			out = rw.feedStreamField(rw.content.get(0), out, "delta", gjson.Get(out, "delta"))
		case "response.output_text.done":
			out = rw.replaceStringField(out, "text", gjson.Get(out, "text"))
		case "response.reasoning_summary_text.delta":
			out = rw.feedStreamField(rw.thinking.get(0), out, "delta", gjson.Get(out, "delta"))
		case "response.reasoning_summary_text.done":
			out = rw.replaceStringField(out, "text", gjson.Get(out, "text"))
		}
	}
	frames = append(frames, ResponseRewriteFrame{JSON: out, Event: eventType})
	return frames
}

func (rw *ResponseRewriter) rewriteGeminiChunk(data string) []ResponseRewriteFrame {
	frames := make([]ResponseRewriteFrame, 0, 2)
	if geminiCandidatesFinished(data) {
		frames = append(frames, rw.flushFrames()...)
	}
	out := rw.rewriteModelField(data, "modelVersion")
	if len(rw.rules) > 0 {
		candidates := gjson.Get(out, "candidates")
		if candidates.IsArray() {
			candidateIndex := -1
			candidates.ForEach(func(_, candidate gjson.Result) bool {
				candidateIndex++
				parts := candidate.Get("content.parts")
				if !parts.IsArray() {
					return true
				}
				content := rw.content.get(candidateIndex)
				thinking := rw.thinking.get(candidateIndex)
				partIndex := -1
				parts.ForEach(func(_, part gjson.Result) bool {
					partIndex++
					path := fmt.Sprintf("candidates.%d.content.parts.%d.text", candidateIndex, partIndex)
					if part.Get("thought").Bool() {
						out = rw.feedStreamField(thinking, out, path, part.Get("text"))
					} else {
						out = rw.feedStreamField(content, out, path, part.Get("text"))
					}
					return true
				})
				return true
			})
		}
	}
	return append(frames, ResponseRewriteFrame{JSON: out})
}

func (rw *ResponseRewriter) rewriteModelField(data string, path string) string {
	if !rw.rewriteModel || rw.model == "" || !gjson.Get(data, path).Exists() {
		return data
	}
	updated, err := sjson.Set(data, path, rw.model)
	if err != nil {
		return data
	}
	return updated
}

func (rw *ResponseRewriter) replaceStringField(data string, path string, value gjson.Result) string {
	if value.Type != gjson.String || value.String() == "" {
		return data
	}
	updated, err := sjson.Set(data, path, applyResponseRewriteRules(value.String(), rw.rules))
	if err != nil {
		return data
	}
	return updated
}

// feedStreamField buffers value and writes back only the portion that is safe to
// emit, remembering the chunk shape so a held tail can be flushed later.
func (rw *ResponseRewriter) feedStreamField(buffer *rewriteTextBuffer, data string, path string, value gjson.Result) string {
	if value.Type != gjson.String || value.String() == "" {
		return data
	}
	buffer.template = rewriteChunkTemplate{json: data, path: path, event: rw.chunkEvent(data)}
	buffer.hasTemplate = true
	emitted := buffer.feed(value.String(), rw.rules, rw.maxFindLen)
	updated, err := sjson.Set(data, path, emitted)
	if err != nil {
		return data
	}
	return updated
}

func (rw *ResponseRewriter) chunkEvent(data string) string {
	if rw.protocol == types.RelayFormatClaude || rw.protocol == types.RelayFormatOpenAIResponses {
		return gjson.Get(data, "type").String()
	}
	return ""
}

func geminiCandidatesFinished(data string) bool {
	candidates := gjson.Get(data, "candidates")
	if !candidates.IsArray() {
		return false
	}
	finished := false
	candidates.ForEach(func(_, candidate gjson.Result) bool {
		if candidate.Get("finishReason").String() != "" {
			finished = true
			return false
		}
		return true
	})
	return finished
}

// feed appends text and returns the prefix that can be emitted now. The tail is
// held back while it could still be the beginning of a find string, so a match
// spanning two chunks is still replaced.
func (b *rewriteTextBuffer) feed(text string, rules []operation_setting.ResponseRewriteRule, maxFindLen int) string {
	b.pending += text
	hold := heldTailLength(b.pending, rules, maxFindLen)
	safe := len(b.pending) - hold
	// The emitted prefix must not end inside an occurrence of a find that is
	// already fully present, otherwise the match would be cut in half and never
	// replaced. When it does, hold the whole occurrence instead. Lowering the
	// boundary can expose another straddling occurrence, so re-check until it is
	// stable.
	for safe > 0 {
		boundary := safe
		for _, rule := range rules {
			if rule.Find == "" {
				continue
			}
			for from := 0; from < boundary; {
				index := strings.Index(b.pending[from:], rule.Find)
				if index < 0 {
					break
				}
				start := from + index
				if start >= boundary {
					break
				}
				if start+len(rule.Find) > boundary {
					boundary = start
					break
				}
				from = start + 1
			}
			if boundary == 0 {
				break
			}
		}
		if boundary == safe {
			break
		}
		safe = boundary
	}
	if safe <= 0 {
		return ""
	}
	confirmed := b.pending[:safe]
	b.pending = b.pending[safe:]
	return applyResponseRewriteRules(confirmed, rules)
}

func (b *rewriteTextBuffer) flushFrame(rules []operation_setting.ResponseRewriteRule) *ResponseRewriteFrame {
	if b.pending == "" {
		return nil
	}
	text := applyResponseRewriteRules(b.pending, rules)
	b.pending = ""
	if text == "" || !b.hasTemplate {
		return nil
	}
	updated, err := sjson.Set(b.template.json, b.template.path, text)
	if err != nil {
		return nil
	}
	return &ResponseRewriteFrame{JSON: updated, Event: b.template.event}
}

// heldTailLength returns the length of the longest suffix of pending that is a
// prefix of some find string. Such a suffix may still grow into a match, so it
// must not be emitted yet. When no match is possible the held length is at most
// maxFindLen-1.
func heldTailLength(pending string, rules []operation_setting.ResponseRewriteRule, maxFindLen int) int {
	if maxFindLen <= 0 {
		return 0
	}
	limit := len(pending)
	if limit > maxFindLen {
		limit = maxFindLen
	}
	for length := limit; length >= 1; length-- {
		suffix := pending[len(pending)-length:]
		for _, rule := range rules {
			if strings.HasPrefix(rule.Find, suffix) {
				return length
			}
		}
	}
	return 0
}

func applyResponseRewriteRules(text string, rules []operation_setting.ResponseRewriteRule) string {
	for _, rule := range rules {
		if rule.Find == "" {
			continue
		}
		text = strings.ReplaceAll(text, rule.Find, rule.Replace)
	}
	return text
}
