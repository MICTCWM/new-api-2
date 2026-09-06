package service

import (
	"errors"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// ErrFallbackExhausted 表示主渠道配置的兜底序列已全部失败。
// 主循环据此返回主渠道的首个错误，而非继续参与全局随机重试。
var ErrFallbackExhausted = errors.New("fallback sequence exhausted")

type RetryParam struct {
	Ctx                 *gin.Context
	TokenGroup          string
	ModelName           string
	RequestPath         string
	Retry               *int
	resetNextTry        bool
	AttemptedChannelIDs map[int]struct{}
	OpencodePool        bool
	// OpencodeIncludeRPMFull is used by the distributor's initial selection so
	// requests can reach the relay queue when all eligible channels are busy.
	OpencodeIncludeRPMFull      bool
	OpencodeConcurrencyReserved bool
	// OpencodeRPMReserved is retained for source compatibility with older
	// callers. New routing code uses OpencodeConcurrencyReserved.
	OpencodeRPMReserved         bool
	ExcludeCodeProviderChannels bool

	// ── 兜底序列（Fallback Sequence）──
	// FallbackQueue 为当前主渠道解析出的有序兜底渠道ID列表（已去重/过滤已尝试渠道）。
	FallbackQueue []int
	// FallbackQueueIndex 指向下一个待尝试的兜底索引。
	FallbackQueueIndex int
	// InFallback 表示当前是否处于兜底模式（getChannel 据此直接从队列取渠道，绕过池选择）。
	InFallback bool
	// FallbackStarted 标记兜底是否已启动，防止兜底渠道失败时递归触发其自身兜底序列。
	FallbackStarted bool
	// FirstError 记录主渠道的首个错误，兜底全部失败后返回给用户。
	FirstError *types.NewAPIError
}

var codeProviderLoadSelectionCursor atomic.Uint64

func (p *RetryParam) GetRetry() int {
	if p.Retry == nil {
		return 0
	}
	return *p.Retry
}

func (p *RetryParam) SetRetry(retry int) {
	p.Retry = &retry
}

func (p *RetryParam) IncreaseRetry() {
	if p.resetNextTry {
		p.resetNextTry = false
		return
	}
	if p.Retry == nil {
		p.Retry = new(int)
	}
	*p.Retry++
}

func (p *RetryParam) ResetRetryNextTry() {
	p.resetNextTry = true
}

// StartFallback 根据主渠道的兜底序列初始化兜底模式。
// 执行去重、剔除已在 AttemptedChannelIDs 中的渠道，并重置索引、置 InFallback=true、
// FallbackStarted=true（防止兜底渠道自身失败时再次递归启动其兜底序列）。
func (p *RetryParam) StartFallback(ids []int) {
	seen := make(map[int]struct{}, len(ids))
	queue := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		if p.AttemptedChannelIDs != nil {
			if _, attempted := p.AttemptedChannelIDs[id]; attempted {
				continue
			}
		}
		seen[id] = struct{}{}
		queue = append(queue, id)
	}
	p.FallbackQueue = queue
	p.FallbackQueueIndex = 0
	p.InFallback = true
	p.FallbackStarted = true
}

// HasNextFallback 返回兜底序列中是否还有待尝试的渠道。
func (p *RetryParam) HasNextFallback() bool {
	return p.FallbackQueueIndex < len(p.FallbackQueue)
}

// NextFallbackID 取出下一个待尝试的兜底渠道ID并推进索引。
func (p *RetryParam) NextFallbackID() (int, bool) {
	if !p.HasNextFallback() {
		return 0, false
	}
	id := p.FallbackQueue[p.FallbackQueueIndex]
	p.FallbackQueueIndex++
	return id, true
}

