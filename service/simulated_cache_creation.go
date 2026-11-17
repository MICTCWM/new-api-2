package service

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cachex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/samber/hot"
)

// 模拟创建缓存（Simulated Cache Creation）
//
// 当渠道开启「模拟创建缓存」开关时，网关在本地计费与用量展示层面模拟上游的
// prompt cache 创建/命中行为：以「渠道 + 用户 + 模型」为维度，按文本多段前缀
// 建链，TTL 内重复前缀计缓存命中（cache read），超出前缀的新增 token 计缓存创建。
//
// 本层只改写本地 billingUsage 的字段，不修改发往上游的请求体，也不伪造上游响应；
// 上游已返回真实 cache 相关值时整体跳过模拟，以真实值为准。
//
// 状态全部存放在 HybridCache 内，读-改-写按「Get → 计算 → SetWithTTL」覆盖写，
// 不引入全局锁；并发下偶尔丢失一次段写入在模拟层可接受。

const (
	simulatedCacheCreationNamespace       = "new-api:simulated_cache_creation:v1"
	simulatedCacheCreationMemoryCapacity  = 50_000
	simulatedCacheCreationMaxSegments     = 32
	simulatedCacheCreationMaxBytes        = 1 << 20 // 1 MiB
	simulatedCacheCreationMinBillingInput = 1024
	simulatedCacheCreationTTLClass5m      = "5m"
	simulatedCacheCreationTTLClass1h      = "1h"
	simulatedCacheCreationTTL5m           = 5 * time.Minute
	simulatedCacheCreationTTL1h           = time.Hour
)

// SimulatedCacheSegment 是缓存条目里的一段前缀。只存段哈希与计量信息，不存原文，
// 避免大 prompt 撑爆内存。Chars 为本段字符长度，Tokens 为段尾累计本地分词 token。
type SimulatedCacheSegment struct {
	Chars     int    `json:"chars"`
	Tokens    int    `json:"tokens"`
	Hash      string `json:"hash"`
	TTLClass  string `json:"ttl_class"`
	ExpiresAt int64  `json:"expires_at"` // unix 毫秒
}

// SimulatedCacheEntry 是以「channelId:userId:model」为键的一条前缀链。
type SimulatedCacheEntry struct {
	Segments   []SimulatedCacheSegment `json:"segments"`
	TotalChars int                     `json:"total_chars"`
}

var (
	simulatedCacheCreationOnce  sync.Once
	simulatedCacheCreationCache *cachex.HybridCache[SimulatedCacheEntry]
	// simulatedCacheCreationNow 可在测试中注入时钟，验证 TTL 过期与命中续期。
	simulatedCacheCreationNow = time.Now
)

