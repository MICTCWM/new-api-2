package middleware

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

func TestChannelSupportsRequestPath_OpenCodeChannel(t *testing.T) {
	// nil channel must never pass the path check.
	if channelSupportsRequestPath(nil, "/v1/responses", "deepseek-v4-flash") {
		t.Fatal("expected nil channel to be rejected")
	}

	// Opencode (type 61, not Advanced Custom type 58) must pass the path check
	// so that channel affinity can select an opencode channel.
	opencode := &model.Channel{Type: constant.ChannelTypeOpencode}
	if !channelSupportsRequestPath(opencode, "/v1/responses", "deepseek-v4-flash") {
		t.Fatal("expected opencode channel to pass the request path check")
	}

	// A regular OpenAI channel passes for any request path.
	openai := &model.Channel{Type: constant.ChannelTypeOpenAI}
	if !channelSupportsRequestPath(openai, "/v1/chat/completions", "gpt-4o") {
		t.Fatal("expected openai channel to pass the request path check")
	}
}