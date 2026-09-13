package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOpencodeSessionTestContext(t *testing.T, body string, headerName string, headerValue string) *gin.Context {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if headerName != "" {
		req.Header.Set(headerName, headerValue)
	}
	c.Request = req
	if strings.TrimSpace(body) != "" {
		storage, err := common.CreateBodyStorage([]byte(body))
		require.NoError(t, err)
		c.Set(common.KeyBodyStorage, storage)
	}
	return c
}

func assertOpencodeSessionNonEmpty(t *testing.T, session string) string {
	t.Helper()
	require.NotEmpty(t, session)
	// 会话 ID 为 32 位十六进制随机串，不带业务前缀，避免向上游暴露网关身份。
	require.Regexp(t, `^[0-9a-f]{32}$`, session)
	return session
}

func TestResolveOpencodeSession_ReusesSessionForSameConversationAndChannel(t *testing.T) {
	const channelID = 61001
	body := fmt.Sprintf(`{"prompt_cache_key":"conv-reuse-%d"}`, channelID)
	c := newOpencodeSessionTestContext(t, body, "", "")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}}

	first := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, info))
	second := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, info))
	require.Equal(t, first, second, "same conversation on same channel must reuse the session")
}

func TestResolveOpencodeSession_DifferentConversationKeysDiffer(t *testing.T) {
	const channelID = 61002
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}}

	first := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(newOpencodeSessionTestContext(t, "", "X-Session-Id", fmt.Sprintf("sess-a-%d", channelID)), info))
	second := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(newOpencodeSessionTestContext(t, "", "X-Session-Id", fmt.Sprintf("sess-b-%d", channelID)), info))
	require.NotEqual(t, first, second, "different conversation identities must yield different sessions")
}

func TestResolveOpencodeSession_DifferentChannelsDiffer(t *testing.T) {
	body := `{"prompt_cache_key":"conv-shared-61003"}`
	infoA := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 61003}}
	infoB := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 61004}}

	sessionA := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(newOpencodeSessionTestContext(t, body, "", ""), infoA))
	sessionB := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(newOpencodeSessionTestContext(t, body, "", ""), infoB))
	require.NotEqual(t, sessionA, sessionB, "fallback to another channel must never reuse the previous channel's session")
}

func TestResolveOpencodeSession_NeverForwardsClientHeaderValue(t *testing.T) {
	const channelID = 61005
	c := newOpencodeSessionTestContext(t, "", "X-OpenCode-Session", "client-session-123")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}}

	session := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, info))
	assert.NotEqual(t, "client-session-123", session, "client session value must never be forwarded upstream")
}

func TestResolveOpencodeSession_BodyPromptCacheKeyParticipatesInIdentity(t *testing.T) {
	const channelID = 61006
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}}

	first := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(newOpencodeSessionTestContext(t, fmt.Sprintf(`{"prompt_cache_key":"pc-a-%d"}`, channelID), "", ""), info))
	second := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(newOpencodeSessionTestContext(t, fmt.Sprintf(`{"prompt_cache_key":"pc-b-%d"}`, channelID), "", ""), info))
	require.NotEqual(t, first, second, "prompt_cache_key must participate in the conversation identity")
}

// 会话键只由（会话身份 × 渠道 ID）决定，与渠道亲和 CacheKey 的粒度无关：
// 亲和值可能回退到 token 级（token_id 兜底），若会话键跟随亲和键，
// 同一 token 下的不同对话会复用同一上游 session。
func TestResolveOpencodeSession_SessionKeyIndependentOfAffinityMeta(t *testing.T) {
	const channelID = 61007
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}}

	first := newOpencodeSessionTestContext(t, `{"metadata":{"session_id":"conv-61007"}}`, "", "")
	setChannelAffinityContext(first, channelAffinityMeta{CacheKey: "affinity-test-key-61007", TTLSeconds: 3600, RuleName: "test-rule"})
	sessionA := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(first, info))

	second := newOpencodeSessionTestContext(t, `{"metadata":{"session_id":"conv-61007"}}`, "", "")
	setChannelAffinityContext(second, channelAffinityMeta{CacheKey: "affinity-test-key-other-61007", TTLSeconds: 3600, RuleName: "test-rule"})
	sessionB := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(second, info))
	require.Equal(t, sessionA, sessionB, "same conversation identity and channel must reuse the session regardless of affinity meta")

	third := newOpencodeSessionTestContext(t, `{"metadata":{"session_id":"conv-61007-other"}}`, "", "")
	setChannelAffinityContext(third, channelAffinityMeta{CacheKey: "affinity-test-key-61007", TTLSeconds: 3600, RuleName: "test-rule"})
	sessionC := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(third, info))
	require.NotEqual(t, sessionA, sessionC, "different conversations under a coarse affinity key must not share a session")
}

// 客户端只带 body metadata.session_id（亲和规则历史上不认识该字段）时，
// 不同对话也必须得到不同的上游 session。
func TestResolveOpencodeSession_MetadataSessionIDIsolatesConversations(t *testing.T) {
	const channelID = 61011
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}}

	first := newOpencodeSessionTestContext(t, `{"metadata":{"session_id":"conv-A"}}`, "", "")
	setChannelAffinityContext(first, channelAffinityMeta{CacheKey: "token-level-affinity-61011", TTLSeconds: 3600, RuleName: "opencode session trace"})
	sessionA := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(first, info))

	second := newOpencodeSessionTestContext(t, `{"metadata":{"session_id":"conv-B"}}`, "", "")
	setChannelAffinityContext(second, channelAffinityMeta{CacheKey: "token-level-affinity-61011", TTLSeconds: 3600, RuleName: "opencode session trace"})
	sessionB := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(second, info))

	require.NotEqual(t, sessionA, sessionB, "metadata.session_id must isolate conversations even under a token-level affinity key")
}