func getSimulatedCacheCreationCache() *cachex.HybridCache[SimulatedCacheEntry] {
	simulatedCacheCreationOnce.Do(func() {
		simulatedCacheCreationCache = cachex.NewHybridCache[SimulatedCacheEntry](cachex.HybridCacheConfig[SimulatedCacheEntry]{
			Namespace: cachex.Namespace(simulatedCacheCreationNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[SimulatedCacheEntry]{},
			Memory: func() *hot.HotCache[string, SimulatedCacheEntry] {
				return hot.NewHotCache[string, SimulatedCacheEntry](hot.LRU, simulatedCacheCreationMemoryCapacity).
					WithTTL(simulatedCacheCreationTTL1h).
					WithJanitor().
					Build()
			},
		})
	})
	return simulatedCacheCreationCache
}

// simulatedCacheCreationEnabledFromJSON 从渠道 OtherSettings 的 JSON 解析开关。
// 空字符串、非法 JSON 或缺省一律视为关闭。
func simulatedCacheCreationEnabledFromJSON(otherSettings string) bool {
	if otherSettings == "" {
		return false
	}
	var settings dto.ChannelOtherSettings
	if err := common.UnmarshalJsonStr(otherSettings, &settings); err != nil {
		return false
	}
	return settings.SimulatedCacheCreationEnabled
}

// isSimulatedCacheCreationEnabled 读取渠道开关，非法 JSON / 渠道缺失视为关闭。
func isSimulatedCacheCreationEnabled(relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo == nil {
		return false
	}
	channelId := relayInfo.GetChannelID()
	if channelId <= 0 {
		return false
	}
	channel, err := model.CacheGetChannel(channelId)
	if err != nil || channel == nil {
		return false
	}
	return simulatedCacheCreationEnabledFromJSON(channel.OtherSettings)
}

// simulatedCacheKey 构建缓存键：channelId:userId:model，由命名空间统一加前缀。
func simulatedCacheKey(channelId, userId int, modelName string) string {
	return strconv.Itoa(channelId) + ":" + strconv.Itoa(userId) + ":" + modelName
}

func simulatedCacheTTL(class string) time.Duration {
	if class == simulatedCacheCreationTTLClass1h {
		return simulatedCacheCreationTTL1h
	}
	return simulatedCacheCreationTTL5m
}

func simulatedCacheHashText(text string) string {
	return hex.EncodeToString(common.Sha256Raw([]byte(text)))
}

// claudeSystemText 抽取 Claude 请求 system 字段的纯文本（string 或文本块数组）。
func claudeSystemText(req *dto.ClaudeRequest) string {
	if req == nil || req.System == nil {
		return ""
	}
	if req.IsStringSystem() {
		return strings.TrimSpace(req.GetStringSystem())
	}
	var parts []string
	for _, m := range req.ParseSystem() {
		if m.Type == "text" && m.GetText() != "" {
			parts = append(parts, m.GetText())
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// simulatedCacheToolsJSON 把请求 tools 字段序列化为稳定 JSON 串；缺失/空返回 ""。
func simulatedCacheToolsJSON(tools any) string {
	if tools == nil {
		return ""
	}
	raw, err := common.Marshal(tools)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// simulatedCachePrefixText 抽取并归一化本次请求的前缀文本：
// toolsJSON + "\n" + systemText + "\n" + join(messages, "\n")，缺失项跳过。
// 消息抽取复用 extractStationCacheMessages（OpenAI / Claude / Gemini / Responses 四种 DTO）。
func simulatedCachePrefixText(relayInfo *relaycommon.RelayInfo) (string, bool) {
	msgs, ok := extractStationCacheMessages(relayInfo)
	if !ok || len(msgs) == 0 {
		return "", false
	}
	var parts []string
	switch req := relayInfo.Request.(type) {
	case *dto.ClaudeRequest:
		if toolsJSON := simulatedCacheToolsJSON(req.Tools); toolsJSON != "" {
			parts = append(parts, toolsJSON)
		}
		if sys := claudeSystemText(req); sys != "" {
			parts = append(parts, sys)
		}
	case *dto.GeneralOpenAIRequest:
		if toolsJSON := simulatedCacheToolsJSON(req.Tools); toolsJSON != "" {
			parts = append(parts, toolsJSON)
		}
	case *dto.OpenAIResponsesRequest:
		if len(req.Tools) > 0 {
			parts = append(parts, string(req.Tools))
		}
	}
	parts = append(parts, strings.Join(msgs, "\n"))
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

// rawCacheControlHas1hTTL 从 Anthropic 风格 cache_control raw JSON 中识别 ttl=="1h"。
// 无法解析的 raw JSON 一律按未声明处理（返回 false），不 panic。
func rawCacheControlHas1hTTL(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	ttl, _ := obj["ttl"].(string)
	return ttl == simulatedCacheCreationTTLClass1h
}

// promptCacheOptionsTTLIs1h 识别 OpenAI prompt_cache_options.ttl ∈ {1h,12h,24h}。
func promptCacheOptionsTTLIs1h(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	ttl, _ := obj["ttl"].(string)
	return ttl == "1h" || ttl == "12h" || ttl == "24h"
}

// promptCacheRetentionIs24h 识别 OpenAI prompt_cache_retention == "24h"。
func promptCacheRetentionIs24h(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == "24h"
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	if v, ok := obj["retention"].(string); ok {
		return v == "24h"
	}
	return false
}

// detectSimulatedCacheTTLClass 判定本次请求声明的缓存档位：显式 1 小时 → "1h"，
// 其余/缺省/无法解析 → "5m"。
func detectSimulatedCacheTTLClass(relayInfo *relaycommon.RelayInfo) string {
	if relayInfo == nil || relayInfo.Request == nil {
		return simulatedCacheCreationTTLClass5m
	}
	switch req := relayInfo.Request.(type) {
	case *dto.ClaudeRequest:
		if rawCacheControlHas1hTTL(req.CacheControl) {
			return simulatedCacheCreationTTLClass1h
		}
		if req.System != nil && !req.IsStringSystem() {
			for _, m := range req.ParseSystem() {
				if rawCacheControlHas1hTTL(m.CacheControl) {
					return simulatedCacheCreationTTLClass1h
				}
			}
		}
		for _, msg := range req.Messages {
			parts, _ := msg.ParseContent()
			for _, p := range parts {
				if rawCacheControlHas1hTTL(p.CacheControl) {
					return simulatedCacheCreationTTLClass1h
				}
			}
		}
	case *dto.GeneralOpenAIRequest:
		for _, m := range req.Messages {
			if rawCacheControlHas1hTTL(m.CacheControl) {
				return simulatedCacheCreationTTLClass1h
			}
			for _, p := range m.ParseContent() {
				if rawCacheControlHas1hTTL(p.CacheControl) {
					return simulatedCacheCreationTTLClass1h
				}
			}
		}
		if promptCacheOptionsTTLIs1h(req.PromptCacheOptions) {
			return simulatedCacheCreationTTLClass1h
		}
		if promptCacheRetentionIs24h(req.PromptCacheRetention) {
			return simulatedCacheCreationTTLClass1h
		}
	case *dto.OpenAIResponsesRequest:
		if promptCacheOptionsTTLIs1h(req.PromptCacheOptions) {
			return simulatedCacheCreationTTLClass1h
		}
		if promptCacheRetentionIs24h(req.PromptCacheRetention) {
			return simulatedCacheCreationTTLClass1h
		}
	}
	return simulatedCacheCreationTTLClass5m
}

// simulatedCacheOutcome 是一次本地前缀匹配的计量结果。
type simulatedCacheOutcome struct {
	HitTokens     int
	CreationTotal int
	Creation5m    int
	Creation1h    int
}

// lookupAndUpdateSimulatedCache 执行前缀匹配并把本次前缀链写回缓存。
// 命中按段校验哈希 + 未过期 + 连续；命中段按自身档位续期（不升级档位）；
// 新增段用 CountTextToken 增量累加 token；单键超 32 段整条丢弃重建。
func lookupAndUpdateSimulatedCache(relayInfo *relaycommon.RelayInfo, text, ttlClass string) simulatedCacheOutcome {
	var outcome simulatedCacheOutcome
	if relayInfo == nil || text == "" {
		return outcome
	}
	modelName := strings.TrimSpace(relayInfo.OriginModelName)
	if modelName == "" {
		return outcome
	}

	now := simulatedCacheCreationNow()
	nowMillis := now.UnixMilli()
	cache := getSimulatedCacheCreationCache()
	key := simulatedCacheKey(relayInfo.GetChannelID(), relayInfo.UserId, modelName)

	var existing SimulatedCacheEntry
	if entry, found, err := cache.Get(key); err == nil && found {
		existing = entry
	}

	// 逐段前缀匹配：offset 为当前已命中累计字符偏移，段不连续（过期 / 内容不符 / 越界）即停止。
	offset := 0
	hitTokens := 0
	matched := make([]SimulatedCacheSegment, 0, len(existing.Segments))
	for _, seg := range existing.Segments {
		if seg.Chars <= 0 || offset+seg.Chars > len(text) {
			break
		}
		chunk := text[offset : offset+seg.Chars]
		if simulatedCacheHashText(chunk) != seg.Hash {
			break
		}
		if seg.ExpiresAt <= nowMillis {
			break
		}
		// 命中续期：5m 段续 5m，1h 段续 1h，读取不升级档位。
		seg.ExpiresAt = nowMillis + simulatedCacheTTL(seg.TTLClass).Milliseconds()
		matched = append(matched, seg)
		offset += seg.Chars
		hitTokens = seg.Tokens
	}

	suffix := text[offset:]
	var segments []SimulatedCacheSegment
	if suffix == "" {
		segments = matched
	} else {
		suffixTokens := CountTextToken(suffix, modelName)
		newTokens := hitTokens + suffixTokens
		if newTokens < hitTokens { // 饱和钳零，防止回绕
			newTokens = hitTokens
		}
		newSeg := SimulatedCacheSegment{
			Chars:     len(suffix),
			Tokens:    newTokens,
			Hash:      simulatedCacheHashText(suffix),
			TTLClass:  ttlClass,
			ExpiresAt: nowMillis + simulatedCacheTTL(ttlClass).Milliseconds(),
		}
		segments = append(matched, newSeg)

		outcome.CreationTotal = newTokens - hitTokens
		if outcome.CreationTotal < 0 {
			outcome.CreationTotal = 0
		}
		if ttlClass == simulatedCacheCreationTTLClass1h {
			outcome.Creation1h = outcome.CreationTotal
		} else {
			outcome.Creation5m = outcome.CreationTotal
		}
	}

	// 有界策略：单键超过 32 段，整条丢弃，下一请求重新建链（不做无界增长）。
	if len(segments) > simulatedCacheCreationMaxSegments {
		fullTokens := CountTextToken(text, modelName)
		segments = []SimulatedCacheSegment{{
			Chars:     len(text),
			Tokens:    fullTokens,
			Hash:      simulatedCacheHashText(text),
			TTLClass:  ttlClass,
			ExpiresAt: nowMillis + simulatedCacheTTL(ttlClass).Milliseconds(),
		}}
		outcome.HitTokens = 0
		outcome.CreationTotal = fullTokens
		outcome.Creation5m = 0
		outcome.Creation1h = 0
		if ttlClass == simulatedCacheCreationTTLClass1h {
			outcome.Creation1h = fullTokens
		} else {
			outcome.Creation5m = fullTokens
		}
	} else {
		outcome.HitTokens = hitTokens
	}

	entryTTL := simulatedCacheCreationTTL5m
	for _, seg := range segments {
		if seg.TTLClass == simulatedCacheCreationTTLClass1h {
			entryTTL = simulatedCacheCreationTTL1h
			break
		}
	}
	_ = cache.SetWithTTL(key, SimulatedCacheEntry{
		Segments:   segments,
		TotalChars: len(text),
	}, entryTTL)

	return outcome
}

// shouldApplySimulatedCacheCreation 热路径短路：relayInfo 为空、Request 为 nil 或非文本格式直接返回 false。
func shouldApplySimulatedCacheCreation(relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo == nil || relayInfo.Request == nil {
		return false
	}
	return isStationCacheTextFormat(relayInfo)
}

// applySimulatedCacheCreation 是 PostTextConsumeQuota 内的模拟创建缓存钩子，
// 在 applyStationCache 之前调用。返回追加日志后的 extraContent、改写后的 usage，
// 以及模拟层是否生效（生效时调用方应跳过站内缓存注入，避免两套前缀折扣叠加）。
func applySimulatedCacheCreation(relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent []string) ([]string, *dto.Usage, bool) {
	return applySimulatedCacheCreationChecked(relayInfo, usage, extraContent, isSimulatedCacheCreationEnabled)
}

// applySimulatedCacheCreationChecked 是内部实现，允许测试注入渠道开关判定函数。
func applySimulatedCacheCreationChecked(relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent []string, enabled func(*relaycommon.RelayInfo) bool) ([]string, *dto.Usage, bool) {
	if !shouldApplySimulatedCacheCreation(relayInfo) {
		return extraContent, usage, false
	}
	if enabled == nil || !enabled(relayInfo) {
		return extraContent, usage, false
	}
	if usage == nil {
		usage = &dto.Usage{
			PromptTokens:     relayInfo.GetEstimatePromptTokens(),
			CompletionTokens: 0,
			TotalTokens:      relayInfo.GetEstimatePromptTokens(),
		}
	}

	// 真实上游值优先：任一 cache 字段 > 0 整层跳过。
	realCached := usage.PromptTokensDetails.CachedTokens
	realCreationCached := usage.PromptTokensDetails.CachedCreationTokens
	realCreationTotal := usage.PromptTokensDetails.CacheCreationTokensTotal()
	if realCached > 0 || realCreationTotal > 0 ||
		usage.ClaudeCacheCreation5mTokens > 0 || usage.ClaudeCacheCreation1hTokens > 0 {
		return extraContent, usage, false
	}

	text, ok := simulatedCachePrefixText(relayInfo)
	if !ok {
		return extraContent, usage, false
	}
	// 超长 prompt（>1 MiB）跳过模拟，避免撑内存。
	if len(text) > simulatedCacheCreationMaxBytes {
		return extraContent, usage, false
	}

	semantic := usageSemanticFromUsage(relayInfo, usage)
	var billingInput int
	var realCacheComponent int
	if semantic == "anthropic" {
		// anthropic：PromptTokens 不含 cache read/creation，输入总量 = prompt + read + creation。
		billingInput = usage.PromptTokens + realCached + realCreationCached
		realCacheComponent = realCached + realCreationCached
	} else {
		// openai：prompt_tokens 已包含 cached 与 cache-write，直接取 PromptTokens。
		billingInput = usage.PromptTokens
		realCacheComponent = realCached + realCreationTotal
	}
	if billingInput <= simulatedCacheCreationMinBillingInput {
		return extraContent, usage, false
	}

	ttlClass := detectSimulatedCacheTTLClass(relayInfo)
	outcome := lookupAndUpdateSimulatedCache(relayInfo, text, ttlClass)

	// clamp 到 available = billingInput − 真实 cache 分量；hit+creation 超限时 hit 优先。
	available := billingInput - realCacheComponent
	if available < 0 {
		available = 0
	}
	hit := outcome.HitTokens
	creation := outcome.CreationTotal
	if hit > available {
		hit = available
	}
	creationBudget := available - hit
	if creation > creationBudget {
		creation = creationBudget
	}

	creation5m, creation1h := outcome.Creation5m, outcome.Creation1h
	if creation5m+creation1h > creation {
		total := creation5m + creation1h
		if total > 0 {
			creation5m = creation5m * creation / total
			creation1h = creation - creation5m
		} else {
			creation5m, creation1h = 0, 0
		}
	}

	if hit <= 0 && creation <= 0 {
		return extraContent, usage, false
	}

	switch semantic {
	case "anthropic":
		// anthropic 语义：注入的 hit+creation 必须从 PromptTokens 扣掉，否则重复计费。
		remaining := usage.PromptTokens - hit - creation
		if remaining < 0 {
			remaining = 0
		}
		usage.PromptTokens = remaining
		usage.PromptTokensDetails.CachedTokens += hit
		usage.PromptTokensDetails.CachedCreationTokens += creation
		usage.ClaudeCacheCreation5mTokens += creation5m
		usage.ClaudeCacheCreation1hTokens += creation1h
	default:
		// openai 语义：不动 PromptTokens，text_quota.go 自动扣减 base。
		usage.PromptTokensDetails.CachedTokens += hit
		usage.PromptTokensDetails.CacheWriteTokens += creation
		// 同步 CachedCreationTokens（两者取大，不重复）。
		if usage.PromptTokensDetails.CachedCreationTokens < usage.PromptTokensDetails.CacheWriteTokens {
			usage.PromptTokensDetails.CachedCreationTokens = usage.PromptTokensDetails.CacheWriteTokens
		}
		usage.ClaudeCacheCreation5mTokens += creation5m
		usage.ClaudeCacheCreation1hTokens += creation1h
		// 标记 openai 语义，避免本地注入的 5m/1h 字段被误判为 legacy Claude-derived usage
		// （legacy 检测器依赖空 source/semantic + Claude 拆分字段）。
		if usage.UsageSemantic == "" {
			usage.UsageSemantic = dto.BillingUsageSemanticOpenAI
		}
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	extraContent = append(extraContent, fmt.Sprintf(
		"模拟缓存创建 %d tokens（5m %d / 1h %d），模拟缓存命中 %d tokens",
		creation, creation5m, creation1h, hit,
	))
	return extraContent, usage, true
}
