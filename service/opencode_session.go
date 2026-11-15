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
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cachex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
	"github.com/tidwall/gjson"
)

const (
	ginKeyOpencodeSessionAffinity     = "opencode_session_affinity"
	ginKeyOpencodeSessionQueue        = "opencode_session_queue"
	ginKeyOpencodeSessionHardFallback = "opencode_session_hard_fallback"
	ginKeyOpencodeSessionSelectedID   = "opencode_session_selected_id"
	ginKeyOpencodeSessionWaitDone     = "opencode_session_wait_done"

	opencodeSessionCacheNamespace      = "new-api:opencode_session:v1"
	opencodeActiveSessionNamespace     = "new-api:opencode_active_sessions:v1"
	opencodeSessionMaxCacheEntries     = 100_000
	opencodeSessionQueueTimeoutDefault = 8 * time.Second
)

// opencodeActiveWindowSeconds is the sliding active occupancy window.
// Tests may override it instead of sleeping for the production 15s window.
var opencodeActiveWindowSeconds = 15

// opencodeSessionQueueTimeout is the bounded FIFO wait when every eligible
// channel is at session capacity. Tests may set it to 0 to exercise the
// least-loaded hard fallback without a timing race.
var opencodeSessionQueueTimeout = opencodeSessionQueueTimeoutDefault

const (
	opencodeSessionEventCreate     = "create"
	opencodeSessionEventHit        = "hit"
	opencodeSessionEventMove       = "move"
	opencodeSessionEventQueue      = "queue"
	opencodeSessionEventExpire     = "expire"
	opencodeSessionEventInvalidate = "invalidate"
)

type opencodeSessionBinding struct {
	ChannelID int    `json:"channel_id"`
	Session   string `json:"session"`
}

type opencodeKnownSession struct {
	Binding   opencodeSessionBinding
	ExpiresAt int64
}

var (
	opencodeSessionCacheOnce sync.Once
	opencodeSessionCache     *cachex.HybridCache[opencodeSessionBinding]
	opencodeActiveCacheOnce  sync.Once
	opencodeActiveCache      *cachex.HybridCache[map[string]int64]
	opencodeActiveMu         sync.Mutex
	opencodeKnownMu          sync.Mutex
	opencodeKnownSessions    = make(map[string]opencodeKnownSession)

	opencodeSessionRandCounter uint64

	opencodeSessionWaitMu    sync.Mutex
	opencodeSessionWaitQueue []*opencodeSessionWaitEntry
	opencodeSessionWaitWake  = make(chan struct{})
)

type opencodeSessionWaitKey struct {
	group string
	model string
}

type opencodeSessionWaitEntry struct {
	ctx *gin.Context
	key opencodeSessionWaitKey
}

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

