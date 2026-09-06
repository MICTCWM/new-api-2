package service

import (
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
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/samber/hot"
)

// 站内缓存（Station Cache）
//
// 当上游渠道不提供 prompt 缓存时，网关自己做站内缓存：以「用户+模型」为维度，把最近一次
// 请求的输入消息序列缓存起来。下一次请求到来时，从第 1 条消息开始做最长连续匹配（消息级
// 前缀匹配），命中部分的 token 数计入结算时的 cached tokens，从而享受现有 CacheRatio 折扣价
// 计费。此功能独立于渠道亲和开关，只要路由到的渠道开启了「站内缓存」开关即生效。
//
// 计费折扣复用现有 CacheRatio：命中 token 合并进 Usage.PromptTokensDetails.CachedTokens，
// 即可被 calculateTextQuotaSummary 自动打折，无需改动计费核心逻辑。

const (
	stationCacheNamespace = "new-api:station_cache:v1"
	// stationCacheDefaultTTL 缓存默认存活时长（5 分钟），仅用于限制缓存占用。
	// 与 prompt cache 常见的生效窗口一致：只约束缓存条目在热存/双通道中的占用时长。
	stationCacheDefaultTTL = 5 * time.Minute
	// stationCacheDefaultCapacity 内存热缓存（Redis 关闭时）的 LRU 容量上限。
	stationCacheDefaultCapacity = 100_000
)

// StationCacheEntry 是站内缓存的单条记录，以「userId:model」为 key。
// MessageHashes 保存逐条消息的规范化哈希，CumulativeTokens 保存前 j 条消息的累计 token 数，
// 当连续命中前 j 条时，命中 token 数 = CumulativeTokens[j-1]。
type StationCacheEntry struct {
	MessageHashes    []string `json:"message_hashes"`
	CumulativeTokens []int    `json:"cumulative_tokens"`
}

var (
	stationCacheOnce sync.Once
	stationCache     *cachex.HybridCache[StationCacheEntry]
)

