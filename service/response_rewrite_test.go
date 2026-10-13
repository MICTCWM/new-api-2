// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func withResponseRewriteSetting(t *testing.T, modelIdEnabled bool, modelIdGroups []string, textEnabled bool, rules []operation_setting.ResponseRewriteRule) {
	t.Helper()
	setting := operation_setting.GetResponseRewriteSetting()
	original := *setting
	t.Cleanup(func() {
		*setting = original
	})
	setting.ModelIdEnabled = modelIdEnabled
	setting.ModelIdGroups = modelIdGroups
	setting.TextEnabled = textEnabled
	setting.TextRules = rules
}

func rewriteRule(id string, groups []string, find string, replace string) operation_setting.ResponseRewriteRule {
	return operation_setting.ResponseRewriteRule{ID: id, Name: id, Groups: groups, Find: find, Replace: replace}
}

// newResponseRewriteContext builds a request context carrying an installed
// rewriter, mirroring what controller.Relay does per channel attempt. Optional
// setup callbacks run before installation so tests can pre-set the context keys
// (using group, model split target) that the rewriter decision depends on.
func newResponseRewriteContext(t *testing.T, format types.RelayFormat, originModel string, upstreamModel string, setup ...func(c *gin.Context)) *gin.Context {
	t.Helper()
	c := ginJSONContext(t, "/v1/chat/completions", "{}")
	for _, fn := range setup {
		fn(c)
	}
	InstallResponseRewriter(c, &relaycommon.RelayInfo{
		RelayFormat:     format,
		OriginModelName: originModel,
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: upstreamModel},
	})
	return c
}

// collectStreamContents replays chunks through the streaming hook and returns
// the concatenation of whatever was actually emitted (including the final
// flush), which is the text the client would reconstruct from deltas.
func collectStreamContents(t *testing.T, c *gin.Context, chunks []string) string {
	t.Helper()
	var builder strings.Builder
	for _, chunk := range chunks {
		frames, handled := RewriteStreamChunk(c, chunk)
		require.True(t, handled)
		for _, frame := range frames {
			builder.WriteString(gjson.Get(frame.JSON, "choices.0.delta.content").String())
		}
	}
	for _, frame := range FlushResponseRewrite(c) {
		builder.WriteString(gjson.Get(frame.JSON, "choices.0.delta.content").String())
	}
	return builder.String()
}

func openAIChatChunk(model string, content string) string {
	return `{"id":"c1","object":"chat.completion.chunk","model":"` + model + `","choices":[{"index":0,"delta":{"content":"` + content + `"}}]}`
}

func TestRewriteStreamChunkWithoutRewriterIsPassThrough(t *testing.T) {
	c := ginJSONContext(t, "/v1/chat/completions", "{}")
	chunk := openAIChatChunk("upstream-real", "hello")
	frames, handled := RewriteStreamChunk(c, chunk)
	require.False(t, handled)
	require.Nil(t, frames)

	// The non-streaming body is untouched as well when no rewriter is installed.
	body := []byte(openAIChatChunk("upstream-real", "hello"))
	require.Equal(t, body, RewriteResponseBody(c, body))

	require.Nil(t, FlushResponseRewrite(c))
}

func TestResponseRewriterUnsupportedFormatIsIgnored(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAIAudio, "alias", "upstream-real")

	body := []byte(`{"model":"upstream-real","text":"SECRET"}`)
	require.Equal(t, body, RewriteResponseBody(c, body))
}

func TestResponseRewriterModelIdOpenAIChatStream(t *testing.T) {
	withResponseRewriteSetting(t, true, []string{"vip"}, false, nil)
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "my-alias", "upstream-real", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "vip")
	})

	frames, handled := RewriteStreamChunk(c, openAIChatChunk("upstream-real", "hello"))
	require.True(t, handled)
	require.Len(t, frames, 1)
	require.Equal(t, "my-alias", gjson.Get(frames[0].JSON, "model").String())
	require.Equal(t, "hello", gjson.Get(frames[0].JSON, "choices.0.delta.content").String())
}

