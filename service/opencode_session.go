package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/cachex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
	"github.com/tidwall/gjson"
)

const (
	ginKeyOpencodeSessionAffinity = "opencode_session_affinity"

	opencodeSessionCacheNamespace    = "new-api:opencode_session:v1"
	opencodeSessionDefaultTTLSeconds = 86400 // 24h
	opencodeSessionMaxCacheEntries   = 100_000
)

var (
	opencodeSessionCacheOnce sync.Once
	opencodeSessionCache     *cachex.HybridCache[string]

	opencodeSessionRandCounter uint64
)

// OpencodeSessionHeaderSpellings 是 OpenCode 会话 header 的全部拼写。
// 这些拼写仅用于会话身份推导与 OpenCode 上游 header 清理，
// 绝不作为上游 session 值转发。
var OpencodeSessionHeaderSpellings = []string{
	"X-OpenCode-Session",
	"X-OpenCode-Session-Id",
	"X-Session-Id",
	"Session-Id",
	"Session_id",
}

// opencodeSessionBodyPaths 是客户端会话身份在 JSON body 中的候选路径。
var opencodeSessionBodyPaths = []string{
	"prompt_cache_key",
	"session_id",
	"metadata.session_id",
}

func getOpencodeSessionCache() *cachex.HybridCache[string] {
	opencodeSessionCacheOnce.Do(func() {
		opencodeSessionCache = cachex.NewHybridCache[string](cachex.HybridCacheConfig[string]{
			Namespace: cachex.Namespace(opencodeSessionCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.StringCodec{},
			Memory: func() *hot.HotCache[string, string] {
				return hot.NewHotCache[string, string](hot.LRU, opencodeSessionMaxCacheEntries).
					WithTTL(time.Duration(opencodeSessionDefaultTTLSeconds) * time.Second).
					WithJanitor().
					Build()
			},
		})
	})
	return opencodeSessionCache
}

// ResolveOpencodeSession 返回 OpenCode 上游的会话 ID。
// 客户端携带的 session（header/body）仅作为会话身份来源参与会话键推导，
// 返回值是网关按（会话身份 × 渠道 ID）自管的 session，绝不等于客户端值，且永不返回空串。
// 会话键不并入渠道亲和 CacheKey：亲和值可能回退到 token 级（如 token_id），
// 直接对齐会让同一 token 下的不同对话复用同一上游 session。
func ResolveOpencodeSession(c *gin.Context, info *relaycommon.RelayInfo) string {
	channelID := opencodeChannelID(c, info)
	conversationKey := opencodeConversationKeyOrFallback(c, info, deriveOpencodeConversationKey(c, info))
	if channelID <= 0 {
		// 无渠道上下文：返回确定性的会话 ID，保证同一会话稳定且非空。
		return deterministicOpencodeSession(conversationKey)
	}

	sum := sha256.Sum256([]byte("opencode-conversation:" + conversationKey))
	key := strconv.Itoa(channelID) + ":" + hex.EncodeToString(sum[:])
	session, cacheHit := opencodeSessionGetOrCreate(getOpencodeSessionCache(), key, time.Duration(opencodeSessionDefaultTTLSeconds)*time.Second)
	markOpencodeSessionAffinity(c, channelID, session, cacheHit)
	return session
}

// opencodeChannelID 从 RelayInfo / gin context 解析渠道 ID，取不到时返回 0。
func opencodeChannelID(c *gin.Context, info *relaycommon.RelayInfo) int {
	if info != nil && info.ChannelMeta != nil && info.ChannelMeta.ChannelId > 0 {
		return info.ChannelMeta.ChannelId
	}
	if c != nil {
		if id := common.GetContextKeyInt(c, constant.ContextKeyChannelId); id > 0 {
			return id
		}
	}
	return 0
}

// deriveOpencodeConversationKey 提取客户端会话身份。优先级：header 候选 > body 候选。
// 返回值仅作会话身份，绝不直接作为上游 session 值。
func deriveOpencodeConversationKey(c *gin.Context, info *relaycommon.RelayInfo) string {
	for _, name := range OpencodeSessionHeaderSpellings {
		if value := opencodeHeaderValue(c, info, name); value != "" {
			return value
		}
	}
	for _, path := range opencodeSessionBodyPaths {
		if value := opencodeBodyString(c, path); value != "" {
			return value
		}
	}
	return ""
}

func opencodeHeaderValue(c *gin.Context, info *relaycommon.RelayInfo, name string) string {
	if c != nil && c.Request != nil {
		if value := strings.TrimSpace(c.Request.Header.Get(name)); value != "" {
			return value
		}
	}
	if info != nil {
		for key, value := range info.RequestHeaders {
			if strings.EqualFold(key, name) {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func opencodeBodyString(c *gin.Context, path string) string {
	if c == nil || strings.TrimSpace(path) == "" {
		return ""
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return ""
	}
	body, err := storage.Bytes()
	if err != nil || len(body) == 0 {
		return ""
	}
	res := gjson.GetBytes(body, path)
	if !res.Exists() {
		return ""
	}
	switch res.Type {
	case gjson.String, gjson.Number, gjson.True, gjson.False:
		return strings.TrimSpace(res.String())
	default:
		return strings.TrimSpace(res.Raw)
	}
}

// opencodeConversationKeyOrFallback 在会话身份缺失时回退到 token/user/model 组合。
func opencodeConversationKeyOrFallback(c *gin.Context, info *relaycommon.RelayInfo, conversationKey string) string {
	if conversationKey != "" {
		return conversationKey
	}
	tokenID, userID := 0, 0
	modelName := ""
	if info != nil {
		tokenID = info.TokenId
		userID = info.UserId
		modelName = strings.TrimSpace(info.OriginModelName)
		// UpstreamModelName 位于嵌入的 *ChannelMeta 上，ChannelMeta 为 nil 时不可解引用。
		if modelName == "" && info.ChannelMeta != nil {
			modelName = strings.TrimSpace(info.UpstreamModelName)
		}
	} else if c != nil {
		tokenID = common.GetContextKeyInt(c, constant.ContextKeyTokenId)
		userID = common.GetContextKeyInt(c, constant.ContextKeyUserId)
	}
	fallback := fmt.Sprintf("token:%d:user:%d:model:%s", tokenID, userID, modelName)
	return fallback
}

// opencodeSessionGetOrCreate 命中缓存返回已有 session（cacheHit=true），未命中生成新 session 并写入缓存。
// Set 出错仅记录日志，仍返回新生成的 session，保证永不空。
func opencodeSessionGetOrCreate(cache *cachex.HybridCache[string], key string, ttl time.Duration) (session string, cacheHit bool) {
	if session, found, err := cache.Get(key); err == nil && found && session != "" {
		return session, true
	}
	session = newOpencodeSessionID()
	if err := cache.SetWithTTL(key, session, ttl); err != nil {
		common.SysError(fmt.Sprintf("opencode session cache set failed: key=%s, err=%v", key, err))
	}
	return session, false
}

// markOpencodeSessionAffinity 将会话亲和结果记录到 gin context，
// 供消费日志 admin_info 展示（见 AppendOpencodeSessionAdminInfo）。
func markOpencodeSessionAffinity(c *gin.Context, channelID int, session string, cacheHit bool) {
	if c == nil || channelID <= 0 {
		return
	}
	c.Set(ginKeyOpencodeSessionAffinity, map[string]interface{}{
		"session_id":  session,
		"channel_id":  channelID,
		"cache_hit":   cacheHit,
		"affinitized": true,
	})
}

// AppendOpencodeSessionAdminInfo 将本请求的 OpenCode 会话亲和信息追加到 adminInfo。
// 无亲和信息时保持 adminInfo 不变，不写入空 map。
func AppendOpencodeSessionAdminInfo(c *gin.Context, adminInfo map[string]interface{}) {
	if c == nil || adminInfo == nil {
		return
	}
	anyInfo, ok := c.Get(ginKeyOpencodeSessionAffinity)
	if !ok || anyInfo == nil {
		return
	}
	adminInfo["opencode_session_affinity"] = anyInfo
}

// newOpencodeSessionID 生成随机的网关自管会话 ID，前缀固定为 byteiq-。
func newOpencodeSessionID() string {
	buf := make([]byte, 16)
	_, err := rand.Read(buf)
	if err == nil {
		return "byteiq-" + hex.EncodeToString(buf)
	}
	common.SysError(fmt.Sprintf("opencode session random generation failed: err=%v", err))
	counter := atomic.AddUint64(&opencodeSessionRandCounter, 1)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", time.Now().UnixNano(), counter)))
	return "byteiq-" + hex.EncodeToString(sum[:])
}

// deterministicOpencodeSession 无渠道上下文时的确定性会话 ID，永不空。
func deterministicOpencodeSession(identity string) string {
	sum := sha256.Sum256([]byte("byteiq-opencode:" + identity))
	return "byteiq-" + hex.EncodeToString(sum[:])[:32]
}
