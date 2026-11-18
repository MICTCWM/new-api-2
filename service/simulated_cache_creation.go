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

// SimulatedCachePlan 与 relaycommon.SimulatedCachePlan 同构别名：类型定义放在
// relay/common 以避免 service ↔ relay 循环依赖，service 包对外保留同名类型。
type SimulatedCachePlan = relaycommon.SimulatedCachePlan

// 模拟创建缓存（Simulated Cache Creation）
//
// 当渠道开启「模拟创建缓存」开关时，网关在本地计费与用量展示层面模拟上游的
// prompt cache 创建/命中行为：以「渠道 + 用户 + 模型」为维度，按文本多段前缀
// 建链，TTL 内重复前缀计缓存命中（cache read），超出前缀的新增 token 计缓存创建。
//
// 本层只在本地改写 billingUsage 与响应 usage，不伪造上游响应；开启模拟缓存时，
// 请求侧会先读取客户端 TTL，再把 cache_control / prompt_cache_options 等 TTL
// 承载字段从出站请求中剥离，避免上游按自己的 TTL 再建一次缓存。上游已返回
// 真实 cache 相关值时整体跳过模拟，以真实值为准。
//
// 状态全部存放在 HybridCache 内，读-改-写按「Get → 计算 → SetWithTTL」覆盖写，
// 不引入全局锁；并发下偶尔丢失一次段写入在模拟层可接受。