func getStationCache() *cachex.HybridCache[StationCacheEntry] {
	stationCacheOnce.Do(func() {
		stationCache = cachex.NewHybridCache[StationCacheEntry](cachex.HybridCacheConfig[StationCacheEntry]{
			Namespace: cachex.Namespace(stationCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[StationCacheEntry]{},
			Memory: func() *hot.HotCache[string, StationCacheEntry] {
				return hot.NewHotCache[string, StationCacheEntry](hot.LRU, stationCacheDefaultCapacity).
					WithTTL(stationCacheDefaultTTL).
					WithJanitor().
					Build()
			},
		})
	})
	return stationCache
}

// stationCacheEnabledFromJSON 从渠道 OtherSettings 的 JSON 字符串解析站内缓存开关。
// 空字符串、非法 JSON 或未显式开启（缺省 false）一律视为未启用。
func stationCacheEnabledFromJSON(otherSettings string) bool {
	if otherSettings == "" {
		return false
	}
	var settings dto.ChannelOtherSettings
	if err := common.UnmarshalJsonStr(otherSettings, &settings); err != nil {
		return false
	}
	return settings.StationCacheEnabled
}

// isStationCacheEnabled 返回路由到的渠道是否开启了站内缓存开关。
// 从渠道的 OtherSettings（settings 字段，JSON 存储）解析，非法 JSON 视为关闭。
func isStationCacheEnabled(relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo == nil || relayInfo.ChannelId <= 0 {
		return false
	}
	channel, err := model.CacheGetChannel(relayInfo.ChannelId)
	if err != nil || channel == nil {
		return false
	}
	return stationCacheEnabledFromJSON(channel.OtherSettings)
}

// stationCacheModelName 返回缓存 key 中使用的模型字段。
// 使用 relayInfo.OriginModelName（已归一化的请求原始模型名）：网关在 InitChannelMeta 里会把
// 请求 DTO 的 Model 统一设为 OriginModelName，因此相同语义的模型会映射到同一个 key，保证
// 缓存维度稳定一致。RelayInfo 本身没有独立的 Model 字段，这里复用原始模型名。
func stationCacheModelName(relayInfo *relaycommon.RelayInfo) string {
	if relayInfo == nil {
		return ""
	}
	return strings.TrimSpace(relayInfo.OriginModelName)
}

// openaiMessageToText 将一条 OpenAI chat 消息规范化为文本（含 role、文本内容与工具名），
// 用于哈希比对与 token 估算。内容兼容 string / []any / []MediaContent 三种形态。
// 若消息没有任何实际内容（仅带 role），视为空消息返回 ""。
func openaiMessageToText(m dto.Message) string {
	var body strings.Builder
	writeOpenAIContentText(&body, m)
	if m.Name != nil && *m.Name != "" {
		body.WriteString("name:")
		body.WriteString(*m.Name)
		body.WriteString("\n")
	}
	if toolCalls := m.ParseToolCalls(); len(toolCalls) > 0 {
		for _, call := range toolCalls {
			if call.Function.Name != "" {
				body.WriteString("tool:")
				body.WriteString(call.Function.Name)
				body.WriteString("\n")
			}
			if call.Function.Arguments != "" {
				body.WriteString(call.Function.Arguments)
				body.WriteString("\n")
			}
		}
	}
	content := strings.TrimSpace(body.String())
	if content == "" {
		return ""
	}
	if m.Role == "" {
		return content
	}
	return m.Role + "\n" + content
}

// writeOpenAIContentText 提取 OpenAI 消息的文本内容部分。
func writeOpenAIContentText(b *strings.Builder, m dto.Message) {
	switch content := m.Content.(type) {
	case nil:
		return
	case string:
		if content != "" {
			b.WriteString(content)
			b.WriteString("\n")
		}
		return
	case []dto.MediaContent:
		writeOpenAIMediaText(b, content)
		return
	}
	writeOpenAIMediaText(b, m.ParseContent())
}

// writeOpenAIMediaText 从媒体内容列表里拼出纯文本部分。
func writeOpenAIMediaText(b *strings.Builder, parts []dto.MediaContent) {
	for _, p := range parts {
		if p.Type == dto.ContentTypeText && p.Text != "" {
			b.WriteString(p.Text)
			b.WriteString("\n")
		}
	}
}

// claudeMessageToText 将一条 Claude 消息规范化为文本（含 role、文本内容与工具名）。
// 若消息没有任何实际内容（仅带 role），视为空消息返回 ""。
func claudeMessageToText(m dto.ClaudeMessage) string {
	var body strings.Builder
	if m.IsStringContent() {
		if s := m.GetStringContent(); s != "" {
			body.WriteString(s)
		}
	} else {
		parts, _ := m.ParseContent()
		for _, p := range parts {
			switch p.Type {
			case "text":
				if t := p.GetText(); t != "" {
					body.WriteString(t)
					body.WriteString("\n")
				}
			case "tool_use":
				if p.Name != "" {
					body.WriteString("tool:")
					body.WriteString(p.Name)
					body.WriteString("\n")
				}
			case "tool_result":
				if p.Content != nil {
					if bs, err := common.Marshal(p.Content); err == nil {
						body.Write(bs)
						body.WriteString("\n")
					}
				}
			case "thinking":
				if p.Thinking != nil {
					body.WriteString(*p.Thinking)
					body.WriteString("\n")
				}
			}
		}
	}
	content := strings.TrimSpace(body.String())
	if content == "" {
		return ""
	}
	if m.Role == "" {
		return content
	}
	return m.Role + "\n" + content
}

// geminiContentToText 将一条 Gemini content 规范化为文本（含 role、文本内容与工具名/文件引用）。
// 若没有任何实际内容（仅带 role），视为空消息返回 ""。
func geminiContentToText(c dto.GeminiChatContent) string {
	var body strings.Builder
	for _, p := range c.Parts {
		if p.Text != "" {
			body.WriteString(p.Text)
			body.WriteString("\n")
		}
		if p.FunctionCall != nil && p.FunctionCall.FunctionName != "" {
			body.WriteString("tool:")
			body.WriteString(p.FunctionCall.FunctionName)
			body.WriteString("\n")
		}
		if p.FileData != nil && p.FileData.FileUri != "" {
			body.WriteString("file:")
			body.WriteString(p.FileData.FileUri)
			body.WriteString("\n")
		}
	}
	content := strings.TrimSpace(body.String())
	if content == "" {
		return ""
	}
	if c.Role == "" {
		return content
	}
	return c.Role + "\n" + content
}

// responsesInputToText 将一条 Responses input item 规范化为文本（含 role 与文本/工具内容）。
// 若没有任何实际内容，视为空消息返回 ""。
func responsesInputToText(in dto.Input) string {
	var body strings.Builder
	if len(in.Content) > 0 {
		switch common.GetJsonType(in.Content) {
		case "string":
			var s string
			if err := common.Unmarshal(in.Content, &s); err == nil && s != "" {
				body.WriteString(s)
				body.WriteString("\n")
			}
		case "array":
			var arr []any
			if err := common.Unmarshal(in.Content, &arr); err == nil {
				for _, item := range arr {
					obj, ok := item.(map[string]any)
					if !ok {
						continue
					}
					typ, _ := obj["type"].(string)
					switch typ {
					case "input_text", "output_text", "text":
						if t, ok := obj["text"].(string); ok && t != "" {
							body.WriteString(t)
							body.WriteString("\n")
						}
					case "function_call":
						if name, ok := obj["name"].(string); ok && name != "" {
							body.WriteString("tool:")
							body.WriteString(name)
							body.WriteString("\n")
						}
					}
				}
			}
		}
	}
	content := strings.TrimSpace(body.String())
	if content == "" {
		return ""
	}
	role := in.Role
	if role == "" {
		role = "user"
	}
	return role + "\n" + content
}

// responsesToMessages 将 Responses 请求的 Input 拆成逐条消息的规范化文本。
// Input 可以是字符串（视为单条 user 消息）或对象数组（每条顶层级对象视为一条消息）。
func responsesToMessages(req *dto.OpenAIResponsesRequest) []string {
	if req == nil || len(req.Input) == 0 {
		return nil
	}
	switch common.GetJsonType(req.Input) {
	case "string":
		var s string
		if err := common.Unmarshal(req.Input, &s); err != nil {
			return nil
		}
		s = strings.TrimSpace("user\n" + s)
		if s == "" {
			return nil
		}
		return []string{s}
	case "array":
		var inputs []dto.Input
		if err := common.Unmarshal(req.Input, &inputs); err != nil {
			return nil
		}
		msgs := make([]string, 0, len(inputs))
		for _, in := range inputs {
			txt := responsesInputToText(in)
			if txt == "" {
				return nil
			}
			msgs = append(msgs, txt)
		}
		return msgs
	default:
		return nil
	}
}

// extractStationCacheMessages 从四种文本请求 DTO 中提取消息规范化文本列表。
// 不支持的消息格式、提取失败或消息为空时返回 (nil, false)。
func extractStationCacheMessages(relayInfo *relaycommon.RelayInfo) ([]string, bool) {
	if relayInfo == nil || relayInfo.Request == nil {
		return nil, false
	}
	var msgs []string
	switch req := relayInfo.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		for _, m := range req.Messages {
			txt := openaiMessageToText(m)
			if txt == "" {
				return nil, false
			}
			msgs = append(msgs, txt)
		}
	case *dto.ClaudeRequest:
		for _, m := range req.Messages {
			txt := claudeMessageToText(m)
			if txt == "" {
				return nil, false
			}
			msgs = append(msgs, txt)
		}
	case *dto.GeminiChatRequest:
		for _, c := range req.Contents {
			txt := geminiContentToText(c)
			if txt == "" {
				return nil, false
			}
			msgs = append(msgs, txt)
		}
	case *dto.OpenAIResponsesRequest:
		msgs = responsesToMessages(req)
	default:
		// embedding / rerank / 图片 / 音频等无消息结构，直接跳过不启用。
		return nil, false
	}
	if len(msgs) == 0 {
		return nil, false
	}
	return msgs, true
}