func TestResponseRewriterModelIdGroupMismatchKeepsModel(t *testing.T) {
	withResponseRewriteSetting(t, true, []string{"vip"}, false, nil)
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "my-alias", "upstream-real", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
	})

	chunk := openAIChatChunk("upstream-real", "hello")
	frames, handled := RewriteStreamChunk(c, chunk)
	// No model rewrite and no text rules means the rewriter was not installed.
	require.False(t, handled)
	require.Nil(t, frames)
}

func TestModelSplitHitHidesSplitTargetRegardlessOfSwitch(t *testing.T) {
	// Model id rewrite switch is off: a split hit must still hide the target.
	withResponseRewriteSetting(t, false, nil, false, nil)
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "user-model", "split-target", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "vip")
		common.SetContextKey(c, constant.ContextKeyModelSplitTarget, "split-target")
	})

	frames, handled := RewriteStreamChunk(c, openAIChatChunk("split-target", "hello"))
	require.True(t, handled)
	require.Len(t, frames, 1)
	require.Equal(t, "user-model", gjson.Get(frames[0].JSON, "model").String())

	// The non-streaming copy is hidden the same way.
	body := RewriteResponseBody(c, []byte(`{"model":"split-target","choices":[{"message":{"content":"hi"}}]}`))
	require.Equal(t, "user-model", gjson.GetBytes(body, "model").String())
}

func TestModelSplitHitWithoutOriginModelLeavesModelAlone(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, false, nil)
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "", "split-target", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyModelSplitTarget, "split-target")
	})

	chunk := openAIChatChunk("split-target", "hello")
	frames, handled := RewriteStreamChunk(c, chunk)
	require.False(t, handled)
	require.Nil(t, frames)
}

func TestApplyResponseRewriteRulesOrdering(t *testing.T) {
	rules := []operation_setting.ResponseRewriteRule{
		rewriteRule("r1", nil, "ab", "X"),
		rewriteRule("r2", nil, "Xc", "Y"),
	}
	require.Equal(t, "Y", applyResponseRewriteRules("abc", rules))

	// Rules are applied strictly in configuration order.
	reversed := []operation_setting.ResponseRewriteRule{rules[1], rules[0]}
	require.Equal(t, "Xc", applyResponseRewriteRules("abc", reversed))

	require.Equal(t, "abc", applyResponseRewriteRules("abc", nil))
	// A blank find is skipped instead of replacing everything.
	require.Equal(t, "abc", applyResponseRewriteRules("abc", []operation_setting.ResponseRewriteRule{rewriteRule("r", nil, "", "Z")}))
}