func getOpencodeSessionCache() *cachex.HybridCache[opencodeSessionBinding] {
	opencodeSessionCacheOnce.Do(func() {
		opencodeSessionCache = cachex.NewHybridCache[opencodeSessionBinding](cachex.HybridCacheConfig[opencodeSessionBinding]{
			Namespace: cachex.Namespace(opencodeSessionCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[opencodeSessionBinding]{},
			Memory: func() *hot.HotCache[string, opencodeSessionBinding] {
				return hot.NewHotCache[string, opencodeSessionBinding](hot.LRU, opencodeSessionMaxCacheEntries).
					WithTTL(opencodeSessionRetention()).
					WithJanitor().
					Build()
			},
		})
	})
	return opencodeSessionCache
}

func getOpencodeActiveCache() *cachex.HybridCache[map[string]int64] {
	opencodeActiveCacheOnce.Do(func() {
		opencodeActiveCache = cachex.NewHybridCache[map[string]int64](cachex.HybridCacheConfig[map[string]int64]{
			Namespace: cachex.Namespace(opencodeActiveSessionNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[map[string]int64]{},
			Memory: func() *hot.HotCache[string, map[string]int64] {
				return hot.NewHotCache[string, map[string]int64](hot.LRU, opencodeSessionMaxCacheEntries).
					WithTTL(time.Duration(opencodeActiveWindowSeconds) * time.Second).
					WithJanitor().
					Build()
			},
		})
	})
	return opencodeActiveCache
}

func opencodeSessionRetention() time.Duration {
	seconds := operation_setting.GetOpencodeSessionRetentionSeconds()
	if seconds <= 0 {
		seconds = operation_setting.DefaultOpencodeSessionRetentionSeconds
	}
	return time.Duration(seconds) * time.Second
}

// resetOpencodeSessionStateForTest clears in-process session caches and wait
// queues so capacity tests do not leak active counts across cases.
func resetOpencodeSessionStateForTest() error {
	if err := getOpencodeSessionCache().Purge(); err != nil {
		return err
	}
	if err := getOpencodeActiveCache().Purge(); err != nil {
		return err
	}
	opencodeKnownMu.Lock()
	opencodeKnownSessions = make(map[string]opencodeKnownSession)
	opencodeKnownMu.Unlock()
	opencodeSessionWaitMu.Lock()
	opencodeSessionWaitQueue = nil
	opencodeSessionWaitWake = make(chan struct{})
	opencodeSessionWaitMu.Unlock()
	return nil
}

// ResolveOpencodeSession 返回 OpenCode 上游的会话 ID。渠道选择阶段已经决定了
// 会话绑定；这里仅按 conversation key 读取绑定并刷新滑动 TTL，保证 adaptor
// 不会在渠道确定前创建一个无法迁移的 session。
func ResolveOpencodeSession(c *gin.Context, info *relaycommon.RelayInfo) string {
	channelID := opencodeChannelID(c, info)
	conversationKey := resolveOpencodeConversationKey(c, info, info.GetUpstreamModelName())
	if channelID <= 0 {
		// 无渠道上下文：返回确定性的会话 ID，保证同一会话稳定且非空。
		return deterministicOpencodeSession(conversationKey)
	}

	key := opencodeSessionKey(conversationKey)
	binding, found, err := getOpencodeSessionBinding(key)
	if err != nil {
		common.SysError(fmt.Sprintf("opencode session cache get failed: key=%s, err=%v", key, err))
	}
	if found && binding.ChannelID == channelID && binding.Session != "" {
		if err := setOpencodeSessionBinding(key, binding); err != nil {
			common.SysError(fmt.Sprintf("opencode session cache set failed: key=%s, err=%v", key, err))
		}
		_, _ = touchOpencodeActiveSession(channelID, key, time.Now().Unix())
		markOpencodeSessionAffinity(c, channelID, binding.Session, true, opencodeSessionEventHit)
		return binding.Session
	}

	previousChannelID := 0
	if found {
		previousChannelID = binding.ChannelID
	}
	session := newOpencodeSessionID()
	// 分流换目标时旧渠道可能无法服务新模型，需要把绑定迁到新渠道，但必须复用
	// 已有 session。无分流时换渠道仍新建：旧 session 在新上游账号上无效。
	if found && binding.Session != "" && RequestModelSplitTarget(c) != "" {
		session = binding.Session
	}
	binding = opencodeSessionBinding{ChannelID: channelID, Session: session}
	if err := setOpencodeSessionBinding(key, binding); err != nil {
		common.SysError(fmt.Sprintf("opencode session cache set failed: key=%s, err=%v", key, err))
	}
	if previousChannelID > 0 && previousChannelID != channelID {
		_, _ = releaseOpencodeActiveSession(previousChannelID, key, time.Now().Unix())
	}
	_, _ = touchOpencodeActiveSession(channelID, key, time.Now().Unix())
	event := opencodeSessionEventCreate
	if previousChannelID > 0 && previousChannelID != channelID {
		event = opencodeSessionEventMove
	}
	markOpencodeSessionAffinity(c, channelID, session, false, event)
	return session
}

// InvalidateOpencodeSessionBinding 作废当前会话在失败渠道上的绑定。
// code provider 上游出错要转移到别的渠道时调用：清掉绑定后，下一轮
// ResolveOpencodeSession 找不到旧绑定，会为新渠道新建会话 ID，避免把失败渠道的
// 旧 session 带到新渠道造成冲突。幂等：无绑定或身份不可用时直接返回。
//
// 只作废「当前请求渠道」持有的绑定：当本轮请求未走到 ResolveOpencodeSession
// （例如 adaptor 早失败）时，缓存里的绑定可能属于其它健康渠道，误删会破坏其会话粘性。
// 若当前渠道 ID 取不到（<=0），无法判定归属，采取更保守的策略：直接返回、不作废，
// 宁可不清理也不误伤其它渠道的绑定。
func InvalidateOpencodeSessionBinding(c *gin.Context, info *relaycommon.RelayInfo) {
	if c == nil {
		return
	}
	currentChannelID := opencodeChannelID(c, info)
	if currentChannelID <= 0 {
		return
	}
	conversationKey := resolveOpencodeConversationKey(c, info, info.GetUpstreamModelName())
	if conversationKey == "" {
		return
	}
	key := opencodeSessionKey(conversationKey)
	binding, found, err := getOpencodeSessionBinding(key)
	if err != nil {
		common.SysError(fmt.Sprintf("opencode session cache get failed: key=%s, err=%v", key, err))
	}
	if !found {
		return
	}
	// 绑定归属其它渠道：不受本次失败影响，保持原样返回。
	if binding.ChannelID != currentChannelID {
		return
	}
	if _, err := getOpencodeSessionCache().DeleteMany([]string{key}); err != nil {
		common.SysError(fmt.Sprintf("opencode session cache delete failed: key=%s, err=%v", key, err))
	}
	opencodeKnownMu.Lock()
	delete(opencodeKnownSessions, key)
	opencodeKnownMu.Unlock()
	if _, err := releaseOpencodeActiveSession(binding.ChannelID, key, time.Now().Unix()); err != nil {
		common.SysError(fmt.Sprintf("opencode session release failed: key=%s, err=%v", key, err))
	}
	markOpencodeSessionAffinity(c, binding.ChannelID, binding.Session, false, opencodeSessionEventInvalidate)
	model.RecordOpencodeSessionLog(model.OpencodeSessionLogParams{
		UserID:          common.GetContextKeyInt(c, constant.ContextKeyUserId),
		Event:           opencodeSessionEventInvalidate,
		SessionID:       binding.Session,
		ConversationKey: key,
		ChannelID:       binding.ChannelID,
	})
}

func opencodeSessionKey(conversationKey string) string {
	sum := sha256.Sum256([]byte("opencode-conversation:" + conversationKey))
	return hex.EncodeToString(sum[:])
}

func getOpencodeSessionBinding(key string) (opencodeSessionBinding, bool, error) {
	binding, found, err := getOpencodeSessionCache().Get(key)
	if found && binding.Session != "" && binding.ChannelID > 0 {
		return binding, true, err
	}
	if !found {
		recordExpiredOpencodeSession(key)
	}
	return opencodeSessionBinding{}, false, err
}

func setOpencodeSessionBinding(key string, binding opencodeSessionBinding) error {
	rememberOpencodeSession(key, binding)
	return getOpencodeSessionCache().SetWithTTL(key, binding, opencodeSessionRetention())
}

func rememberOpencodeSession(key string, binding opencodeSessionBinding) {
	if key == "" || binding.Session == "" || binding.ChannelID <= 0 {
		return
	}
	opencodeKnownMu.Lock()
	opencodeKnownSessions[key] = opencodeKnownSession{
		Binding:   binding,
		ExpiresAt: time.Now().Add(opencodeSessionRetention()).Unix(),
	}
	opencodeKnownMu.Unlock()
}

func recordExpiredOpencodeSession(key string) {
	opencodeKnownMu.Lock()
	known, ok := opencodeKnownSessions[key]
	if ok {
		delete(opencodeKnownSessions, key)
	}
	opencodeKnownMu.Unlock()
	if !ok || known.ExpiresAt > time.Now().Unix() {
		return
	}
	model.RecordOpencodeSessionLog(model.OpencodeSessionLogParams{
		Event:           opencodeSessionEventExpire,
		SessionID:       known.Binding.Session,
		ConversationKey: key,
		ChannelID:       known.Binding.ChannelID,
	})
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
	// 无显式 session id 时，用对话开头（到第一条 assistant 之前的 system/user）
	// 作为身份：同对话多轮可复用缓存，内容不同的独立请求不会挤进同一个 session。
	if root := opencodeMessageRoot(c); root != "" {
		sum := sha256.Sum256([]byte("opencode-msgroot:" + root))
		return "msgroot:" + hex.EncodeToString(sum[:])
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

// resolveOpencodeConversationKey 统一会话身份：
// 1. 客户端显式 session / prompt_cache_key
// 2. 无显式 id 时用 messages 对话开头（可复用缓存，且能拆开不同对话）
// 3. 仍没有可用身份时才回退到 token/user/model（避免「完全不复用」）
func resolveOpencodeConversationKey(c *gin.Context, info *relaycommon.RelayInfo, modelName string) string {
	key := deriveOpencodeConversationKey(c, info)
	tokenID, userID, resolvedModel := opencodeIdentityScope(c, info, modelName)
	return qualifyOpencodeConversationKey(tokenID, userID, resolvedModel, key)
}

func opencodeConversationKeyForModel(c *gin.Context, modelName string) string {
	return resolveOpencodeConversationKey(c, nil, modelName)
}

// requestSessionIdentityModel 是会话/亲和身份用的模型名，通用亲和与 OpenCode
// 会话共用。匹配（regex、候选、配额）用调用方传入的模型（分流后是 target）；
// IncludeModelName 的 cache key 与 conversation identity 用用户原模型，避免分流拆粘性。
// 永不把 ContextKeyModelSplitTarget 当身份。优先级：
// original_model → info.OriginModelName → fallback → info.UpstreamModelName。
func requestSessionIdentityModel(c *gin.Context, info *relaycommon.RelayInfo, fallback string) string {
	if c != nil {
		if original := strings.TrimSpace(common.GetContextKeyString(c, constant.ContextKeyOriginalModel)); original != "" {
			return original
		}
	}
	if info != nil {
		if original := strings.TrimSpace(info.OriginModelName); original != "" {
			return original
		}
	}
	if trimmed := strings.TrimSpace(fallback); trimmed != "" {
		return trimmed
	}
	if info != nil {
		if upstream := strings.TrimSpace(info.GetUpstreamModelName()); upstream != "" {
			return upstream
		}
	}
	return ""
}

func opencodeIdentityScope(c *gin.Context, info *relaycommon.RelayInfo, modelName string) (tokenID, userID int, model string) {
	model = requestSessionIdentityModel(c, info, modelName)
	if info != nil {
		tokenID = info.TokenId
		userID = info.UserId
	}
	if c != nil {
		if tokenID == 0 {
			tokenID = common.GetContextKeyInt(c, constant.ContextKeyTokenId)
		}
		if userID == 0 {
			userID = common.GetContextKeyInt(c, constant.ContextKeyUserId)
		}
	}
	return tokenID, userID, model
}

func qualifyOpencodeConversationKey(tokenID, userID int, modelName, conversationKey string) string {
	if conversationKey == "" {
		return fmt.Sprintf("token:%d:user:%d:model:%s", tokenID, userID, modelName)
	}
	// 消息指纹不是全局唯一的（不同用户可能说同一句），必须挂上 token/user。
	if strings.HasPrefix(conversationKey, "msgroot:") {
		return fmt.Sprintf("token:%d:user:%d:%s", tokenID, userID, conversationKey)
	}
	return conversationKey
}

// opencodeMessageRoot 取出对话开头：从 messages[0] 走到第一条 assistant/tool 之前。
// 多轮请求若带完整历史，开头不变，因而能命中同一 session；独立任务的首条 user 不同则拆开。
func opencodeMessageRoot(c *gin.Context) string {
	if c == nil {
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
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		messages = gjson.GetBytes(body, "input")
	}
	if !messages.IsArray() {
		return ""
	}
	var b strings.Builder
	foundUser := false
	messages.ForEach(func(_, msg gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		if role == "assistant" || role == "tool" || role == "function" {
			return false
		}
		text := opencodeMessageText(msg)
		if text == "" {
			return true
		}
		if role == "user" || role == "" {
			foundUser = true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(role)
		b.WriteByte(':')
		b.WriteString(text)
		return true
	})
	if !foundUser {
		return ""
	}
	return b.String()
}

func opencodeMessageText(msg gjson.Result) string {
	content := msg.Get("content")
	switch {
	case content.Type == gjson.String:
		return strings.TrimSpace(content.String())
	case content.IsArray():
		var parts []string
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				if text := strings.TrimSpace(part.String()); text != "" {
					parts = append(parts, text)
				}
				return true
			}
			partType := strings.ToLower(part.Get("type").String())
			if partType == "" || partType == "text" || partType == "input_text" || partType == "output_text" {
				if text := strings.TrimSpace(part.Get("text").String()); text != "" {
					parts = append(parts, text)
				}
			}
			return true
		})
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return strings.TrimSpace(msg.Get("text").String())
	}
}

func getOpencodeActiveSessions(channelID int, now int64) (map[string]int64, error) {
	if channelID <= 0 {
		return map[string]int64{}, nil
	}
	active, found, err := getOpencodeActiveCache().Get(strconv.Itoa(channelID))
	if err != nil {
		return nil, err
	}
	if !found || active == nil {
		return map[string]int64{}, nil
	}
	changed := false
	for sessionKey, lastSeen := range active {
		if lastSeen < now-int64(opencodeActiveWindowSeconds) {
			delete(active, sessionKey)
			changed = true
		}
	}
	if changed {
		if len(active) == 0 {
			_, _ = getOpencodeActiveCache().DeleteMany([]string{strconv.Itoa(channelID)})
		} else if setErr := getOpencodeActiveCache().SetWithTTL(strconv.Itoa(channelID), active, time.Duration(opencodeActiveWindowSeconds)*time.Second); setErr != nil {
			return nil, setErr
		}
		NotifyOpencodeSessionQueue()
	}
	return active, nil
}

func releaseOpencodeActiveSession(channelID int, sessionKey string, now int64) (bool, error) {
	if channelID <= 0 || sessionKey == "" {
		return false, nil
	}
	opencodeActiveMu.Lock()
	defer opencodeActiveMu.Unlock()
	active, err := getOpencodeActiveSessions(channelID, now)
	if err != nil {
		return false, err
	}
	if _, ok := active[sessionKey]; !ok {
		return false, nil
	}
	delete(active, sessionKey)
	if len(active) == 0 {
		_, err = getOpencodeActiveCache().DeleteMany([]string{strconv.Itoa(channelID)})
	} else {
		err = getOpencodeActiveCache().SetWithTTL(strconv.Itoa(channelID), active, time.Duration(opencodeActiveWindowSeconds)*time.Second)
	}
	if err != nil {
		return false, err
	}
	NotifyOpencodeSessionQueue()
	return true, nil
}

func touchOpencodeActiveSession(channelID int, sessionKey string, now int64) (bool, error) {
	if channelID <= 0 || sessionKey == "" {
		return false, nil
	}
	opencodeActiveMu.Lock()
	defer opencodeActiveMu.Unlock()
	active, err := getOpencodeActiveSessions(channelID, now)
	if err != nil {
		return false, err
	}
	_, wasActive := active[sessionKey]
	active[sessionKey] = now
	if err := getOpencodeActiveCache().SetWithTTL(strconv.Itoa(channelID), active, time.Duration(opencodeActiveWindowSeconds)*time.Second); err != nil {
		return wasActive, err
	}
	return wasActive, nil
}

func isOpencodeActiveSession(channelID int, sessionKey string, now int64) (bool, error) {
	opencodeActiveMu.Lock()
	defer opencodeActiveMu.Unlock()
	active, err := getOpencodeActiveSessions(channelID, now)
	if err != nil {
		return false, err
	}
	_, ok := active[sessionKey]
	return ok, nil
}

func countOpencodeActiveSessions(channelID int, now int64) (int, error) {
	opencodeActiveMu.Lock()
	defer opencodeActiveMu.Unlock()
	active, err := getOpencodeActiveSessions(channelID, now)
	if err != nil {
		return 0, err
	}
	return len(active), nil
}

func setOpencodeSessionQueueRequired(c *gin.Context, required bool) {
	if c != nil {
		c.Set(ginKeyOpencodeSessionQueue, required)
	}
}

func IsOpencodeSessionQueueRequired(c *gin.Context) bool {
	return c != nil && c.GetBool(ginKeyOpencodeSessionQueue)
}

func setOpencodeSessionHardFallback(c *gin.Context, enabled bool) {
	if c != nil {
		c.Set(ginKeyOpencodeSessionHardFallback, enabled)
	}
}

func isOpencodeSessionHardFallback(c *gin.Context) bool {
	return c != nil && c.GetBool(ginKeyOpencodeSessionHardFallback)
}

func rememberOpencodeSessionSelection(c *gin.Context, channelID int) {
	if c != nil && channelID > 0 {
		c.Set(ginKeyOpencodeSessionSelectedID, channelID)
	}
}

func rememberedOpencodeSessionChannelID(c *gin.Context) int {
	if c == nil {
		return 0
	}
	return c.GetInt(ginKeyOpencodeSessionSelectedID)
}

func isOpencodeChannelAttempted(attempted map[int]struct{}, channelID int) bool {
	if channelID <= 0 || attempted == nil {
		return false
	}
	_, ok := attempted[channelID]
	return ok
}

// SelectOpencodeSessionChannel applies the session-capacity gate before the
// ordinary least-loaded request selection. It returns queueRequired when all
// eligible channels are at session capacity; the caller must wait on the
// (group, model) FIFO and only then use the returned channel as hard-fallback.
func SelectOpencodeSessionChannel(c *gin.Context, group, modelName, requestPath string, candidates []*model.Channel, attempted map[int]struct{}) (*model.Channel, bool, error) {
	if c == nil || strings.TrimSpace(group) == "" || strings.TrimSpace(modelName) == "" {
		return nil, false, nil
	}
	setOpencodeSessionQueueRequired(c, false)
	quotaCandidates, err := model.GetCodeProviderQuotaCandidates(group, modelName, attempted)
	if err != nil {
		return nil, false, err
	}
	candidateByID := make(map[int]*model.Channel, len(quotaCandidates)+len(candidates))
	for _, candidate := range quotaCandidates {
		if candidate != nil {
			candidateByID[candidate.Id] = candidate
		}
	}
	for _, candidate := range candidates {
		if candidate != nil {
			if model.IsFallbackChannel(candidate.Id) {
				continue
			}
			candidateByID[candidate.Id] = candidate
		}
	}
	if len(candidateByID) == 0 {
		return nil, false, nil
	}
	allCandidates := make([]*model.Channel, 0, len(candidateByID))
	for _, candidate := range candidateByID {
		if candidate.Status != common.ChannelStatusEnabled ||
			!model.IsChannelEnabledForGroupModel(group, modelName, candidate.Id) ||
			!model.ChannelSupportsRequestPath(candidate, requestPath, modelName) {
			continue
		}
		allCandidates = append(allCandidates, candidate)
	}
	if len(allCandidates) == 0 {
		return nil, false, nil
	}

	// 会话身份必须用原模型，候选/配额继续用入参 modelName（分流目标）。
	identityModel := requestSessionIdentityModel(c, nil, modelName)
	conversationKey := opencodeConversationKeyForModel(c, identityModel)
	sessionKey := opencodeSessionKey(conversationKey)
	c.Set("opencode_session_model", identityModel)
	c.Set("opencode_session_group", group)

	if selectedID := rememberedOpencodeSessionChannelID(c); selectedID > 0 && !isOpencodeChannelAttempted(attempted, selectedID) {
		var remembered *model.Channel
		if channel := findOpencodeCandidate(allCandidates, selectedID); channel != nil {
			remembered = channel
		} else if channel, channelErr := model.CacheGetChannel(selectedID); channelErr == nil && channel != nil && !model.IsFallbackChannel(channel.Id) && channel.Status == common.ChannelStatusEnabled && model.IsChannelEnabledForGroupModel(group, modelName, channel.Id) && model.ChannelSupportsRequestPath(channel, requestPath, modelName) {
			remembered = channel
		}
		if remembered != nil {
			if binding, found, bindErr := getOpencodeSessionBinding(sessionKey); bindErr == nil && found {
				_ = setOpencodeSessionBinding(sessionKey, binding)
				_, _ = touchOpencodeActiveSession(remembered.Id, sessionKey, time.Now().Unix())
			}
			return remembered, false, nil
		}
	}

	binding, found, err := getOpencodeSessionBinding(sessionKey)
	if err != nil {
		return nil, false, err
	}
	assign := !shouldDeferOpencodeSessionAssignment(c, group, modelName)
	if found {
		attemptedOriginal := isOpencodeChannelAttempted(attempted, binding.ChannelID)
		original := findOpencodeCandidate(allCandidates, binding.ChannelID)
		if original == nil && !attemptedOriginal {
			if channel, channelErr := model.CacheGetChannel(binding.ChannelID); channelErr == nil && channel != nil && !model.IsFallbackChannel(channel.Id) && channel.Status == common.ChannelStatusEnabled && model.IsChannelEnabledForGroupModel(group, modelName, channel.Id) && model.ChannelSupportsRequestPath(channel, requestPath, modelName) {
				quotaAvailable, quotaErr := model.CodeProviderChannelHasCapacity(channel, modelName, time.Now().Unix(), false)
				if quotaErr != nil {
					return nil, false, quotaErr
				}
				if quotaAvailable {
					original = channel
				}
			}
		}
		if original == nil {
			if attemptedOriginal || RequestModelSplitTarget(c) != "" {
				if channel, ok, capacityErr := chooseOpencodeSessionCapacity(c, allCandidates, binding.ChannelID, sessionKey, binding, group, modelName, assign); capacityErr != nil {
					return nil, false, capacityErr
				} else if ok {
					return channel, false, nil
				} else if channel != nil {
					setOpencodeSessionQueueRequired(c, true)
					return channel, true, nil
				}
			} else {
				_, _ = getOpencodeSessionCache().DeleteMany([]string{sessionKey})
				found = false
				binding = opencodeSessionBinding{}
			}
		} else {
			active, activeErr := isOpencodeActiveSession(original.Id, sessionKey, time.Now().Unix())
			if activeErr != nil {
				return nil, false, activeErr
			}
			if active {
				if setErr := setOpencodeSessionBinding(sessionKey, binding); setErr != nil {
					return nil, false, setErr
				}
				_, _ = touchOpencodeActiveSession(original.Id, sessionKey, time.Now().Unix())
				markOpencodeSessionAffinity(c, original.Id, binding.Session, true, opencodeSessionEventHit)
				rememberOpencodeSessionSelection(c, original.Id)
				return original, false, nil
			}
			originalActiveCount, countErr := countOpencodeActiveSessions(original.Id, time.Now().Unix())
			if countErr != nil {
				return nil, false, countErr
			}
			if originalActiveCount < operation_setting.GetOpencodeConcurrencyLimit() {
				if setErr := setOpencodeSessionBinding(sessionKey, binding); setErr != nil {
					return nil, false, setErr
				}
				_, _ = touchOpencodeActiveSession(original.Id, sessionKey, time.Now().Unix())
				markOpencodeSessionAffinity(c, original.Id, binding.Session, true, opencodeSessionEventHit)
				rememberOpencodeSessionSelection(c, original.Id)
				model.RecordOpencodeSessionLog(model.OpencodeSessionLogParams{
					UserID:          common.GetContextKeyInt(c, constant.ContextKeyUserId),
					Event:           opencodeSessionEventHit,
					SessionID:       binding.Session,
					ConversationKey: sessionKey,
					ChannelID:       original.Id,
					ModelName:       modelName,
					Group:           group,
				})
				return original, false, nil
			}
			excludedID := original.Id
			if isOpencodeSessionHardFallback(c) {
				excludedID = 0
			}
			if channel, ok, capacityErr := chooseOpencodeSessionCapacity(c, allCandidates, excludedID, sessionKey, binding, group, modelName, assign); capacityErr != nil {
				return nil, false, capacityErr
			} else if ok {
				return channel, false, nil
			} else if channel != nil {
				setOpencodeSessionQueueRequired(c, true)
				return channel, true, nil
			}
		}
	}

	channel, ok, err := chooseOpencodeSessionCapacity(c, allCandidates, 0, sessionKey, binding, group, modelName, assign)
	if err != nil || ok {
		return channel, false, err
	}
	if channel == nil {
		return nil, false, nil
	}
	setOpencodeSessionQueueRequired(c, true)
	return channel, true, nil
}

func findOpencodeCandidate(candidates []*model.Channel, channelID int) *model.Channel {
	for _, candidate := range candidates {
		if candidate != nil && candidate.Id == channelID {
			return candidate
		}
	}
	return nil
}

func chooseOpencodeSessionCapacity(c *gin.Context, candidates []*model.Channel, excludedID int, sessionKey string, binding opencodeSessionBinding, group, modelName string, assign bool) (*model.Channel, bool, error) {
	limit := operation_setting.GetOpencodeConcurrencyLimit()
	now := time.Now().Unix()
	hardFallback := isOpencodeSessionHardFallback(c)

	leastNotFullCount := int(^uint(0) >> 1)
	leastAllCount := int(^uint(0) >> 1)
	var leastNotFull []*model.Channel
	var leastAll []*model.Channel
	for _, candidate := range candidates {
		if candidate == nil || candidate.Id == excludedID {
			continue
		}
		count, err := countOpencodeActiveSessions(candidate.Id, now)
		if err != nil {
			return nil, false, err
		}
		if count < limit {
			if count < leastNotFullCount {
				leastNotFullCount = count
				leastNotFull = leastNotFull[:0]
			}
			if count == leastNotFullCount {
				leastNotFull = append(leastNotFull, candidate)
			}
		}
		if count < leastAllCount {
			leastAllCount = count
			leastAll = leastAll[:0]
		}
		if count == leastAllCount {
			leastAll = append(leastAll, candidate)
		}
	}

	var channel *model.Channel
	switch {
	case len(leastNotFull) > 0:
		channel = leastNotFull[0]
	case len(leastAll) == 0:
		return nil, false, nil
	case hardFallback && assign:
		channel = leastAll[0]
	default:
		setOpencodeSessionQueueRequired(c, true)
		return leastAll[0], false, nil
	}
	if !assign {
		setOpencodeSessionQueueRequired(c, true)
		return channel, false, nil
	}

	session := binding.Session
	if session == "" || (binding.ChannelID != channel.Id && RequestModelSplitTarget(c) == "") {
		session = newOpencodeSessionID()
	}
	newBinding := opencodeSessionBinding{ChannelID: channel.Id, Session: session}
	if err := setOpencodeSessionBinding(sessionKey, newBinding); err != nil {
		return nil, false, err
	}
	event := opencodeSessionEventCreate
	cacheHit := false
	fromChannelID := 0
	if binding.ChannelID == channel.Id && binding.Session != "" {
		event = opencodeSessionEventHit
		cacheHit = true
	} else if binding.ChannelID > 0 && binding.ChannelID != channel.Id {
		event = opencodeSessionEventMove
		fromChannelID = binding.ChannelID
	}
	markOpencodeSessionAffinity(c, channel.Id, newBinding.Session, cacheHit, event)
	if event == opencodeSessionEventMove && fromChannelID > 0 {
		_, _ = releaseOpencodeActiveSession(fromChannelID, sessionKey, now)
	}
	toChannelID := 0
	if event == opencodeSessionEventMove {
		toChannelID = channel.Id
	}
	model.RecordOpencodeSessionLog(model.OpencodeSessionLogParams{
		UserID:          common.GetContextKeyInt(c, constant.ContextKeyUserId),
		Event:           event,
		SessionID:       newBinding.Session,
		ConversationKey: sessionKey,
		ChannelID:       channel.Id,
		FromChannelID:   fromChannelID,
		ToChannelID:     toChannelID,
		ModelName:       modelName,
		Group:           group,
	})
	if _, err := touchOpencodeActiveSession(channel.Id, sessionKey, now); err != nil {
		return nil, false, err
	}
	rememberOpencodeSessionSelection(c, channel.Id)
	setOpencodeSessionQueueRequired(c, false)
	return channel, true, nil
}

func shouldDeferOpencodeSessionAssignment(c *gin.Context, group, modelName string) bool {
	if isOpencodeSessionHardFallback(c) {
		return false
	}
	key := opencodeSessionWaitKey{group: group, model: modelName}
	opencodeSessionWaitMu.Lock()
	defer opencodeSessionWaitMu.Unlock()
	for _, entry := range opencodeSessionWaitQueue {
		if entry == nil || entry.key != key {
			continue
		}
		return entry.ctx != c
	}
	return false
}

func isOpencodeSessionWaitTurnLocked(entry *opencodeSessionWaitEntry) bool {
	for _, current := range opencodeSessionWaitQueue {
		if current == entry {
			return true
		}
		if current.key == entry.key {
			return false
		}
	}
	return false
}

func removeOpencodeSessionWaitEntryLocked(entry *opencodeSessionWaitEntry) {
	for index, current := range opencodeSessionWaitQueue {
		if current != entry {
			continue
		}
		copy(opencodeSessionWaitQueue[index:], opencodeSessionWaitQueue[index+1:])
		opencodeSessionWaitQueue = opencodeSessionWaitQueue[:len(opencodeSessionWaitQueue)-1]
		return
	}
}

func NotifyOpencodeSessionQueue() {
	opencodeSessionWaitMu.Lock()
	close(opencodeSessionWaitWake)
	opencodeSessionWaitWake = make(chan struct{})
	opencodeSessionWaitMu.Unlock()
}

func waitForOpencodeSessionCapacity(param *RetryParam, group string) (*model.Channel, string, error) {
	if param == nil || param.Ctx == nil {
		return nil, group, nil
	}
	if group == "" {
		group = queueGroupForParam(param)
	}
	if param.Ctx.GetBool(ginKeyOpencodeSessionWaitDone) {
		if selectedID := rememberedOpencodeSessionChannelID(param.Ctx); selectedID > 0 && !isOpencodeChannelAttempted(param.AttemptedChannelIDs, selectedID) {
			if channel, err := model.CacheGetChannel(selectedID); err == nil && channel != nil && !model.IsFallbackChannel(channel.Id) {
				setOpencodeSessionQueueRequired(param.Ctx, false)
				return channel, group, nil
			}
		}
	}
	param.Ctx.Set(ginKeyOpencodeSessionWaitDone, true)

	lookupParam := retryParamForGroup(param, group)
	lookupModel := param.ModelName
	if lookupParam != nil {
		lookupModel = lookupParam.ModelName
	}

	entry := &opencodeSessionWaitEntry{
		ctx: param.Ctx,
		key: opencodeSessionWaitKey{group: group, model: lookupModel},
	}
	opencodeSessionWaitMu.Lock()
	opencodeSessionWaitQueue = append(opencodeSessionWaitQueue, entry)
	opencodeSessionWaitMu.Unlock()

	start := time.Now()
	loggedWaiting := false
	var done <-chan struct{}
	if param.Ctx.Request != nil {
		done = param.Ctx.Request.Context().Done()
	}

	finish := func() {
		opencodeSessionWaitMu.Lock()
		removeOpencodeSessionWaitEntryLocked(entry)
		close(opencodeSessionWaitWake)
		opencodeSessionWaitWake = make(chan struct{})
		opencodeSessionWaitMu.Unlock()
	}

	for {
		if done != nil {
			select {
			case <-done:
				finish()
				return nil, group, param.Ctx.Request.Context().Err()
			default:
			}
		}

		timedOut := opencodeSessionQueueTimeout <= 0 || time.Since(start) >= opencodeSessionQueueTimeout
		if timedOut {
			setOpencodeSessionHardFallback(param.Ctx, true)
		}

		opencodeSessionWaitMu.Lock()
		turn := isOpencodeSessionWaitTurnLocked(entry)
		wake := opencodeSessionWaitWake
		opencodeSessionWaitMu.Unlock()

		if timedOut || turn {
			channel, queueRequired, err := SelectOpencodeSessionChannel(param.Ctx, group, lookupModel, param.RequestPath, nil, param.AttemptedChannelIDs)
			if err != nil {
				finish()
				return nil, group, err
			}
			if channel != nil && (!queueRequired || timedOut) {
				finish()
				result := "released"
				if timedOut {
					result = "timeout_hard_fallback"
				}
				if loggedWaiting || result == "timeout_hard_fallback" {
					recordOpencodeSessionQueueLog(param.Ctx, group, lookupModel, result, int(time.Since(start).Seconds()))
				}
				setOpencodeSessionQueueRequired(param.Ctx, false)
				return channel, group, nil
			}
			if !loggedWaiting {
				recordOpencodeSessionQueueLog(param.Ctx, group, lookupModel, "waiting", 0)
				loggedWaiting = true
			}
		}

		if timedOut {
			finish()
			return nil, group, nil
		}

		waitFor := time.Second
		remaining := opencodeSessionQueueTimeout - time.Since(start)
		if remaining > 0 && remaining < waitFor {
			waitFor = remaining
		}
		timer := time.NewTimer(waitFor)
		select {
		case <-done:
			if !timer.Stop() {
				<-timer.C
			}
			finish()
			if param.Ctx.Request != nil {
				return nil, group, param.Ctx.Request.Context().Err()
			}
			return nil, group, nil
		case <-wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func recordOpencodeSessionQueueLog(c *gin.Context, group, modelName, result string, waitSeconds int) {
	if c == nil {
		return
	}
	conversationKey := opencodeConversationKeyForModel(c, requestSessionIdentityModel(c, nil, modelName))
	sessionKey := opencodeSessionKey(conversationKey)
	sessionID := ""
	channelID := 0
	if binding, found, err := getOpencodeSessionBinding(sessionKey); err == nil && found {
		sessionID = binding.Session
		channelID = binding.ChannelID
	}
	model.RecordOpencodeSessionLog(model.OpencodeSessionLogParams{
		UserID:          common.GetContextKeyInt(c, constant.ContextKeyUserId),
		Event:           opencodeSessionEventQueue,
		SessionID:       sessionID,
		ConversationKey: sessionKey,
		ChannelID:       channelID,
		ModelName:       modelName,
		Group:           group,
		Result:          result,
		WaitSeconds:     waitSeconds,
	})
}

// markOpencodeSessionAffinity records the upstream session used by the current
// request and keeps lifecycle details in the admin-only common log metadata.
func markOpencodeSessionAffinity(c *gin.Context, channelID int, session string, cacheHit bool, event string) {
	if c == nil || channelID <= 0 {
		return
	}
	info := map[string]interface{}{
		"session_id":  session,
		"channel_id":  channelID,
		"cache_hit":   cacheHit,
		"affinitized": true,
	}
	if event != "" {
		info["event"] = event
	}
	c.Set(ginKeyOpencodeSessionAffinity, info)
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

// newOpencodeSessionID 生成随机的网关自管会话 ID，为 32 位十六进制随机串，不带业务前缀。
func newOpencodeSessionID() string {
	buf := make([]byte, 16)
	_, err := rand.Read(buf)
	if err == nil {
		return hex.EncodeToString(buf)
	}
	common.SysError(fmt.Sprintf("opencode session random generation failed: err=%v", err))
	counter := atomic.AddUint64(&opencodeSessionRandCounter, 1)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", time.Now().UnixNano(), counter)))
	return hex.EncodeToString(sum[:])[:32]
}

// deterministicOpencodeSession 无渠道上下文时的确定性会话 ID，永不空。
func deterministicOpencodeSession(identity string) string {
	sum := sha256.Sum256([]byte("opencode-session:" + identity))
	return hex.EncodeToString(sum[:])[:32]
}