// 客户端完全不带会话身份时，身份回退到 token/user/model 组合：
// 同一 token 的不同对话共享同一上游 session，属既有兜底语义（无对话身份可区分）。
func TestResolveOpencodeSession_NoIdentityFallsBackToTokenScope(t *testing.T) {
	const channelID = 61012
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}, TokenId: 61012}

	first := newOpencodeSessionTestContext(t, `{"messages":[{"role":"user","content":"hi"}]}`, "", "")
	sessionA := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(first, info))
	second := newOpencodeSessionTestContext(t, `{"messages":[{"role":"user","content":"another conversation"}]}`, "", "")
	sessionB := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(second, info))

	require.Equal(t, sessionA, sessionB, "without any client identity, sessions fall back to token scope by design")
}

func TestResolveOpencodeSession_RecordsAffinityInfo(t *testing.T) {
	const channelID = 61009
	body := fmt.Sprintf(`{"prompt_cache_key":"conv-affinity-%d"}`, channelID)
	c := newOpencodeSessionTestContext(t, body, "", "")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}}

	first := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, info))
	anyInfo, ok := c.Get(ginKeyOpencodeSessionAffinity)
	require.True(t, ok, "affinity info must be recorded on context")
	infoMap, ok := anyInfo.(map[string]interface{})
	require.True(t, ok, "affinity info must be a map: %T", anyInfo)
	assert.Equal(t, first, infoMap["session_id"], "session_id must match the returned session")
	assert.Equal(t, channelID, infoMap["channel_id"], "channel_id must match")
	assert.Equal(t, false, infoMap["cache_hit"], "first call must not be a cache hit")
	assert.Equal(t, true, infoMap["affinitized"], "affinitized must be true when a channel is present")

	second := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, info))
	require.Equal(t, first, second, "same conversation on same channel must reuse the session")
	anyInfo, ok = c.Get(ginKeyOpencodeSessionAffinity)
	require.True(t, ok, "affinity info must be recorded on context")
	infoMap, ok = anyInfo.(map[string]interface{})
	require.True(t, ok, "affinity info must be a map: %T", anyInfo)
	assert.Equal(t, second, infoMap["session_id"], "session_id must match the returned session")
	assert.Equal(t, true, infoMap["cache_hit"], "second call must be a cache hit")
}

func TestAppendOpencodeSessionAdminInfo(t *testing.T) {
	const channelID = 61010
	body := fmt.Sprintf(`{"prompt_cache_key":"conv-admin-%d"}`, channelID)
	c := newOpencodeSessionTestContext(t, body, "", "")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID}}

	session := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, info))

	adminInfo := map[string]interface{}{"existing_key": "keep-me"}
	AppendOpencodeSessionAdminInfo(c, adminInfo)
	assert.Equal(t, "keep-me", adminInfo["existing_key"], "existing admin info must be preserved")
	anyAffinity, ok := adminInfo["opencode_session_affinity"]
	require.True(t, ok, "admin info must contain opencode_session_affinity")
	affinityMap, ok := anyAffinity.(map[string]interface{})
	require.True(t, ok, "opencode_session_affinity must be a map: %T", anyAffinity)
	assert.Equal(t, session, affinityMap["session_id"], "session_id must match the returned session")
	assert.Equal(t, channelID, affinityMap["channel_id"], "channel_id must match")
	assert.Equal(t, false, affinityMap["cache_hit"], "first call must not be a cache hit")
	assert.Equal(t, true, affinityMap["affinitized"], "affinitized must be true when a channel is present")
}

func TestResolveOpencodeSession_NoAffinityInfoWithoutChannel(t *testing.T) {
	tests := []struct {
		name string
		info *relaycommon.RelayInfo
		body string
	}{
		{
			name: "nil info no channel id",
			info: nil,
			body: "",
		},
		{
			name: "zero channel id",
			info: &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 0}},
			body: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpencodeSessionTestContext(t, tt.body, "", "")
			assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, tt.info))

			_, ok := c.Get(ginKeyOpencodeSessionAffinity)
			assert.False(t, ok, "no affinity info must be recorded without a channel")

			adminInfo := map[string]interface{}{}
			AppendOpencodeSessionAdminInfo(c, adminInfo)
			_, exists := adminInfo["opencode_session_affinity"]
			assert.False(t, exists, "no affinity info must be written to admin info without a channel")
		})
	}
}

func TestResolveOpencodeSession_NeverEmpty(t *testing.T) {
	tests := []struct {
		name         string
		info         *relaycommon.RelayInfo
		body         string
		contextKeyID int
	}{
		{
			name: "nil info no body no channel id",
			info: nil,
			body: "",
		},
		{
			name: "zero channel id",
			info: &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 0}},
			body: "",
		},
		{
			name: "empty identity info and empty body",
			info: &relaycommon.RelayInfo{TokenId: 0, UserId: 0},
			body: `{}`,
		},
		{
			name:         "channel id from context key",
			info:         nil,
			body:         "",
			contextKeyID: 61008,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpencodeSessionTestContext(t, tt.body, "", "")
			if tt.contextKeyID > 0 {
				common.SetContextKey(c, constant.ContextKeyChannelId, tt.contextKeyID)
			}
			session := assertOpencodeSessionNonEmpty(t, ResolveOpencodeSession(c, tt.info))
			require.NotEmpty(t, session)
		})
	}
}