func TestRewriteTextBufferMatchesAcrossChunks(t *testing.T) {
	withResponseRewriteSetting(t, false, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	chunks := []string{
		openAIChatChunk("upstream-real", "SE"),
		openAIChatChunk("upstream-real", "CR"),
		openAIChatChunk("upstream-real", "ET done"),
	}

	// A prefix of a find string must never leak to the client, so the first two
	// chunks emit nothing at all.
	frames, handled := RewriteStreamChunk(c, chunks[0])
	require.True(t, handled)
	require.Equal(t, "", gjson.Get(frames[0].JSON, "choices.0.delta.content").String())
	frames, _ = RewriteStreamChunk(c, chunks[1])
	require.Equal(t, "", gjson.Get(frames[0].JSON, "choices.0.delta.content").String())

	// The reconstructed stream equals the replaced text exactly: no loss, no
	// duplication, no leftover fragment.
	require.Equal(t, "[x] done", collectStreamContents(t, c, chunks[2:]))
}

func TestRewriteTextBufferHoldsTailUntilFlush(t *testing.T) {
	withResponseRewriteSetting(t, false, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	// The whole chunk is a potential match prefix, so it is held back.
	frames, handled := RewriteStreamChunk(c, openAIChatChunk("upstream-real", "SECRET"))
	require.True(t, handled)
	require.Equal(t, "", gjson.Get(frames[0].JSON, "choices.0.delta.content").String())

	flushed := FlushResponseRewrite(c)
	require.Len(t, flushed, 1)
	require.Equal(t, "[x]", gjson.Get(flushed[0].JSON, "choices.0.delta.content").String())

	// Flushing is idempotent.
	require.Nil(t, FlushResponseRewrite(c))
}

func TestRewriteTextBufferWithoutMatchPassesTextThrough(t *testing.T) {
	withResponseRewriteSetting(t, false, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	chunk := openAIChatChunk("upstream-real", "hello world")
	frames, handled := RewriteStreamChunk(c, chunk)
	require.True(t, handled)
	require.Len(t, frames, 1)
	// No match, so the emitted payload carries the input content unchanged.
	require.JSONEq(t, chunk, frames[0].JSON)
	require.Nil(t, FlushResponseRewrite(c))
}

func TestResponseRewriterOpenAIChatNonStream(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	body := RewriteResponseBody(c, []byte(`{"model":"upstream-real","choices":[{"index":0,"message":{"content":"SECRET","reasoning_content":"think SECRET","reasoning":"alt SECRET"}}]}`))
	require.Equal(t, "alias", gjson.GetBytes(body, "model").String())
	require.Equal(t, "[x]", gjson.GetBytes(body, "choices.0.message.content").String())
	require.Equal(t, "think [x]", gjson.GetBytes(body, "choices.0.message.reasoning_content").String())
	require.Equal(t, "alt [x]", gjson.GetBytes(body, "choices.0.message.reasoning").String())

	// Array-shaped content parts are rewritten too.
	arrayBody := RewriteResponseBody(c, []byte(`{"model":"upstream-real","choices":[{"index":0,"message":{"content":[{"type":"text","text":"SECRET"}]}}]}`))
	require.Equal(t, "[x]", gjson.GetBytes(arrayBody, "choices.0.message.content.0.text").String())
}

func TestResponseRewriterOpenAICompletionsLegacyFields(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	body := RewriteResponseBody(c, []byte(`{"model":"upstream-real","choices":[{"index":0,"text":"SECRET"}]}`))
	require.Equal(t, "alias", gjson.GetBytes(body, "model").String())
	require.Equal(t, "[x]", gjson.GetBytes(body, "choices.0.text").String())

	frames, handled := RewriteStreamChunk(c, `{"id":"c1","model":"upstream-real","choices":[{"index":0,"text":"SECRET"}]}`)
	require.True(t, handled)
	require.Equal(t, "alias", gjson.Get(frames[0].JSON, "model").String())
	require.Equal(t, "", gjson.Get(frames[0].JSON, "choices.0.text").String())

	flushed := FlushResponseRewrite(c)
	require.Len(t, flushed, 1)
	require.Equal(t, "[x]", gjson.Get(flushed[0].JSON, "choices.0.text").String())
}

func TestResponseRewriterClaudeNonStreamTextAndThinking(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatClaude, "alias", "upstream-real")

	body := RewriteResponseBody(c, []byte(`{"model":"upstream-real","content":[{"type":"text","text":"SECRET"},{"type":"thinking","thinking":"SECRET plan"}]}`))
	require.Equal(t, "alias", gjson.GetBytes(body, "model").String())
	require.Equal(t, "[x]", gjson.GetBytes(body, "content.0.text").String())
	require.Equal(t, "[x] plan", gjson.GetBytes(body, "content.1.thinking").String())
}

func TestResponseRewriterResponsesNonStream(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAIResponses, "alias", "upstream-real")

	body := RewriteResponseBody(c, []byte(`{"model":"upstream-real","output":[{"type":"message","content":[{"type":"output_text","text":"SECRET"}],"summary":[{"type":"summary_text","text":"SECRET"}]}]}`))
	require.Equal(t, "alias", gjson.GetBytes(body, "model").String())
	require.Equal(t, "[x]", gjson.GetBytes(body, "output.0.content.0.text").String())
	require.Equal(t, "[x]", gjson.GetBytes(body, "output.0.summary.0.text").String())
}

func TestResponseRewriterGeminiNonStream(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatGemini, "alias", "upstream-real")

	body := RewriteResponseBody(c, []byte(`{"modelVersion":"upstream-real","candidates":[{"content":{"parts":[{"text":"SECRET"}]}}]}`))
	require.Equal(t, "alias", gjson.GetBytes(body, "modelVersion").String())
	require.Equal(t, "[x]", gjson.GetBytes(body, "candidates.0.content.parts.0.text").String())
}