// CacheGetRandomSatisfiedChannel tries to get a random channel that satisfies the requirements.
// 尝试获取一个满足要求的随机渠道。
//
// For "auto" tokenGroup with cross-group Retry enabled:
// 对于启用了跨分组重试的 "auto" tokenGroup：
//
//   - Each group will exhaust all its priorities before moving to the next group.
//     每个分组会用完所有优先级后才会切换到下一个分组。
//
//   - Uses ContextKeyAutoGroupIndex to track current group index.
//     使用 ContextKeyAutoGroupIndex 跟踪当前分组索引。
//
//   - Uses ContextKeyAutoGroupRetryIndex to track the global Retry count when current group started.
//     使用 ContextKeyAutoGroupRetryIndex 跟踪当前分组开始时的全局重试次数。
//
//   - priorityRetry = Retry - startRetryIndex, represents the priority level within current group.
//     priorityRetry = Retry - startRetryIndex，表示当前分组内的优先级级别。
//
//   - When GetRandomSatisfiedChannel returns nil (priorities exhausted), moves to next group.
//     当 GetRandomSatisfiedChannel 返回 nil（优先级用完）时，切换到下一个分组。
//
// Example flow (2 groups, each with 2 priorities, RetryTimes=3):
// 示例流程（2个分组，每个有2个优先级，RetryTimes=3）：
//
//	Retry=0: GroupA, priority0 (startRetryIndex=0, priorityRetry=0)
//	         分组A, 优先级0
//
//	Retry=1: GroupA, priority1 (startRetryIndex=0, priorityRetry=1)
//	         分组A, 优先级1
//
//	Retry=2: GroupA exhausted → GroupB, priority0 (startRetryIndex=2, priorityRetry=0)
//	         分组A用完 → 分组B, 优先级0
//
//	Retry=3: GroupB, priority1 (startRetryIndex=2, priorityRetry=1)
//	         分组B, 优先级1
func selectWeightedOpencodeCandidate(candidates []*model.Channel) *model.Channel {
	if len(candidates) == 0 {
		return nil
	}
	highestPriority := candidates[0].GetPriority()
	for _, candidate := range candidates[1:] {
		if candidate.GetPriority() > highestPriority {
			highestPriority = candidate.GetPriority()
		}
	}
	priorityCandidates := make([]*model.Channel, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.GetPriority() == highestPriority {
			priorityCandidates = append(priorityCandidates, candidate)
		}
	}
	if len(priorityCandidates) == 1 {
		return priorityCandidates[0]
	}
	totalWeight := 0
	for _, candidate := range priorityCandidates {
		if weight := candidate.GetWeight(); weight > 0 {
			totalWeight += weight
		}
	}
	if totalWeight == 0 {
		return priorityCandidates[rand.Intn(len(priorityCandidates))]
	}
	randomWeight := rand.Intn(totalWeight)
	for _, candidate := range priorityCandidates {
		weight := candidate.GetWeight()
		if weight <= 0 {
			continue
		}
		randomWeight -= weight
		if randomWeight < 0 {
			return candidate
		}
	}
	return priorityCandidates[len(priorityCandidates)-1]
}

func selectLeastLoadedCodeProviderCandidate(candidates []*model.Channel, now int64, allowFull bool) (*model.Channel, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}

	minimumLoad := int(^uint(0) >> 1)
	leastLoaded := make([]*model.Channel, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		usedConcurrency, concurrencyLimit, err := model.GetCodeProviderConcurrencyUsage(candidate.Type, candidate.Id, now)
		if err != nil {
			return nil, err
		}
		reservedConcurrency := GetChannelAffinityReservedConcurrency(candidate.Id, now)
		effectiveLoad := usedConcurrency + reservedConcurrency
		if !allowFull && concurrencyLimit > 0 && effectiveLoad >= concurrencyLimit {
			continue
		}
		if effectiveLoad < minimumLoad {
			minimumLoad = effectiveLoad
			leastLoaded = leastLoaded[:0]
		}
		if effectiveLoad == minimumLoad {
			leastLoaded = append(leastLoaded, candidate)
		}
	}
	if len(leastLoaded) == 0 {
		return nil, nil
	}
	index := int(codeProviderLoadSelectionCursor.Add(1)-1) % len(leastLoaded)
	return leastLoaded[index], nil
}

