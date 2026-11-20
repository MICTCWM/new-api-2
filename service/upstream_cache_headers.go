package service

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

const (
	// MshCacheWrite5mHeader / MshCacheWrite1hHeader 是上游返回的缓存写入分档头。
	// 只有渠道未开启模拟缓存创建时，这两个值才作为创建量的权威来源；模拟缓存
	// 开启时本地 plan 是唯一口径，上游头会被忽略并删除，避免下游收到两套口径。
	MshCacheWrite5mHeader = "Msh-Usage-Cache-Write-Tokens-5m"
	MshCacheWrite1hHeader = "Msh-Usage-Cache-Write-Tokens-1h"
)

// CaptureUpstreamMshCacheHeaders 在拿到上游响应后统一读取 Msh 缓存写入分档头。
// 该函数只负责把上游真实值存入 RelayInfo，并在上游模式下提前写入下游响应头；
// usage / 日志 / 计费的合并由 ApplyUpstreamMshCacheUsage 完成。
func CaptureUpstreamMshCacheHeaders(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) {
	captureUpstreamMshCacheHeadersChecked(c, info, resp, isSimulatedCacheCreationEnabled)
}

// captureUpstreamMshCacheHeadersChecked 允许测试注入“模拟缓存是否开启”的判定，
// 避免单测依赖渠道数据库。
func captureUpstreamMshCacheHeadersChecked(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	resp *http.Response,
	simulatedEnabled func(*relaycommon.RelayInfo) bool,
) {
	if info == nil || resp == nil {
		return
	}
	// 同一个 RelayInfo 可能跨渠道重试；新响应到达时先清掉上一次的 Msh 状态，
	// 避免重试响应没有该头时沿用旧值。
	info.MshCacheWriteHeadersPresent = false
	info.MshCacheWrite5mTokens = 0
	info.MshCacheWrite1hTokens = 0
	if c != nil && c.Writer != nil {
		c.Writer.Header().Del(MshCacheWrite5mHeader)
		c.Writer.Header().Del(MshCacheWrite1hHeader)
	}
	if !isStationCacheTextFormat(info) {
		return
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return
	}

	if simulatedEnabled != nil && simulatedEnabled(info) {
		// 模拟缓存开启时本地 plan 是创建量的唯一主数据；上游头既不能进入
		// usage/计费，也不能经 IOCopyBytesGracefully 透传给下游。即使本次上游
		// 只声明了 expose 头、没有返回值，也一并清理掉。
		logger.LogDebug(c, "ignoring upstream Msh cache-write headers because simulated cache creation is enabled")
		resp.Header.Del(MshCacheWrite5mHeader)
		resp.Header.Del(MshCacheWrite1hHeader)
		removeExposeHeaderValues(resp.Header, []string{MshCacheWrite5mHeader, MshCacheWrite1hHeader})
		return
	}

	raw5m := strings.TrimSpace(resp.Header.Get(MshCacheWrite5mHeader))
	raw1h := strings.TrimSpace(resp.Header.Get(MshCacheWrite1hHeader))
	if raw5m == "" && raw1h == "" {
		return
	}

	info.MshCacheWriteHeadersPresent = true
	five, fiveOK := parseMshTokenHeader(c, MshCacheWrite5mHeader, raw5m)
	one, oneOK := parseMshTokenHeader(c, MshCacheWrite1hHeader, raw1h)
	if !fiveOK && !oneOK {
		// 两个头都非法时不能把 0 当成权威创建量，否则会用 0 覆盖上游 body
		// 里真实的聚合创建量，造成少收缓存创建费。
		info.MshCacheWriteHeadersPresent = false
		return
	}
	info.MshCacheWrite5mTokens = five
	info.MshCacheWrite1hTokens = one

	if c == nil || c.Writer == nil {
		return
	}
	if values := resp.Header.Values("Access-Control-Expose-Headers"); len(values) > 0 {
		mergeExposeHeaderValues(c.Writer.Header(), values)
	}
	if fiveOK {
		c.Writer.Header().Set(MshCacheWrite5mHeader, strconv.Itoa(five))
	}
	if oneOK {
		c.Writer.Header().Set(MshCacheWrite1hHeader, strconv.Itoa(one))
	}
	appendExposeHeaderValues(c.Writer.Header(), []string{MshCacheWrite5mHeader, MshCacheWrite1hHeader})
}

func parseMshTokenHeader(c *gin.Context, name, raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		logger.LogWarn(c, "invalid upstream "+name+" value: "+raw)
		return 0, false
	}
	return value, true
}

