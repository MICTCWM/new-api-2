package service

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsOpenAIChatUpstream(t *testing.T) {
	tests := []struct {
		apiType int
		name    string
		want    bool
	}{
		{constant.APITypeOpenAI, "openai", true},
		{constant.APITypeDeepSeek, "deepseek", true},
		{constant.APITypeSiliconFlow, "siliconflow", true},
		{constant.APITypeMiniMax, "minimax", true},
		{constant.APITypeAnthropic, "anthropic", false},
		{constant.APITypeGemini, "gemini", false},
		{constant.APITypeAws, "aws", false},
		{constant.APITypeCodex, "codex", false},
		{constant.APITypeAdvancedCustom, "advanced_custom", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isOpenAIChatUpstream(tt.apiType))
		})
	}
}

func TestShouldResponsesUseChatPolicy(t *testing.T) {
	tests := []struct {
		name      string
		policy    model_setting.ResponsesToChatPolicy
		channelID int
		model     string
		want      bool
	}{
		{
			name: "disabled",
			policy: model_setting.ResponsesToChatPolicy{
				Enabled:     false,
				AllChannels: true,
			},
			model: "gpt-4o",
			want:  false,
		},
		{
			name: "enabled all channels matches every model",
			policy: model_setting.ResponsesToChatPolicy{
				Enabled:     true,
				AllChannels: true,
			},
			model: "gpt-4o",
			want:  true,
		},
		{
			name: "enabled pattern matches",
			policy: model_setting.ResponsesToChatPolicy{
				Enabled:       true,
				AllChannels:   true,
				ModelPatterns: []string{"^gpt-"},
			},
			model: "gpt-4o",
			want:  true,
		},
		{
			name: "enabled pattern no match",
			policy: model_setting.ResponsesToChatPolicy{
				Enabled:       true,
				AllChannels:   true,
				ModelPatterns: []string{"^gpt-"},
			},
			model: "claude-3",
			want:  false,
		},
		{
			name: "enabled specific channel id not matched",
			policy: model_setting.ResponsesToChatPolicy{
				Enabled:    true,
				ChannelIDs: []int{7},
			},
			channelID: 1,
			model:     "gpt-4o",
			want:      false,
		},
		{
			name: "enabled specific channel id matched",
			policy: model_setting.ResponsesToChatPolicy{
				Enabled:    true,
				ChannelIDs: []int{7},
			},
			channelID: 7,
			model:     "gpt-4o",
			want:      true,
		},
		{
			name: "enabled empty patterns treats every model as matching",
			policy: model_setting.ResponsesToChatPolicy{
				Enabled:     true,
				AllChannels: true,
			},
			model: "anything",
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ShouldResponsesUseChatPolicy(tt.policy, tt.channelID, 0, tt.model))
		})
	}
}

func TestShouldResponsesUseChatGlobalDefaultIsEnabledForChatUpstreams(t *testing.T) {
	// The default global policy is enabled for all channels, so an
	// OpenAI-compatible chat upstream should convert by default while
	// non-chat upstreams are excluded.
	require.True(t, ShouldResponsesUseChatGlobal(0, constant.ChannelTypeOpenAI, constant.APITypeOpenAI, "gpt-4o"))
	require.False(t, ShouldResponsesUseChatGlobal(0, constant.ChannelTypeCodex, constant.APITypeCodex, "gpt-4o"))
	require.False(t, ShouldResponsesUseChatGlobal(0, constant.ChannelTypeAnthropic, constant.APITypeAnthropic, "gpt-4o"))
	require.False(t, ShouldResponsesUseChatGlobal(0, constant.ChannelTypeGemini, constant.APITypeGemini, "gpt-4o"))
}