func selectSpeedProtectedCandidate(param *RetryParam, group string) (*model.Channel, error) {
	if param == nil || param.Ctx == nil || !common.GetContextKeyBool(param.Ctx, constant.ContextKeyIsStream) {
		return nil, nil
	}
	candidates, err := model.GetSpeedProtectionCandidates(group, param.ModelName, param.AttemptedChannelIDs)
	if err != nil {
		return nil, err
	}
	available := make([]*model.Channel, 0, len(candidates))
	now := time.Now().Unix()
	for _, candidate := range candidates {
		if param.ExcludeCodeProviderChannels && (candidate.Type == constant.ChannelTypeOpencode || candidate.Type == constant.ChannelTypeCommand) {
			continue
		}
		if param.OpencodePool && candidate.Type != constant.ChannelTypeOpencode && candidate.Type != constant.ChannelTypeCommand {
			continue
		}
		if !relaycommon.IsStreamSupportedChannel(candidate.Type) {
			continue
		}
		if (candidate.Type == constant.ChannelTypeOpencode || candidate.Type == constant.ChannelTypeCommand) && !strings.HasSuffix(strings.TrimRight(param.RequestPath, "/"), "/chat/completions") {
			continue
		}
		if !model.ChannelSupportsRequestPath(candidate, param.RequestPath, param.ModelName) {
			continue
		}
		if candidate.Type != constant.ChannelTypeOpencode && candidate.Type != constant.ChannelTypeCommand {
			available, err := model.ChannelHasRPMCapacity(candidate, now)
			if err != nil {
				return nil, err
			}
			if !available {
				continue
			}
		}
		ok, err := model.CodeProviderChannelHasCapacity(candidate, param.ModelName, now, true)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		available = append(available, candidate)
	}
	return selectWeightedOpencodeCandidate(available), nil
}

func GetSpeedProtectedChannel(param *RetryParam) (*model.Channel, string, error) {
	if param == nil {
		return nil, "", nil
	}
	groups := []string{param.TokenGroup}
	if param.TokenGroup == "auto" {
		userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
		if group := common.GetContextKeyString(param.Ctx, constant.ContextKeyAutoGroup); group != "" {
			groups = []string{group}
		} else {
			groups = GetRequestAutoGroups(param.Ctx, userGroup)
		}
	}
	for _, group := range groups {
		channel, err := selectSpeedProtectedCandidate(param, group)
		if err != nil || channel != nil {
			return channel, group, err
		}
	}
	return nil, "", nil
}

func selectAffinityCodeProviderCandidate(param *RetryParam, group string) (*model.Channel, error) {
	if param == nil || param.Ctx == nil || group == "" {
		return nil, nil
	}
	affinityID := GetCurrentChannelAffinitySelection(param.Ctx)
	if affinityID <= 0 {
		return nil, nil
	}
	if _, attempted := param.AttemptedChannelIDs[affinityID]; attempted {
		ClearCurrentChannelAffinitySelection(param.Ctx)
		return nil, nil
	}

	channel, err := model.CacheGetChannel(affinityID)
	if err != nil || channel == nil {
		if !ShouldKeepChannelAffinityOnChannelDisabled() {
			ClearCurrentChannelAffinityCache(param.Ctx)
		} else {
			ClearCurrentChannelAffinitySelection(param.Ctx)
		}
		return nil, nil
	}
	if channel.Status != common.ChannelStatusEnabled ||
		(channel.Type != constant.ChannelTypeOpencode && channel.Type != constant.ChannelTypeCommand) ||
		!model.IsChannelEnabledForGroupModel(group, param.ModelName, channel.Id) ||
		!model.ChannelSupportsRequestPath(channel, param.RequestPath, param.ModelName) {
		if !ShouldKeepChannelAffinityOnChannelDisabled() {
			ClearCurrentChannelAffinityCache(param.Ctx)
		} else {
			ClearCurrentChannelAffinitySelection(param.Ctx)
		}
		return nil, nil
	}

	kicked, err := model.IsSpeedProtectionChannelKicked(channel, group)
	if err != nil {
		return nil, err
	}
	if kicked {
		if !ShouldKeepChannelAffinityOnChannelDisabled() {
			ClearCurrentChannelAffinityCache(param.Ctx)
		} else {
			ClearCurrentChannelAffinitySelection(param.Ctx)
		}
		return nil, nil
	}

	now := time.Now().Unix()
	quotaAvailable, err := model.CodeProviderChannelHasCapacity(channel, param.ModelName, now, false)
	if err != nil {
		return nil, err
	}
	if !quotaAvailable {
		// A quota-exhausted affinity must never be selected again for this key.
		ClearCurrentChannelAffinityCache(param.Ctx)
		return nil, nil
	}
	available, err := model.CodeProviderChannelHasCapacity(channel, param.ModelName, now, true)
	if err != nil {
		return nil, err
	}
	if !available {
		// RPM saturation is temporary. Keep the cache, but let ordinary
		// candidates serve this request when one is available.
		return nil, nil
	}
	return channel, nil
}