// stationCacheHash 计算单条消息规范化文本的哈希（SHA1）。
func stationCacheHash(s string) string {
	return common.Sha1([]byte(s))
}

// stationCacheKey 构建缓存 key：{userId}:{model}，由 HybridCache 的命名空间统一加前缀。
func stationCacheKey(userId int, model string) string {
	return strconv.Itoa(userId) + ":" + model
}

// lookupAndUpdateStationCache 处理一次请求的站内缓存：构建本次消息哈希与累计 token，
// 从第 1 条消息开始做最长连续前缀匹配，命中 token 数 clamp 后返回；随后总是把本次输入
// 前缀序列写回缓存（"最近一次请求"即最新缓存态）。
func lookupAndUpdateStationCache(relayInfo *relaycommon.RelayInfo, messages []string) int {
	if relayInfo == nil || len(messages) == 0 {
		return 0
	}
	modelName := stationCacheModelName(relayInfo)
	if modelName == "" {
		return 0
	}

	// 本次输入序列的哈希与累计 token。
	hashes := make([]string, len(messages))
	cumulative := make([]int, len(messages))
	inputTotal := 0
	for i, msg := range messages {
		hashes[i] = stationCacheHash(msg)
		inputTotal += CountTextToken(msg, modelName)
		if inputTotal < 0 {
			// 累计 token 来自用户可控输入，饱和钳到 0，防止回绕。
			inputTotal = 0
		}
		cumulative[i] = inputTotal
	}

	cache := getStationCache()
	key := stationCacheKey(relayInfo.UserId, modelName)

	// 最长连续前缀匹配（从第 1 条消息开始）。
	var cached StationCacheEntry
	hitLen := 0
	if entry, found, err := cache.Get(key); err == nil && found {
		cached = entry
		if len(cached.MessageHashes) == len(cached.CumulativeTokens) && len(cached.MessageHashes) > 0 {
			for i := 0; i < len(messages) && i < len(cached.MessageHashes); i++ {
				if cached.MessageHashes[i] != hashes[i] {
					break
				}
				hitLen = i + 1
			}
		}
	}

	// 命中 token 数 = 缓存累计 token，但必须 clamp 在本批全部输入 token 之内且非负。
	hitTokens := 0
	if hitLen > 0 {
		hitTokens = cached.CumulativeTokens[hitLen-1]
	}
	if hitTokens > inputTotal {
		hitTokens = inputTotal
	}
	if hitTokens < 0 {
		hitTokens = 0
	}

	// 无论是否命中，都写入本次输入前缀序列作为最新缓存态。
	_ = cache.SetWithTTL(key, StationCacheEntry{
		MessageHashes:    hashes,
		CumulativeTokens: cumulative,
	}, stationCacheDefaultTTL)

	return hitTokens
}