func TestResponseRewriterClaudeStreamFlushBeforeTerminalEvent(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatClaude, "alias", "upstream-real")

	startFrames, handled := RewriteStreamChunk(c, `{"type":"message_start","message":{"model":"upstream-real"}}`)
	require.True(t, handled)
	require.Equal(t, "alias", gjson.Get(startFrames[0].JSON, "message.model").String())
	require.Equal(t, "message_start", startFrames[0].Event)

	deltaFrames, _ := RewriteStreamChunk(c, `{"type":"content_block_delta","delta":{"type":"text_delta","text":"SECRET"}}`)
	require.Equal(t, "", gjson.Get(deltaFrames[0].JSON, "delta.text").String())
	require.Equal(t, "content_block_delta", deltaFrames[0].Event)

	// The held text is flushed as a cloned content_block_delta before the
	// terminal event, so a client that stops at message_delta still sees it.
	terminalFrames, _ := RewriteStreamChunk(c, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	require.Len(t, terminalFrames, 2)
	require.Equal(t, "content_block_delta", terminalFrames[0].Event)
	require.Equal(t, "[x]", gjson.Get(terminalFrames[0].JSON, "delta.text").String())
	require.Equal(t, "message_delta", terminalFrames[1].Event)
	require.Equal(t, "message_delta", gjson.Get(terminalFrames[1].JSON, "type").String())
}

func TestResponseRewriterResponsesStreamFlushBeforeTerminalEvent(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAIResponses, "alias", "upstream-real")

	deltaFrames, handled := RewriteStreamChunk(c, `{"type":"response.output_text.delta","delta":"SECRET"}`)
	require.True(t, handled)
	require.Equal(t, "", gjson.Get(deltaFrames[0].JSON, "delta").String())

	doneFrames, _ := RewriteStreamChunk(c, `{"type":"response.output_text.done","text":"SECRET"}`)
	require.Len(t, doneFrames, 2)
	require.Equal(t, "response.output_text.delta", doneFrames[0].Event)
	require.Equal(t, "[x]", gjson.Get(doneFrames[0].JSON, "delta").String())
	require.Equal(t, "response.output_text.done", doneFrames[1].Event)
	require.Equal(t, "[x]", gjson.Get(doneFrames[1].JSON, "text").String())
}

func TestResponseRewriterGeminiStreamFlushBeforeFinish(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatGemini, "alias", "upstream-real")

	deltaFrames, handled := RewriteStreamChunk(c, `{"modelVersion":"upstream-real","candidates":[{"content":{"parts":[{"text":"SECRET"}]}}]}`)
	require.True(t, handled)
	require.Equal(t, "", gjson.Get(deltaFrames[0].JSON, "candidates.0.content.parts.0.text").String())
	require.Equal(t, "alias", gjson.Get(deltaFrames[0].JSON, "modelVersion").String())

	finishFrames, _ := RewriteStreamChunk(c, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":""}]}}]}`)
	require.Len(t, finishFrames, 2)
	require.Equal(t, "[x]", gjson.Get(finishFrames[0].JSON, "candidates.0.content.parts.0.text").String())
	require.Equal(t, "STOP", gjson.Get(finishFrames[1].JSON, "candidates.0.finishReason").String())
}