func selectQueuedAffinityCandidate(param *RetryParam) (*model.Channel, string, error) {
	if param == nil || param.Ctx == nil {
		return nil, "", nil
	}
	affinityID := GetPendingChannelAffinitySelection(param.Ctx)
	if affinityID <= 0 {
		affinityID = GetCurrentChannelAffinitySelection(param.Ctx)
	}
	if affinityID <= 0 {
		return nil, "", nil
	}
	if _, attempted := param.AttemptedChannelIDs[affinityID]; attempted {
		ClearCurrentChannelAffinitySelection(param.Ctx)
		return nil, "", nil
	}

	channel, err := model.CacheGetChannel(affinityID)
	if err != nil || channel == nil {
		if !ShouldKeepChannelAffinityOnChannelDisabled() {
			ClearCurrentChannelAffinityCache(param.Ctx)
		} else {
			ClearCurrentChannelAffinitySelection(param.Ctx)
		}
		return nil, "", err
	}
	if channel.Status != common.ChannelStatusEnabled ||
		(channel.Type != constant.ChannelTypeOpencode && channel.Type != constant.ChannelTypeCommand) ||
		!model.ChannelSupportsRequestPath(channel, param.RequestPath, param.ModelName) {
		if !ShouldKeepChannelAffinityOnChannelDisabled() {
			ClearCurrentChannelAffinityCache(param.Ctx)
		} else {
			ClearCurrentChannelAffinitySelection(param.Ctx)
		}
		return nil, "", nil
	}

	groups := []string{param.TokenGroup}
	if param.TokenGroup == "auto" {
		userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)
		if selectedGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyAutoGroup); selectedGroup != "" {
			groups = []string{selectedGroup}
		} else {
			groups = GetRequestAutoGroups(param.Ctx, userGroup)
		}
	}
	now := time.Now().Unix()
	quotaAvailable, err := model.CodeProviderChannelHasCapacity(channel, param.ModelName, now, false)
	if err != nil {
		return nil, "", err
	}
	if !quotaAvailable {
		ClearCurrentChannelAffinityCache(param.Ctx)
		return nil, "", nil
	}
	for _, group := range groups {
		if !model.IsChannelEnabledForGroupModel(group, param.ModelName, channel.Id) {
			continue
		}
		kicked, err := model.IsSpeedProtectionChannelKicked(channel, group)
		if err != nil {
			return nil, "", err
		}
		if kicked {
			continue
		}
		touchChannelAffinityProtection(param.Ctx)
		return channel, group, nil
	}
	if !ShouldKeepChannelAffinityOnChannelDisabled() {
		ClearCurrentChannelAffinityCache(param.Ctx)
	} else {
		ClearCurrentChannelAffinitySelection(param.Ctx)
	}
	return nil, "", nil
}