// mergeStationCacheCached 将站内缓存命中 token 与上游已报告的 cached tokens 取较大者，
// 避免对同一段前缀重复计费。命中值应已 clamp，此处只做取大不直接合并。
func mergeStationCacheCached(upstreamCached, hitTokens int) int {
	if hitTokens > upstreamCached {
		return hitTokens
	}
	return upstreamCached
}

// stationCacheTextFormats 站内缓存支持的文本 relay 格式集合，
// 与 extractStationCacheMessages 支持的四种请求 DTO 一一对应。
func stationCacheTextFormats() map[types.RelayFormat]struct{} {
	return map[types.RelayFormat]struct{}{
		types.RelayFormatOpenAI:          {},
		types.RelayFormatClaude:          {},
		types.RelayFormatGemini:          {},
		types.RelayFormatOpenAIResponses: {},
	}
}

// isStationCacheTextFormat 判断 relay 格式是否属于站内缓存支持的文本格式。
func isStationCacheTextFormat(relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo == nil {
		return false
	}
	_, ok := stationCacheTextFormats()[relayInfo.GetFinalRequestRelayFormat()]
	return ok
}

// shouldApplyStationCache 热路径快速短路：
// relayInfo 为空、Request 为 nil（无法提取消息）或非文本 relay 格式（embedding / rerank / 图片 / 音频等）时
// 直接返回 false，让 applyStationCache 在读取渠道开关之前就退出，避免绝大多数无消息结构的请求走渠道读取。
func shouldApplyStationCache(relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo == nil || relayInfo.Request == nil {
		return false
	}
	return isStationCacheTextFormat(relayInfo)
}