func TestRewriteStreamChunkIgnoresNonJSONPayload(t *testing.T) {
	withResponseRewriteSetting(t, true, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	frames, handled := RewriteStreamChunk(c, "[DONE]")
	require.False(t, handled)
	require.Nil(t, frames)
}

func TestRewriteTextBufferUTF8ContentAcrossChunks(t *testing.T) {
	withResponseRewriteSetting(t, false, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	// Multibyte content (Chinese + emoji) around an ASCII find split across the
	// chunk boundary: the reconstructed text must equal the replaced text.
	contents := collectStreamContents(t, c, []string{
		openAIChatChunk("upstream-real", "中文😀SE"),
		openAIChatChunk("upstream-real", "CRET结束"),
	})
	require.Equal(t, "中文😀[x]结束", contents)
}

func TestRewriteTextBufferMultibyteFindAcrossChunks(t *testing.T) {
	withResponseRewriteSetting(t, false, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "秘密", "公开")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	// The find itself is multibyte and split across the boundary. No partial
	// rune of it may leak to the client.
	contents := collectStreamContents(t, c, []string{
		openAIChatChunk("upstream-real", "这是一段秘"),
		openAIChatChunk("upstream-real", "密内容"),
	})
	require.Equal(t, "这是一段公开内容", contents)
}

func TestRewriteTextBufferOverlappingRulesAcrossChunks(t *testing.T) {
	withResponseRewriteSetting(t, false, nil, true, []operation_setting.ResponseRewriteRule{
		rewriteRule("r1", nil, "SECRET", "[x]"),
		rewriteRule("r2", nil, "RETKEY", "[y]"),
	})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	// Two rules whose finds overlap: the tail has to be held for the longest
	// find (6 bytes) even though it arrives one chunk at a time.
	contents := collectStreamContents(t, c, []string{
		openAIChatChunk("upstream-real", "SEC"),
		openAIChatChunk("upstream-real", "RET"),
		openAIChatChunk("upstream-real", "KEY"),
	})
	require.Equal(t, "[x]KEY", contents)
}

func TestResponseRewriterOpenAIMultiChoiceStreamBuckets(t *testing.T) {
	withResponseRewriteSetting(t, false, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real")

	first, handled := RewriteStreamChunk(c, `{"object":"chat.completion.chunk","model":"upstream-real","choices":[{"index":0,"delta":{"content":"SE"}},{"index":1,"delta":{"content":"CR"}}]}`)
	require.True(t, handled)
	require.Len(t, first, 1)
	require.Equal(t, "", gjson.Get(first[0].JSON, "choices.0.delta.content").String())
	require.Equal(t, "CR", gjson.Get(first[0].JSON, "choices.1.delta.content").String())

	second, _ := RewriteStreamChunk(c, `{"object":"chat.completion.chunk","model":"upstream-real","choices":[{"index":0,"delta":{"content":"CRET end"}},{"index":1,"delta":{"content":"ET tail"}}]}`)
	require.Len(t, second, 1)
	require.Equal(t, "[x] end", gjson.Get(second[0].JSON, "choices.0.delta.content").String())
	require.Equal(t, "ET tail", gjson.Get(second[0].JSON, "choices.1.delta.content").String())

	// Both branches drained independently, so nothing is held back at the end.
	require.Nil(t, FlushResponseRewrite(c))
}

func TestResponseRewriterGeminiMultiCandidateStreamBuckets(t *testing.T) {
	withResponseRewriteSetting(t, false, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatGemini, "alias", "upstream-real")

	first, handled := RewriteStreamChunk(c, `{"modelVersion":"upstream-real","candidates":[{"content":{"parts":[{"text":"SE"}]}},{"content":{"parts":[{"text":"CR"}]}}]}`)
	require.True(t, handled)
	require.Len(t, first, 1)
	require.Equal(t, "", gjson.Get(first[0].JSON, "candidates.0.content.parts.0.text").String())
	require.Equal(t, "CR", gjson.Get(first[0].JSON, "candidates.1.content.parts.0.text").String())

	second, _ := RewriteStreamChunk(c, `{"modelVersion":"upstream-real","candidates":[{"content":{"parts":[{"text":"CRET end"}]}},{"content":{"parts":[{"text":"ET tail"}]}}]}`)
	require.Len(t, second, 1)
	require.Equal(t, "[x] end", gjson.Get(second[0].JSON, "candidates.0.content.parts.0.text").String())
	require.Equal(t, "ET tail", gjson.Get(second[0].JSON, "candidates.1.content.parts.0.text").String())

	require.Nil(t, FlushResponseRewrite(c))
}

func TestResponseRewriterPrefersClientRequestedModel(t *testing.T) {
	withResponseRewriteSetting(t, true, []string{"vip"}, false, nil)
	// The relay may append a -nothinking suffix to OriginModelName for upstream
	// routing and pricing; the client must still see the model it requested.
	c := newResponseRewriteContext(t, types.RelayFormatGemini, "gemini-2.5-flash-nothinking", "gemini-2.5-flash-nothinking", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "vip")
		common.SetContextKey(c, constant.ContextKeyOriginalModel, "gemini-2.5-flash")
	})

	frames, handled := RewriteStreamChunk(c, `{"modelVersion":"gemini-2.5-flash-nothinking"}`)
	require.True(t, handled)
	require.Equal(t, "gemini-2.5-flash", gjson.Get(frames[0].JSON, "modelVersion").String())

	body := RewriteResponseBody(c, []byte(`{"modelVersion":"gemini-2.5-flash-nothinking"}`))
	require.Equal(t, "gemini-2.5-flash", gjson.GetBytes(body, "modelVersion").String())
}

// commandHideThinkingContext builds a bare context (no rewriter installed) that
// carries the channel type and, when hide is true, the Command hide-thinking
// marker set by the request-side rewrite.
func commandHideThinkingContext(t *testing.T, channelType int, hide bool) *gin.Context {
	t.Helper()
	c := ginJSONContext(t, "/v1/chat/completions", "{}")
	common.SetContextKey(c, constant.ContextKeyChannelType, channelType)
	if hide {
		common.SetContextKey(c, constant.ContextKeyCommandHideThinking, true)
	}
	return c
}

func TestCommandHideThinkingStripsStreamReasoningFields(t *testing.T) {
	c := commandHideThinkingContext(t, constant.ChannelTypeCommand, true)
	chunk := `{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"hi","reasoning_content":"secret","reasoning":"alt"}}]}`
	frames, handled := RewriteStreamChunk(c, chunk)
	require.True(t, handled)
	require.Len(t, frames, 1)
	require.False(t, gjson.Get(frames[0].JSON, "choices.0.delta.reasoning_content").Exists())
	require.False(t, gjson.Get(frames[0].JSON, "choices.0.delta.reasoning").Exists())
	require.Equal(t, "hi", gjson.Get(frames[0].JSON, "choices.0.delta.content").String())
}

func TestCommandHideThinkingStreamWithoutContentKeepsFrame(t *testing.T) {
	c := commandHideThinkingContext(t, constant.ChannelTypeCommand, true)
	frames, handled := RewriteStreamChunk(c, `{"choices":[{"index":0,"delta":{"reasoning_content":"secret"}}]}`)
	require.True(t, handled)
	require.Len(t, frames, 1)
	require.False(t, gjson.Get(frames[0].JSON, "choices.0.delta.reasoning_content").Exists())
	// The delta object may be left empty; it must still be present so clients do
	// not choke on a missing delta.
	require.True(t, gjson.Get(frames[0].JSON, "choices.0.delta").IsObject())
}

func TestCommandHideThinkingStreamWithoutThinkingPassesThrough(t *testing.T) {
	c := commandHideThinkingContext(t, constant.ChannelTypeCommand, true)
	frames, handled := RewriteStreamChunk(c, openAIChatChunk("m", "hi"))
	require.False(t, handled)
	require.Nil(t, frames)
}

func TestCommandHideThinkingStripsNonStreamBody(t *testing.T) {
	c := commandHideThinkingContext(t, constant.ChannelTypeCommand, true)
	body := RewriteResponseBody(c, []byte(`{"choices":[{"message":{"content":"hi","reasoning_content":"secret","reasoning":"alt"}}]}`))
	require.Equal(t, "hi", gjson.GetBytes(body, "choices.0.message.content").String())
	require.False(t, gjson.GetBytes(body, "choices.0.message.reasoning_content").Exists())
	require.False(t, gjson.GetBytes(body, "choices.0.message.reasoning").Exists())
}

func TestCommandHideThinkingWithoutMarkerPassesThrough(t *testing.T) {
	c := commandHideThinkingContext(t, constant.ChannelTypeCommand, false)
	chunk := `{"choices":[{"index":0,"delta":{"content":"hi","reasoning_content":"secret"}}]}`
	frames, handled := RewriteStreamChunk(c, chunk)
	require.False(t, handled)
	require.Nil(t, frames)

	body := []byte(`{"choices":[{"message":{"content":"hi","reasoning_content":"secret"}}]}`)
	require.Equal(t, body, RewriteResponseBody(c, body))
}

func TestCommandHideThinkingMarkerOnOtherChannelPassesThrough(t *testing.T) {
	c := commandHideThinkingContext(t, constant.ChannelTypeOpenAI, true)
	chunk := `{"choices":[{"index":0,"delta":{"content":"hi","reasoning_content":"secret"}}]}`
	frames, handled := RewriteStreamChunk(c, chunk)
	require.False(t, handled)
	require.Nil(t, frames)

	body := []byte(`{"choices":[{"message":{"content":"hi","reasoning_content":"secret"}}]}`)
	require.Equal(t, body, RewriteResponseBody(c, body))
}

func TestCommandHideThinkingCombinesWithResponseRewriter(t *testing.T) {
	withResponseRewriteSetting(t, false, nil, true, []operation_setting.ResponseRewriteRule{rewriteRule("r1", nil, "SECRET", "[x]")})
	c := newResponseRewriteContext(t, types.RelayFormatOpenAI, "alias", "upstream-real", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeCommand)
		common.SetContextKey(c, constant.ContextKeyCommandHideThinking, true)
	})

	chunk := `{"model":"upstream-real","choices":[{"index":0,"delta":{"content":"SECRET","reasoning_content":"SECRET plan"}}]}`
	frames, handled := RewriteStreamChunk(c, chunk)
	require.True(t, handled)
	require.Len(t, frames, 1)
	// The thinking field is dropped before the rewriter runs.
	require.False(t, gjson.Get(frames[0].JSON, "choices.0.delta.reasoning_content").Exists())
	// The content is held back by the rewrite rules and flushed on terminal.
	require.Equal(t, "", gjson.Get(frames[0].JSON, "choices.0.delta.content").String())
	flushed := FlushResponseRewrite(c)
	require.Len(t, flushed, 1)
	require.Equal(t, "[x]", gjson.Get(flushed[0].JSON, "choices.0.delta.content").String())
}