func filterCodeProviderCandidatesByAffinityReserve(param *RetryParam, candidates []*model.Channel) ([]*model.Channel, error) {
	if param == nil || len(candidates) == 0 {
		return candidates, nil
	}
	filtered := make([]*model.Channel, 0, len(candidates))
	now := time.Now().Unix()
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		reserved := GetChannelAffinityReservedConcurrency(candidate.Id, now)
		if reserved <= 0 {
			filtered = append(filtered, candidate)
			continue
		}
		available, err := model.CodeProviderChannelHasGeneralCapacity(candidate, param.ModelName, now, reserved)
		if err != nil {
			return nil, err
		}
		if available {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, nil
}

func CacheGetRandomSatisfiedChannel(param *RetryParam) (*model.Channel, string, error) {
	var channel *model.Channel
	var err error
	selectGroup := param.TokenGroup
	userGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserGroup)

	if param.OpencodePool {
		groups := []string{param.TokenGroup}
		if param.TokenGroup == "auto" {
			if group := common.GetContextKeyString(param.Ctx, constant.ContextKeyAutoGroup); group != "" {
				groups = []string{group}
			} else {
				groups = GetRequestAutoGroups(param.Ctx, userGroup)
			}
			if len(groups) == 0 {
				return nil, "", nil
			}
		}

		for _, group := range groups {
			if param.OpencodeIncludeRPMFull {
				if queuedAffinity, queuedGroup, queuedErr := selectQueuedAffinityCandidate(param); queuedErr != nil {
					return nil, group, queuedErr
				} else if queuedAffinity != nil {
					if param.TokenGroup == "auto" {
						common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, queuedGroup)
					}
					return queuedAffinity, queuedGroup, nil
				}
			}
			if affinityChannel, affinityErr := selectAffinityCodeProviderCandidate(param, group); affinityErr != nil {
				return nil, group, affinityErr
			} else if affinityChannel != nil {
				if param.TokenGroup == "auto" {
					common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, group)
				}
				return affinityChannel, group, nil
			}
			var candidates []*model.Channel
			if param.OpencodeIncludeRPMFull {
				candidates, err = model.GetCodeProviderQuotaCandidates(group, param.ModelName, param.AttemptedChannelIDs)
			} else {
				candidates, err = model.GetCodeProviderCandidates(group, param.ModelName, param.AttemptedChannelIDs)
			}
			if err != nil {
				return nil, group, err
			}
			candidatePoolEmpty := len(candidates) == 0
			if candidatePoolEmpty {
				if speedChannel, speedErr := selectSpeedProtectedCandidate(param, group); speedErr != nil {
					return nil, group, speedErr
				} else if speedChannel != nil {
					if param.TokenGroup == "auto" {
						common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, group)
					}
					ClearCurrentChannelAffinitySelection(param.Ctx)
					return speedChannel, group, nil
				}
				continue
			}
			if !param.OpencodeIncludeRPMFull {
				candidates, err = filterCodeProviderCandidatesByAffinityReserve(param, candidates)
				if err != nil {
					return nil, group, err
				}
				if len(candidates) == 0 {
					continue
				}
			}
			balancedChannel, balancedErr := selectLeastLoadedCodeProviderCandidate(candidates, time.Now().Unix(), param.OpencodeIncludeRPMFull)
			if balancedErr != nil {
				return nil, group, balancedErr
			}
			if balancedChannel != nil {
				if param.TokenGroup == "auto" {
					common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, group)
				}
				if GetCurrentChannelAffinitySelection(param.Ctx) > 0 {
					ClearCurrentChannelAffinitySelection(param.Ctx)
				}
				return balancedChannel, group, nil
			}
		}
		return nil, groups[0], nil
	}

	if param.TokenGroup == "auto" {
		autoGroups := GetRequestAutoGroups(param.Ctx, userGroup)
		if len(autoGroups) == 0 {
			return nil, selectGroup, errors.New("auto groups is not enabled")
		}

		// startGroupIndex: the group index to start searching from
		// startGroupIndex: 开始搜索的分组索引
		startGroupIndex := 0
		crossGroupRetry := common.GetContextKeyBool(param.Ctx, constant.ContextKeyTokenCrossGroupRetry)

		if lastGroupIndex, exists := common.GetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex); exists {
			if idx, ok := lastGroupIndex.(int); ok {
				startGroupIndex = idx
			}
		}

		for i := startGroupIndex; i < len(autoGroups); i++ {
			autoGroup := autoGroups[i]
			// Calculate priorityRetry for current group
			// 计算当前分组的 priorityRetry
			priorityRetry := param.GetRetry()
			// If moved to a new group, reset priorityRetry and update startRetryIndex
			// 如果切换到新分组，重置 priorityRetry 并更新 startRetryIndex
			if i > startGroupIndex {
				priorityRetry = 0
			}
			logger.LogDebug(param.Ctx, "Auto selecting group: %s, priorityRetry: %d", autoGroup, priorityRetry)
			if speedChannel, speedErr := selectSpeedProtectedCandidate(param, autoGroup); speedErr != nil {
				return nil, autoGroup, speedErr
			} else if speedChannel != nil {
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i)
				return speedChannel, autoGroup, nil
			}

			if param.ExcludeCodeProviderChannels {
				channel, _ = model.GetRandomSatisfiedOrdinaryChannelWithAttempted(autoGroup, param.ModelName, priorityRetry, param.RequestPath, param.AttemptedChannelIDs)
			} else {
				channel, _ = model.GetRandomSatisfiedChannelWithAttempted(autoGroup, param.ModelName, priorityRetry, param.RequestPath, param.AttemptedChannelIDs)
			}
			if channel == nil {
				// Current group has no available channel for this model, try next group
				// 当前分组没有该模型的可用渠道，尝试下一个分组
				logger.LogDebug(param.Ctx, "No available channel in group %s for model %s at priorityRetry %d, trying next group", autoGroup, param.ModelName, priorityRetry)
				// 重置状态以尝试下一个分组
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupRetryIndex, 0)
				// Reset retry counter so outer loop can continue for next group
				// 重置重试计数器，以便外层循环可以为下一个分组继续
				param.SetRetry(0)
				continue
			}
			common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroup, autoGroup)
			selectGroup = autoGroup
			logger.LogDebug(param.Ctx, "Auto selected group: %s", autoGroup)

			// Prepare state for next retry
			// 为下一次重试准备状态
			if crossGroupRetry && priorityRetry >= common.RetryTimes {
				// Current group has exhausted all retries, prepare to switch to next group
				// This request still uses current group, but next retry will use next group
				// 当前分组已用完所有重试次数，准备切换到下一个分组
				// 本次请求仍使用当前分组，但下次重试将使用下一个分组
				logger.LogDebug(param.Ctx, "Current group %s retries exhausted (priorityRetry=%d >= RetryTimes=%d), preparing switch to next group for next retry", autoGroup, priorityRetry, common.RetryTimes)
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i+1)
				// Reset retry counter so outer loop can continue for next group
				// 重置重试计数器，以便外层循环可以为下一个分组继续
				param.SetRetry(0)
				param.ResetRetryNextTry()
			} else {
				// Stay in current group, save current state
				// 保持在当前分组，保存当前状态
				common.SetContextKey(param.Ctx, constant.ContextKeyAutoGroupIndex, i)
			}
			break
		}
	} else {
		if speedChannel, speedErr := selectSpeedProtectedCandidate(param, param.TokenGroup); speedErr != nil {
			return nil, param.TokenGroup, speedErr
		} else if speedChannel != nil {
			return speedChannel, param.TokenGroup, nil
		}
		if param.ExcludeCodeProviderChannels {
			channel, err = model.GetRandomSatisfiedOrdinaryChannelWithAttempted(param.TokenGroup, param.ModelName, param.GetRetry(), param.RequestPath, param.AttemptedChannelIDs)
		} else {
			channel, err = model.GetRandomSatisfiedChannelWithAttempted(param.TokenGroup, param.ModelName, param.GetRetry(), param.RequestPath, param.AttemptedChannelIDs)
		}
		if err != nil {
			return nil, param.TokenGroup, err
		}
	}
	return channel, selectGroup, nil
}

// CacheGetRandomSatisfiedChannelWithCodeProviderFallback first tries the
// provider-specific pool and only falls back to ordinary channels when that
// pool has no candidate. This keeps code-provider queueing intact while
// allowing a regular channel configured with the same model to serve when all
// Command/OpenCode channels in the group are disabled or exhausted.
func CacheGetRandomSatisfiedChannelWithCodeProviderFallback(param *RetryParam) (*model.Channel, string, error) {
	channel, group, err := CacheGetRandomSatisfiedChannel(param)
	if param == nil || !param.OpencodePool || channel != nil || err != nil {
		return channel, group, err
	}

	fallbackParam := *param
	fallbackParam.OpencodePool = false
	fallbackParam.OpencodeIncludeRPMFull = false
	fallbackParam.OpencodeRPMReserved = false
	fallbackParam.OpencodeConcurrencyReserved = false
	fallbackParam.ExcludeCodeProviderChannels = true
	return CacheGetRandomSatisfiedChannel(&fallbackParam)
}