// applyStationCache 是在 PostTextConsumeQuota 注入的站内缓存钩子：
// 若为文本格式、渠道开启开关且能提取出消息，计算命中并合并进 billingUsage.PromptTokensDetails.CachedTokens，
// 使后续 calculateTextQuotaSummary 按现有 CacheRatio 自动打折。返回追加了命中日志的 extraContent 与合并后的 usage。
func applyStationCache(relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent []string) ([]string, *dto.Usage) {
	return applyStationCacheChecked(relayInfo, usage, extraContent, isStationCacheEnabled)
}

// applyStationCacheChecked 是 applyStationCache 的内部实现，允许测试注入渠道开关判定函数。
func applyStationCacheChecked(relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent []string, stationCacheEnabled func(*relaycommon.RelayInfo) bool) ([]string, *dto.Usage) {
	// 热路径短路：绝大多数请求无消息结构（非文本格式），先于渠道开关读取直接返回。
	if !shouldApplyStationCache(relayInfo) {
		return extraContent, usage
	}
	if !stationCacheEnabled(relayInfo) {
		return extraContent, usage
	}
	// 上游无计费信息时（usage 为 nil），构造一个默认 usage，让命中折扣仍能生效。
	if usage == nil {
		usage = &dto.Usage{
			PromptTokens:     relayInfo.GetEstimatePromptTokens(),
			CompletionTokens: 0,
			TotalTokens:      relayInfo.GetEstimatePromptTokens(),
		}
	}
	// Claude 语义渠道（usageSemantic == "anthropic"）不能注入站内缓存命中：calculateTextQuotaSummary
	// 对 Claude 语义不会把 cache tokens 从全价里扣减（text_quota.go:308-313），而是把 cache*cacheRatio
	// 完整加在全价之上，此时注入命中反而会加价。判断口径与 text_quota.go 内部 usageSemanticFromUsage 保持
	// 一致，避免两处口径漂移。
	if usageSemanticFromUsage(relayInfo, usage) == "anthropic" {
		return extraContent, usage
	}
	msgs, ok := extractStationCacheMessages(relayInfo)
	if !ok || len(msgs) == 0 {
		return extraContent, usage
	}
	hit := lookupAndUpdateStationCache(relayInfo, msgs)
	// 命中值 clamp 到 usage.PromptTokens（上游报告或本地估算）：本地估算可能大于上游口径，若不 clamp，
	// summary.CacheTokens > PromptTokens 时 base 会被 text_quota.go 钳零，而 cache*cacheRatio 仍按全价入账，
	// 造成超额计费。注意 query 由默认 usage（EstimatePromptTokens）填充时同样适用此 clamp。
	if hit > usage.PromptTokens {
		hit = usage.PromptTokens
	}
	if hit < 0 {
		hit = 0
	}
	if hit > 0 {
		usage.PromptTokensDetails.CachedTokens = mergeStationCacheCached(usage.PromptTokensDetails.CachedTokens, hit)
		extraContent = append(extraContent, fmt.Sprintf("站内缓存命中 %d tokens", hit))
	}
	return extraContent, usage
}