// When hiding is active but the installed rewriter does not own the chunk
// protocol (e.g. Embedding), the stripped payload must still be emitted instead
// of falling back to the untouched original, which would leak the reasoning.
func TestCommandHideThinkingEmitsStrippedFrameForUnownedProtocol(t *testing.T) {
	withResponseRewriteSetting(t, true, []string{"vip"}, false, nil)
	c := newResponseRewriteContext(t, types.RelayFormatEmbedding, "alias", "upstream-real", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "vip")
		common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeCommand)
		common.SetContextKey(c, constant.ContextKeyCommandHideThinking, true)
	})

	chunk := `{"model":"upstream-real","choices":[{"index":0,"delta":{"content":"hi","reasoning_content":"secret"}}]}`
	frames, handled := RewriteStreamChunk(c, chunk)
	require.True(t, handled)
	require.Len(t, frames, 1)
	require.False(t, gjson.Get(frames[0].JSON, "choices.0.delta.reasoning_content").Exists())
	require.Equal(t, "hi", gjson.Get(frames[0].JSON, "choices.0.delta.content").String())
}

// Without the hide marker the unowned-protocol path stays a no-op, so a plain
// Embedding rewriter never claims ownership of a chunk it cannot rewrite.
func TestEmbeddingStreamChunkWithoutHideIsPassThrough(t *testing.T) {
	withResponseRewriteSetting(t, true, []string{"vip"}, false, nil)
	c := newResponseRewriteContext(t, types.RelayFormatEmbedding, "alias", "upstream-real", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "vip")
	})

	chunk := `{"model":"upstream-real","choices":[{"index":0,"delta":{"reasoning_content":"secret"}}]}`
	frames, handled := RewriteStreamChunk(c, chunk)
	require.False(t, handled)
	require.Nil(t, frames)
}
