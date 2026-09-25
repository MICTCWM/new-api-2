package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 失败渠道的会话绑定必须被作废：下一次在新渠道解析会话时要新建 session，
// 而不能复用失败渠道的旧 session，否则会与新渠道的上游会话身份冲突。
func TestInvalidateOpencodeSessionBinding_ForcesNewSessionOnNextChannel(t *testing.T) {
	require.NoError(t, resetOpencodeSessionStateForTest())

	const channelA = 62001
	const channelB = 62002
	body := `{"prompt_cache_key":"conv-invalidate-62001"}`
	c := newOpencodeSessionTestContext(t, body, "", "")
	infoA := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelA}}
	sessionA := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, infoA))

	key := opencodeSessionKey(resolveOpencodeConversationKey(c, infoA, infoA.GetUpstreamModelName()))
	InvalidateOpencodeSessionBinding(c, infoA)

	_, found, err := getOpencodeSessionBinding(key)
	require.NoError(t, err)
	assert.False(t, found, "failed channel's session binding must be invalidated")

	active, err := isOpencodeActiveSession(channelA, key, time.Now().Unix())
	require.NoError(t, err)
	assert.False(t, active, "failed channel's active session slot must be released")

	infoB := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelB}}
	sessionB := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, infoB))
	assert.NotEqual(t, sessionA, sessionB, "a new session id must be created for the next channel")

	binding, found, err := getOpencodeSessionBinding(key)
	require.NoError(t, err)
	require.True(t, found, "the new channel must own the rebuilt binding")
	assert.Equal(t, channelB, binding.ChannelID)
	assert.Equal(t, sessionB, binding.Session)
	assert.Equal(t, sessionB, ResolveOpencodeSession(c, infoB), "the rebuilt binding must be reused on the new channel")
}

func TestInvalidateOpencodeSessionBinding_IsSafeWithoutBinding(t *testing.T) {
	require.NoError(t, resetOpencodeSessionStateForTest())

	c := newOpencodeSessionTestContext(t, `{"prompt_cache_key":"conv-invalidate-idempotent"}`, "", "")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 62003}}

	InvalidateOpencodeSessionBinding(c, info)
	_, found, err := getOpencodeSessionBinding(opencodeSessionKey(resolveOpencodeConversationKey(c, info, info.GetUpstreamModelName())))
	require.NoError(t, err)
	assert.False(t, found)

	// info 为空时也必须安全返回，不得 panic。
	InvalidateOpencodeSessionBinding(c, nil)
	InvalidateOpencodeSessionBinding(nil, info)
}

// 存在 RequestModelSplitTarget 时 move 会复用旧 session，但作废后缓存已无绑定，
// 下一渠道必须新建会话，不能被分流复用逻辑带回失败渠道的旧 session。
func TestInvalidateOpencodeSessionBinding_SplitTargetStillMintsNewSession(t *testing.T) {
	require.NoError(t, resetOpencodeSessionStateForTest())

	const channelA = 62011
	const channelB = 62012
	body := `{"prompt_cache_key":"conv-invalidate-split-62011"}`
	c := newOpencodeSessionTestContext(t, body, "", "")
	common.SetContextKey(c, constant.ContextKeyModelSplitTarget, "deepseek/deepseek-v4-flash")
	infoA := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelA}}
	sessionA := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, infoA))

	InvalidateOpencodeSessionBinding(c, infoA)

	infoB := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelB}}
	sessionB := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, infoB))
	assert.NotEqual(t, sessionA, sessionB, "split target must not resurrect the invalidated session")
}

// 绑定属于其它渠道时应为 no-op：本轮请求未走到会话解析（adaptor 早失败）时，
// 缓存里的绑定可能属于健康渠道，不能被误删。
func TestInvalidateOpencodeSessionBinding_NoOpForOtherChannelBinding(t *testing.T) {
	require.NoError(t, resetOpencodeSessionStateForTest())

	const channelA = 62021
	const channelB = 62022
	body := `{"prompt_cache_key":"conv-invalidate-owner-62021"}`
	c := newOpencodeSessionTestContext(t, body, "", "")
	infoA := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelA}}
	sessionA := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, infoA))
	key := opencodeSessionKey(resolveOpencodeConversationKey(c, infoA, infoA.GetUpstreamModelName()))

	infoB := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelB}}
	InvalidateOpencodeSessionBinding(c, infoB)

	binding, found, err := getOpencodeSessionBinding(key)
	require.NoError(t, err)
	require.True(t, found, "another channel's binding must survive invalidation")
	assert.Equal(t, channelA, binding.ChannelID)
	assert.Equal(t, sessionA, binding.Session)

	active, err := isOpencodeActiveSession(channelA, key, time.Now().Unix())
	require.NoError(t, err)
	assert.True(t, active, "another channel's active slot must not be released")
}

// 无任何会话身份时不得 panic：身份缺失会回退到 token/user/model 作用域的键，
// 该键没有绑定，作废应为安全的 no-op。
func TestInvalidateOpencodeSessionBinding_NoIdentityIsSafeNoOp(t *testing.T) {
	require.NoError(t, resetOpencodeSessionStateForTest())

	c := newOpencodeSessionTestContext(t, "", "", "")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 62031}}

	require.NotPanics(t, func() {
		InvalidateOpencodeSessionBinding(c, info)
	})

	_, found, err := getOpencodeSessionBinding(opencodeSessionKey(resolveOpencodeConversationKey(c, info, info.GetUpstreamModelName())))
	require.NoError(t, err)
	assert.False(t, found, "no identity means no binding to invalidate")

	// 渠道 ID 取不到时更保守：直接返回，不误伤任何绑定。
	require.NotPanics(t, func() {
		InvalidateOpencodeSessionBinding(c, nil)
	})
}