// ApplyUpstreamMshCacheUsage 将上游 Msh 头合并进通用 dto.Usage。返回 true 表示
// 本次使用了上游分档值；调用方可以据此决定是否重写响应 JSON 的 usage 字段。
// 模拟缓存 plan 已生效时绝不覆盖本地 plan。
func ApplyUpstreamMshCacheUsage(info *relaycommon.RelayInfo, usage *dto.Usage) bool {
	if !upstreamMshCacheAuthoritative(info) || usage == nil {
		return false
	}
	// Msh 头是上游 OpenAI 形态的权威创建量。这里显式标记 OpenAI 语义，
	// 避免仅因为补了 Claude 分档字段就被 isLegacyClaudeDerivedOpenAIUsage
	// 误判成旧版 Claude 派生 usage，从而既不扣减 prompt 又重复收创建费。
	// 原生 Claude 在调用本函数前已显式设置 anthropic，不会被这里覆盖。
	if usage.UsageSemantic == "" && usage.UsageSource == "" {
		usage.UsageSemantic = dto.BillingUsageSemanticOpenAI
	}
	total := info.MshCacheWrite5mTokens + info.MshCacheWrite1hTokens
	applyMshCacheFieldsToUsage(usage, info.MshCacheWrite5mTokens, info.MshCacheWrite1hTokens, total)

	if usage.BillingUsage != nil {
		if usage.BillingUsage.OpenAIUsage != nil {
			applyMshCacheFieldsToUsage(usage.BillingUsage.OpenAIUsage, info.MshCacheWrite5mTokens, info.MshCacheWrite1hTokens, total)
		}
		if usage.BillingUsage.ClaudeUsage != nil {
			applyMshCacheFieldsToClaudeUsage(usage.BillingUsage.ClaudeUsage, info.MshCacheWrite5mTokens, info.MshCacheWrite1hTokens, total)
		}
	}
	return true
}

// ApplyUpstreamMshCacheToClaudeUsage 将同一份上游分档值写入原生 Claude usage。
// 原生 Claude 的 input_tokens 不含缓存，因此这里只改 cache_creation 总量与分档，
// 不改 InputTokens / CacheReadInputTokens。
func ApplyUpstreamMshCacheToClaudeUsage(info *relaycommon.RelayInfo, usage *dto.ClaudeUsage) bool {
	if !upstreamMshCacheAuthoritative(info) || usage == nil {
		return false
	}
	total := info.MshCacheWrite5mTokens + info.MshCacheWrite1hTokens
	applyMshCacheFieldsToClaudeUsage(usage, info.MshCacheWrite5mTokens, info.MshCacheWrite1hTokens, total)
	return true
}

func upstreamMshCacheAuthoritative(info *relaycommon.RelayInfo) bool {
	if info == nil || !info.MshCacheWriteHeadersPresent {
		return false
	}
	// SimulatedCachePlan 一旦存在，说明本地规划已经接管创建量；上游头只做
	// 诊断，不能反向覆盖 plan。
	if info.SimulatedCachePlan != nil {
		return false
	}
	return !simulatedCacheCreationRuntimeEnabled(info)
}

func applyMshCacheFieldsToUsage(usage *dto.Usage, five, one, total int) {
	if usage == nil {
		return
	}
	usage.ClaudeCacheCreation5mTokens = five
	usage.ClaudeCacheCreation1hTokens = one
	usage.PromptTokensDetails.CachedCreationTokens = total
	usage.PromptTokensDetails.CacheWriteTokens = total
	if usage.InputTokensDetails == nil && usage.InputTokens > 0 {
		// Responses 的 usage 只暴露 input_tokens_details；上游给了分档头时
		// 补一个 details 对象，让客户端至少能看到 cache_write_tokens。
		usage.InputTokensDetails = &dto.InputTokenDetails{}
	}
	if usage.InputTokensDetails != nil {
		usage.InputTokensDetails.CachedCreationTokens = total
		usage.InputTokensDetails.CacheWriteTokens = total
	}
}

func applyMshCacheFieldsToClaudeUsage(usage *dto.ClaudeUsage, five, one, total int) {
	if usage == nil {
		return
	}
	usage.CacheCreationInputTokens = total
	usage.ClaudeCacheCreation5mTokens = five
	usage.ClaudeCacheCreation1hTokens = one
	if total > 0 {
		if usage.CacheCreation == nil {
			usage.CacheCreation = &dto.ClaudeCacheCreationUsage{}
		}
		usage.CacheCreation.Ephemeral5mInputTokens = five
		usage.CacheCreation.Ephemeral1hInputTokens = one
	} else {
		usage.CacheCreation = nil
	}
}

func appendExposeHeaderValues(dst http.Header, values []string) {
	if dst == nil {
		return
	}
	mergeExposeHeaderValues(dst, values)
}

// removeExposeHeaderValues 从 Access-Control-Expose-Headers 中剔除指定名字，
// 用于模拟缓存开启时连同上游的分档头一起清理，避免下游误以为存在这些头。
func removeExposeHeaderValues(dst http.Header, names []string) {
	if dst == nil || len(names) == 0 {
		return
	}
	remove := make(map[string]struct{}, len(names))
	for _, name := range names {
		remove[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	kept := make([]string, 0)
	for _, raw := range dst.Values("Access-Control-Expose-Headers") {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := remove[strings.ToLower(name)]; ok {
				continue
			}
			kept = append(kept, name)
		}
	}
	if len(kept) == 0 {
		dst.Del("Access-Control-Expose-Headers")
		return
	}
	dst.Set("Access-Control-Expose-Headers", strings.Join(kept, ", "))
}

// mergeExposeHeaderValues 合并 Access-Control-Expose-Headers，而不是直接覆盖，
// 避免 IOCopyBytesGracefully 复制上游头时把网关追加的 Msh 头暴露声明冲掉。
func mergeExposeHeaderValues(dst http.Header, values []string) {
	if dst == nil || len(values) == 0 {
		return
	}
	seen := make(map[string]struct{})
	all := make([]string, 0)
	for _, raw := range append(dst.Values("Access-Control-Expose-Headers"), values...) {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			all = append(all, name)
		}
	}
	if len(all) > 0 {
		dst.Set("Access-Control-Expose-Headers", strings.Join(all, ", "))
	}
}
