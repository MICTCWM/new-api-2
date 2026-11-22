package service

import (
	"regexp"
	"sync"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/model_setting"
)

// Chat→Responses upgrade policy is host routing logic (it decides *whether*
// to convert, reading host settings), so it lives here, not in relayconvert.

var chatResponsesRegexCache sync.Map // map[string]*regexp.Regexp

func matchAnyModelPattern(patterns []string, model string) bool {
	if len(patterns) == 0 || model == "" {
		return false
	}
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		re, ok := chatResponsesRegexCache.Load(pattern)
		if !ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				// Treat invalid patterns as non-matching to avoid breaking runtime traffic.
				continue
			}
			re = compiled
			chatResponsesRegexCache.Store(pattern, re)
		}
		if re.(*regexp.Regexp).MatchString(model) {
			return true
		}
	}
	return false
}

func ShouldChatCompletionsUseResponsesPolicy(policy model_setting.ChatCompletionsToResponsesPolicy, channelID int, channelType int, model string) bool {
	if !policy.IsChannelEnabled(channelID, channelType) {
		return false
	}
	return matchAnyModelPattern(policy.ModelPatterns, model)
}

func ShouldChatCompletionsUseResponsesGlobal(channelID int, channelType int, model string) bool {
	// Command Go speaks a single NDJSON agent envelope upstream and cannot be
	// adapted from an OpenAI Responses call, so it must never be upgraded from
	// Chat Completions to Responses.
	if channelType == constant.ChannelTypeCommandGo {
		return false
	}
	return ShouldChatCompletionsUseResponsesPolicy(
		model_setting.GetGlobalSettings().ChatCompletionsToResponsesPolicy,
		channelID,
		channelType,
		model,
	)
}

func ShouldResponsesUseChatPolicy(policy model_setting.ResponsesToChatPolicy, channelID int, channelType int, model string) bool {
	if !policy.IsChannelEnabled(channelID, channelType) {
		return false
	}
	// Empty model patterns means the policy applies to all models. This is what
	// makes the "all upstreams speak chat by default" behavior work out of the
	// box; patterns are only used to narrow the policy down.
	if len(policy.ModelPatterns) == 0 {
		return true
	}
	return matchAnyModelPattern(policy.ModelPatterns, model)
}

// isOpenAIChatUpstream reports whether the channel's upstream speaks the OpenAI
// Chat Completions protocol (or an OpenAI-compatible chat dialect), which is the
// only place where a Responses request can be faithfully downgraded to chat.
// Channels with a distinct native protocol (Anthropic, Gemini, Bedrock, Vertex,
// Cohere, Dify, ...) or a responses-only upstream (Codex) are excluded; they
// keep their own native Responses conversion paths.
func isOpenAIChatUpstream(apiType int) bool {
	switch apiType {
	case constant.APITypeOpenAI,
		constant.APITypeBaidu,
		constant.APITypeZhipu,
		constant.APITypeAli,
		constant.APITypeXunfei,
		constant.APITypeTencent,
		constant.APITypeZhipuV4,
		constant.APITypeOllama,
		constant.APITypePerplexity,
		constant.APITypeJina,
		constant.APITypeCloudflare,
		constant.APITypeSiliconFlow,
		constant.APITypeMistral,
		constant.APITypeDeepSeek,
		constant.APITypeMokaAI,
		constant.APITypeVolcEngine,
		constant.APITypeBaiduV2,
		constant.APITypeOpenRouter,
		constant.APITypeXinference,
		constant.APITypeOpencode,
		constant.APITypeCommand,
		constant.APITypeCommandGo,
		constant.APITypeXai,
		constant.APITypeCoze,
		constant.APITypeJimeng,
		constant.APITypeMoonshot,
		constant.APITypeMiniMax:
		return true
	default:
		return false
	}
}

// ShouldResponsesUseChatGlobal decides whether a downstream OpenAI Responses
// request should be converted to a Chat Completions request before relaying to
// the upstream. It reads the global Responses→Chat policy (which defaults to
// enabled for all channels). Channels whose upstream only speaks the Responses
// API (e.g. the Codex subscription channel) or does not speak OpenAI chat are
// automatically excluded so they keep their native conversion behavior.
func ShouldResponsesUseChatGlobal(channelID int, channelType int, apiType int, model string) bool {
	if !isOpenAIChatUpstream(apiType) {
		return false
	}
	return ShouldResponsesUseChatPolicy(
		model_setting.GetGlobalSettings().ResponsesToChatPolicy,
		channelID,
		channelType,
		model,
	)
}