const (
	simulatedCacheCreationNamespace      = "new-api:simulated_cache_creation:v1"
	simulatedCacheCreationMemoryCapacity = 50_000
	simulatedCacheCreationMaxSegments    = 32
	simulatedCacheCreationMaxBytes       = 1 << 20 // 1 MiB
	// 256 对齐 Kimi 自动缓存的门槛（上一请求 prompt >256 才缓存）；DeepSeek 为 64，
	// Claude/OpenAI 为 1024，Gemini 为 2048/4096。这里取主要对接的 Kimi 档。
	simulatedCacheCreationMinBillingInput = 256
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

// simulatedCacheCreationRuntimeEnabled 是请求内的快速判定：渠道开关打开，且
// 未禁用协议转换。协议转换关闭时，模拟缓存不参与请求剥离、响应注入与计费。
func simulatedCacheCreationRuntimeEnabled(relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo == nil || relayInfo.ChannelMeta == nil {
		return false
	}
	if !relayInfo.ChannelOtherSettings.SimulatedCacheCreationEnabled {
		return false
	}
	return !relayInfo.ChannelOtherSettings.DisableProtocolConversion
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
	if !simulatedCacheCreationEnabledFromJSON(channel.OtherSettings) {
		return false
	}
	return simulatedCacheCreationRuntimeEnabled(relayInfo)
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
		if toolsJSON := simulatedCacheToolsJSON(req.Tools); toolsJSON != "" {
			parts = append(parts, toolsJSON)
		}
		if instructions := responsesInstructionsToText(req.Instructions); instructions != "" {
			parts = append(parts, instructions)
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

// jsonValueHas1hCacheTTL 递归检查任意嵌套对象里的 cache_control /
// prompt_cache_options 是否声明 1h；用于 Claude tools 这类 any 字段。
func jsonValueHas1hCacheTTL(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"cache_control", "prompt_cache_options"} {
			if obj, ok := x[key].(map[string]any); ok {
				if ttl, _ := obj["ttl"].(string); ttl == simulatedCacheCreationTTLClass1h {
					return true
				}
			}
		}
		if s, ok := x["prompt_cache_retention"].(string); ok && s == "24h" {
			return true
		}
		for _, child := range x {
			if jsonValueHas1hCacheTTL(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if jsonValueHas1hCacheTTL(child) {
				return true
			}
		}
	}
	return false
}

func claudeToolsHave1hTTL(tools any) bool {
	if tools == nil {
		return false
	}
	raw, err := json.Marshal(tools)
	if err != nil || len(raw) == 0 {
		return false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return jsonValueHas1hCacheTTL(v)
}

// detectSimulatedCacheTTLClass 判定本次请求声明的缓存档位：显式 1 小时 → "1h"，
// 其余/缺省/无法解析 → "5m"。
func detectSimulatedCacheTTLClass(relayInfo *relaycommon.RelayInfo) string {
	if relayInfo == nil || relayInfo.Request == nil {
		return simulatedCacheCreationTTLClass5m
	}
	switch req := relayInfo.Request.(type) {
	case *dto.ClaudeRequest:
		if claudeToolsHave1hTTL(req.Tools) {
			return simulatedCacheCreationTTLClass1h
		}
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

// PlanSimulatedCacheCreation 是出站链路规划缓存创建的唯一入口：幂等（同一
// RelayInfo 已有 plan 时直接返回旧值，绝不二次推进缓存段），开关关闭 / 非文本格式 /
// 无消息 / billingInput <= 256 / 任一真实 cache 值 > 0 时返回 nil。命中与创建的
// 算法、clamp、段推进全部复用第 1 轮引擎；本函数是唯一推进缓存段的地方。
//
// billingInput：anthropic = upstream input_tokens；openai = prompt_tokens。
// realCached / realCreationTotal / realCreation5m / realCreation1h 为上游真实
// cache 分量，任一 > 0 整层跳过，以真实值为准。
func PlanSimulatedCacheCreation(relayInfo *relaycommon.RelayInfo, billingInput, realCached, realCreationTotal, realCreation5m, realCreation1h int) *relaycommon.SimulatedCachePlan {
	return planSimulatedCacheCreationChecked(relayInfo, isSimulatedCacheCreationEnabled, billingInput, realCached, realCreationTotal, realCreation5m, realCreation1h)
}

// planSimulatedCacheCreationChecked 是内部实现，允许测试注入渠道开关判定函数。
func planSimulatedCacheCreationChecked(relayInfo *relaycommon.RelayInfo, enabled func(*relaycommon.RelayInfo) bool,
	billingInput, realCached, realCreationTotal, realCreation5m, realCreation1h int) *relaycommon.SimulatedCachePlan {

	// 幂等：已有规划直接复用，不再推进缓存段、不重复计费。
	if relayInfo != nil && relayInfo.SimulatedCachePlan != nil {
		return relayInfo.SimulatedCachePlan
	}
	if !shouldApplySimulatedCacheCreation(relayInfo) {
		return nil
	}
	if enabled == nil || !enabled(relayInfo) {
		return nil
	}
	if billingInput <= simulatedCacheCreationMinBillingInput {
		return nil
	}
	// 真实上游值优先：任一 cache 字段 > 0 整层跳过。
	if realCached > 0 || realCreationTotal > 0 || realCreation5m > 0 || realCreation1h > 0 {
		return nil
	}

	text, ok := simulatedCachePrefixText(relayInfo)
	if !ok {
		return nil
	}
	// 超长 prompt（>1 MiB）跳过模拟，避免撑内存。
	if len(text) > simulatedCacheCreationMaxBytes {
		return nil
	}

	ttlClass := detectSimulatedCacheTTLClass(relayInfo)
	outcome := lookupAndUpdateSimulatedCache(relayInfo, text, ttlClass)

	// clamp 到 available = billingInput − 真实 cache 分量；hit+creation 超限时 hit 优先。
	available := billingInput - realCached - realCreationTotal
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
		return nil
	}

	plan := &relaycommon.SimulatedCachePlan{
		Applied:      true,
		BillingInput: billingInput,
		HitTokens:    hit,
		Creation5m:   creation5m,
		Creation1h:   creation1h,
		TTLClass:     ttlClass,
	}
	relayInfo.SimulatedCachePlan = plan
	return plan
}

// ApplyPlanToClaudeUsage 按 plan 对 ClaudeUsage 做绝对赋值（anthropic 语义）：
// InputTokens = max(0, BillingInput − hit − creation)；CacheReadInputTokens = hit；
// CacheCreationInputTokens = creation；cache_creation.ephemeral_5m/1h 为拆分值
// （两者之和 = 总量）。plan 或 usage 为 nil 时不改动。绝对赋值可重入、天然幂等。
func ApplyPlanToClaudeUsage(plan *relaycommon.SimulatedCachePlan, usage *dto.ClaudeUsage) {
	if plan == nil || usage == nil {
		return
	}
	creation := plan.CreationTotal()
	inputTokens := plan.BillingInput - plan.HitTokens - creation
	if inputTokens < 0 {
		inputTokens = 0
	}
	usage.InputTokens = inputTokens
	usage.CacheReadInputTokens = plan.HitTokens
	usage.CacheCreationInputTokens = creation
	usage.ClaudeCacheCreation5mTokens = plan.Creation5m
	usage.ClaudeCacheCreation1hTokens = plan.Creation1h
	if creation > 0 {
		if usage.CacheCreation == nil {
			usage.CacheCreation = &dto.ClaudeCacheCreationUsage{}
		}
		usage.CacheCreation.Ephemeral5mInputTokens = plan.Creation5m
		usage.CacheCreation.Ephemeral1hInputTokens = plan.Creation1h
	} else {
		usage.CacheCreation = nil
	}
}

// ApplyPlanToUsage 按 plan 对 dto.Usage 做绝对赋值，语义从 usage.UsageSemantic 判定
// （为空时按 openai 兜底）。plan 或 usage 为 nil 时不改动。绝对赋值可重入、天然幂等。
func ApplyPlanToUsage(plan *relaycommon.SimulatedCachePlan, usage *dto.Usage) {
	ApplyPlanToUsageSemantic(plan, usage, usageSemanticFromUsage(nil, usage))
}

// ApplyPlanToResponsesUsage 将同一份 plan 写回 Responses 语义的 usage：
// input_tokens 仍是含缓存的总量，input_tokens_details 补充 cached_tokens /
// cache_write_tokens；同时保留 prompt_tokens_details，供后续 text_quota 结算复用。
func ApplyPlanToResponsesUsage(plan *relaycommon.SimulatedCachePlan, usage *dto.Usage) {
	if plan == nil || usage == nil {
		return
	}
	ApplyPlanToUsageSemantic(plan, usage, dto.BillingUsageSemanticOpenAI)
	if usage.InputTokens == 0 {
		usage.InputTokens = plan.BillingInput
	}
	if usage.OutputTokens == 0 {
		usage.OutputTokens = usage.CompletionTokens
	}
	if usage.InputTokensDetails == nil {
		usage.InputTokensDetails = &dto.InputTokenDetails{}
	}
	usage.InputTokensDetails.CachedTokens = plan.HitTokens
	usage.InputTokensDetails.CacheWriteTokens = plan.CreationTotal()
	usage.InputTokensDetails.CachedCreationTokens = plan.CreationTotal()
	usage.PromptTokensDetails.CachedTokens = plan.HitTokens
	usage.PromptTokensDetails.CacheWriteTokens = plan.CreationTotal()
	usage.PromptTokensDetails.CachedCreationTokens = plan.CreationTotal()
	usage.ClaudeCacheCreation5mTokens = plan.Creation5m
	usage.ClaudeCacheCreation1hTokens = plan.Creation1h
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
}

// ApplyPlanToUsageSemantic 与 ApplyPlanToUsage 相同，但显式传入已解析的语义
// （anthropic / openai），供结算侧结合 relayInfo 格式判定空 semantic 的用法。
func ApplyPlanToUsageSemantic(plan *relaycommon.SimulatedCachePlan, usage *dto.Usage, semantic string) {
	if plan == nil || usage == nil {
		return
	}
	creation := plan.CreationTotal()
	semantic = strings.TrimSpace(semantic)
	if semantic == "" {
		semantic = dto.BillingUsageSemanticOpenAI
	}

	if semantic == dto.BillingUsageSemanticAnthropic {
		remaining := plan.BillingInput - plan.HitTokens - creation
		if remaining < 0 {
			remaining = 0
		}
		usage.PromptTokens = remaining
		usage.PromptTokensDetails.CachedTokens = plan.HitTokens
		usage.PromptTokensDetails.CachedCreationTokens = creation
	} else {
		// openai 语义：不动 PromptTokens，text_quota.go 自动扣减 base。
		usage.PromptTokensDetails.CachedTokens = plan.HitTokens
		usage.PromptTokensDetails.CacheWriteTokens = creation
		// 同步 CachedCreationTokens（两者取大，不重复）。
		if usage.PromptTokensDetails.CachedCreationTokens < creation {
			usage.PromptTokensDetails.CachedCreationTokens = creation
		}
		// 标记 openai 语义，避免本地注入的 5m/1h 字段被误判为 legacy Claude-derived usage
		// （legacy 检测器依赖空 source/semantic + Claude 拆分字段）。
		if usage.UsageSemantic == "" {
			usage.UsageSemantic = dto.BillingUsageSemanticOpenAI
		}
	}
	usage.ClaudeCacheCreation5mTokens = plan.Creation5m
	usage.ClaudeCacheCreation1hTokens = plan.Creation1h
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
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

	semantic := usageSemanticFromUsage(relayInfo, usage)

	// 已有出站阶段算好的 plan：直接同源写回 usage，不再读缓存、不再推进段。
	if relayInfo.SimulatedCachePlan != nil {
		plan := relayInfo.SimulatedCachePlan
		ApplyPlanToUsageSemantic(plan, usage, semantic)
		extraContent = append(extraContent, fmt.Sprintf(
			"模拟缓存创建 %d tokens（5m %d / 1h %d），模拟缓存命中 %d tokens",
			plan.CreationTotal(), plan.Creation5m, plan.Creation1h, plan.HitTokens,
		))
		return extraContent, usage, true
	}

	// 真实上游值优先：任一 cache 字段 > 0 整层跳过。
	realCached := usage.PromptTokensDetails.CachedTokens
	realCreationCached := usage.PromptTokensDetails.CachedCreationTokens
	realCreationTotal := usage.PromptTokensDetails.CacheCreationTokensTotal()
	realCreation5m := usage.ClaudeCacheCreation5mTokens
	realCreation1h := usage.ClaudeCacheCreation1hTokens
	if realCached > 0 || realCreationTotal > 0 || realCreation5m > 0 || realCreation1h > 0 {
		return extraContent, usage, false
	}

	var billingInput int
	if semantic == "anthropic" {
		// anthropic：PromptTokens 不含 cache read/creation，输入总量 = prompt + read + creation。
		billingInput = usage.PromptTokens + realCached + realCreationCached
	} else {
		// openai：prompt_tokens 已包含 cached 与 cache-write，直接取 PromptTokens。
		billingInput = usage.PromptTokens
	}

	plan := planSimulatedCacheCreationChecked(relayInfo, enabled, billingInput, realCached, realCreationTotal, realCreation5m, realCreation1h)
	if plan == nil {
		return extraContent, usage, false
	}

	ApplyPlanToUsageSemantic(plan, usage, semantic)
	extraContent = append(extraContent, fmt.Sprintf(
		"模拟缓存创建 %d tokens（5m %d / 1h %d），模拟缓存命中 %d tokens",
		plan.CreationTotal(), plan.Creation5m, plan.Creation1h, plan.HitTokens,
	))
	return extraContent, usage, true
}
