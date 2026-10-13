package model

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

var group2model2channels map[string]map[string][]int // enabled channel
var channelsIDM map[int]*Channel                     // all channels include disabled
// channel2advancedCustomConfig caches parsed Advanced Custom (type 58) configs so
// path-aware selection avoids re-parsing JSON per request. Refreshed on full sync.
var channel2advancedCustomConfig map[int]*dto.AdvancedCustomConfig

// fallbackChannelIDSet caches the set of channel IDs referenced by any channel's
// fallback_sequence (i.e. the fallback channel marking set), refreshed on full sync.
var fallbackChannelIDSet map[int]struct{}
var channelSyncLock sync.RWMutex

// fallbackSetTTL bounds how often the fallback marking set is recomputed from the
// database when the memory cache is disabled, so each relay request does not trigger
// a full-table scan. fallbackSetDB holds the last successfully loaded set; on a
// database failure the previous value is retained instead of falling back to empty.
var (
	fallbackSetTTL        = 20 * time.Second
	fallbackSetLastLoaded time.Time
	fallbackSetDB         map[int]struct{}
)

// buildFallbackChannelIDSet derives the fallback marking set from the given
// channels. Channel IDs referenced by any channel's fallback_sequence are kept,
// even if they no longer exist.
func buildFallbackChannelIDSet(channels []*Channel) map[int]struct{} {
	set := make(map[int]struct{})
	for _, channel := range channels {
		for _, id := range channel.GetFallbackSequenceIDs() {
			set[id] = struct{}{}
		}
	}
	return set
}

// refreshFallbackChannelIDSet recomputes the fallback marking set directly from
// the database. It returns the freshly built set or an error when the query fails.
// Used when the memory cache is disabled, where the result is cached with a TTL.
func refreshFallbackChannelIDSet() (map[int]struct{}, error) {
	var channels []*Channel
	if err := DB.Model(&Channel{}).Where("fallback_sequence != ?", "").Find(&channels).Error; err != nil {
		return nil, err
	}
	return buildFallbackChannelIDSet(channels), nil
}

// loadFallbackChannelIDSet returns the fallback marking set for DB mode, applying
// a TTL cache so per-request selection does not rescan the whole table every time.
// On a failed reload the most recently loaded set is reused rather than empty.
func loadFallbackChannelIDSet() map[int]struct{} {
	channelSyncLock.RLock()
	if fallbackSetDB != nil && time.Since(fallbackSetLastLoaded) < fallbackSetTTL {
		set := fallbackSetDB
		channelSyncLock.RUnlock()
		return set
	}
	channelSyncLock.RUnlock()

	set, err := refreshFallbackChannelIDSet()

	channelSyncLock.Lock()
	defer channelSyncLock.Unlock()
	if err != nil {
		// DB reload failed: retain the last good value; only default to empty the
		// very first time. Refresh the loaded timestamp to avoid thrashing on every
		// request while the database is unavailable.
		if fallbackSetDB == nil {
			fallbackSetDB = map[int]struct{}{}
		}
		fallbackSetLastLoaded = time.Now()
		return fallbackSetDB
	}
	fallbackSetDB = set
	fallbackSetLastLoaded = time.Now()
	return fallbackSetDB
}

// GetFallbackChannelIDSet returns the set of channel IDs referenced by any
// channel's fallback_sequence. With the memory cache enabled it returns the
// cached set; otherwise it scans the database live but cached with a short TTL.
func GetFallbackChannelIDSet() map[int]struct{} {
	if !common.MemoryCacheEnabled {
		return loadFallbackChannelIDSet()
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	return fallbackChannelIDSet
}

// IsFallbackChannel reports whether channelID is referenced by any channel's
// fallback_sequence (i.e. it is a fallback channel).
func IsFallbackChannel(channelID int) bool {
	_, ok := GetFallbackChannelIDSet()[channelID]
	return ok
}

func InitChannelCache() {
	initCol()
	if !common.MemoryCacheEnabled {
		InvalidatePricingCache()
		return
	}
	newChannelId2channel := make(map[int]*Channel)
	newChannel2advancedCustomConfig := make(map[int]*dto.AdvancedCustomConfig)
	var channels []*Channel
	DB.Find(&channels)
	for _, channel := range channels {
		newChannelId2channel[channel.Id] = channel
		if channel.Type == constant.ChannelTypeAdvancedCustom {
			if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
				newChannel2advancedCustomConfig[channel.Id] = config
			}
		}
	}
	var abilities []*Ability
	DB.Find(&abilities)
	groups := make(map[string]bool)
	for _, ability := range abilities {
		groups[ability.Group] = true
	}
	newGroup2model2channels := make(map[string]map[string][]int)
	for group := range groups {
		newGroup2model2channels[group] = make(map[string][]int)
	}
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			continue // skip disabled channels
		}
		groups := strings.Split(channel.Group, ",")
		for _, group := range groups {
			models := strings.Split(channel.Models, ",")
			for _, model := range models {
				if _, ok := newGroup2model2channels[group][model]; !ok {
					newGroup2model2channels[group][model] = make([]int, 0)
				}
				newGroup2model2channels[group][model] = append(newGroup2model2channels[group][model], channel.Id)
			}
		}
	}

	// sort by priority
	for group, model2channels := range newGroup2model2channels {
		for model, channels := range model2channels {
			sort.Slice(channels, func(i, j int) bool {
				return newChannelId2channel[channels[i]].GetPriority() > newChannelId2channel[channels[j]].GetPriority()
			})
			newGroup2model2channels[group][model] = channels
		}
	}

	channelSyncLock.Lock()
	group2model2channels = newGroup2model2channels
	//channelsIDM = newChannelId2channel
	for i, channel := range newChannelId2channel {
		if channel.ChannelInfo.IsMultiKey {
			channel.Keys = channel.GetKeys()
			if channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
				if oldChannel, ok := channelsIDM[i]; ok {
					// 存在旧的渠道，如果是多key且轮询，保留轮询索引信息
					if oldChannel.ChannelInfo.IsMultiKey && oldChannel.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
						channel.ChannelInfo.MultiKeyPollingIndex = oldChannel.ChannelInfo.MultiKeyPollingIndex
					}
				}
			}
		}
	}
	channelsIDM = newChannelId2channel
	channel2advancedCustomConfig = newChannel2advancedCustomConfig
	fallbackChannelIDSet = buildFallbackChannelIDSet(channels)
	channelSyncLock.Unlock()
	// Lock ordering: InvalidatePricingCache acquires updatePricingLock, and
	// GetPricing (holding updatePricingLock) nests channelSyncLock.RLock via
	// loadPricingAdvancedCustomConfigs. channelSyncLock MUST be released before
	// invalidating the pricing cache, otherwise the reversed order deadlocks.
	InvalidatePricingCache()
	common.SysLog("channels synced from database")
}

func SyncChannelCache(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Second)
		common.SysLog("syncing channels from database")
		InitChannelCache()
	}
}

func GetRandomSatisfiedChannel(group string, model string, retry int, requestPath string) (*Channel, error) {
	return getRandomSatisfiedChannel(group, model, retry, requestPath, nil, false)
}

func GetRandomSatisfiedChannelWithAttempted(group string, model string, retry int, requestPath string, attempted map[int]struct{}) (*Channel, error) {
	return getRandomSatisfiedChannel(group, model, retry, requestPath, attempted, false)
}

// GetRandomSatisfiedOrdinaryChannel selects a channel while excluding the
// provider-specific OpenCode and Command channel types. This is used only
// after a fixed code-provider model has no eligible provider channel left.
func GetRandomSatisfiedOrdinaryChannel(group string, model string, retry int, requestPath string) (*Channel, error) {
	return getRandomSatisfiedChannel(group, model, retry, requestPath, nil, true)
}

func GetRandomSatisfiedOrdinaryChannelWithAttempted(group string, model string, retry int, requestPath string, attempted map[int]struct{}) (*Channel, error) {
	return getRandomSatisfiedChannel(group, model, retry, requestPath, attempted, true)
}

func getRandomSatisfiedChannel(group string, model string, retry int, requestPath string, attempted map[int]struct{}, excludeCodeProviders bool) (*Channel, error) {
	// if memory cache is disabled, get channel directly from database
	if !common.MemoryCacheEnabled {
		if excludeCodeProviders {
			return GetOrdinaryChannelWithAttempted(group, model, retry, requestPath, attempted)
		}
		return GetChannelWithAttempted(group, model, retry, requestPath, attempted)
	}

	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	// First, try to find channels with the exact model name.
	channels := filterChannelsByRequestPathAndModel(group2model2channels[group][model], requestPath, model)
	if excludeCodeProviders {
		channels = filterChannelIDsByCodeProviderType(channels, true)
	}
	channels = filterChannelIDsByAttempted(channels, attempted)
	channels = filterChannelIDsByFallback(channels, fallbackChannelIDSet)
	channels, err := filterChannelIDsByRPMCapacity(channels, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	channels, err = filterChannelIDsByKickedSpeedProtection(channels, group)
	if err != nil {
		return nil, err
	}

	// If no channels found, try to find channels with the normalized model name.
	if len(channels) == 0 {
		normalizedModel := ratio_setting.FormatMatchingModelName(model)
		channels = filterChannelsByRequestPathAndModel(group2model2channels[group][normalizedModel], requestPath, model)
		if excludeCodeProviders {
			channels = filterChannelIDsByCodeProviderType(channels, true)
		}
		channels = filterChannelIDsByAttempted(channels, attempted)
		channels = filterChannelIDsByFallback(channels, fallbackChannelIDSet)
		channels, err = filterChannelIDsByRPMCapacity(channels, time.Now().Unix())
		if err != nil {
			return nil, err
		}
		channels, err = filterChannelIDsByKickedSpeedProtection(channels, group)
		if err != nil {
			return nil, err
		}
	}

	if len(channels) == 0 {
		return nil, nil
	}

	if len(channels) == 1 {
		if channel, ok := channelsIDM[channels[0]]; ok {
			return channel, nil
		}
		return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channels[0])
	}

	uniquePriorities := make(map[int]bool)
	for _, channelId := range channels {
		if channel, ok := channelsIDM[channelId]; ok {
			uniquePriorities[int(channel.GetPriority())] = true
		} else {
			return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channelId)
		}
	}
	var sortedUniquePriorities []int
	for priority := range uniquePriorities {
		sortedUniquePriorities = append(sortedUniquePriorities, priority)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sortedUniquePriorities)))

	if retry >= len(uniquePriorities) {
		retry = len(uniquePriorities) - 1
	}
	targetPriority := int64(sortedUniquePriorities[retry])

	// get the priority for the given retry number
	var sumWeight = 0
	var targetChannels []*Channel
	for _, channelId := range channels {
		if channel, ok := channelsIDM[channelId]; ok {
			if channel.GetPriority() == targetPriority {
				sumWeight += channel.GetWeight()
				targetChannels = append(targetChannels, channel)
			}
		} else {
			return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channelId)
		}
	}

	if len(targetChannels) == 0 {
		return nil, errors.New(fmt.Sprintf("no channel found, group: %s, model: %s, priority: %d", group, model, targetPriority))
	}

	// smoothing factor and adjustment
	smoothingFactor := 1
	smoothingAdjustment := 0

	if sumWeight == 0 {
		// when all channels have weight 0, set sumWeight to the number of channels and set smoothing adjustment to 100
		// each channel's effective weight = 100
		sumWeight = len(targetChannels) * 100
		smoothingAdjustment = 100
	} else if sumWeight/len(targetChannels) < 10 {
		// when the average weight is less than 10, set smoothing factor to 100
		smoothingFactor = 100
	}

	// Calculate the total weight of all channels up to endIdx
	totalWeight := sumWeight * smoothingFactor

	// Generate a random value in the range [0, totalWeight)
	randomWeight := rand.Intn(totalWeight)

	// Find a channel based on its weight
	for _, channel := range targetChannels {
		randomWeight -= channel.GetWeight()*smoothingFactor + smoothingAdjustment
		if randomWeight < 0 {
			return channel, nil
		}
	}
	// return null if no channel is not found
	return nil, errors.New("channel not found")
}

func filterChannelIDsByCodeProviderType(channelIDs []int, excludeCodeProviders bool) []int {
	if !excludeCodeProviders || len(channelIDs) == 0 {
		return channelIDs
	}
	filtered := make([]int, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		channel, ok := channelsIDM[channelID]
		if !ok || (channel.Type != constant.ChannelTypeOpencode && channel.Type != constant.ChannelTypeCommand) {
			filtered = append(filtered, channelID)
		}
	}
	return filtered
}

func filterChannelIDsByAttempted(channelIDs []int, attempted map[int]struct{}) []int {
	if len(channelIDs) == 0 || len(attempted) == 0 {
		return channelIDs
	}
	filtered := make([]int, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		if _, ok := attempted[channelID]; !ok {
			filtered = append(filtered, channelID)
		}
	}
	return filtered
}

// filterChannelIDsByFallback removes fallback channels (channel IDs referenced
// by any channel's fallback_sequence) from the candidate pool. fallbackSet must
// be an already-computed set; internal callers that already hold channelSyncLock
// pass the cached set directly to avoid a nested read lock.
func filterChannelIDsByFallback(channelIDs []int, fallbackSet map[int]struct{}) []int {
	if len(channelIDs) == 0 || len(fallbackSet) == 0 {
		return channelIDs
	}
	filtered := make([]int, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		if _, ok := fallbackSet[channelID]; !ok {
			filtered = append(filtered, channelID)
		}
	}
	return filtered
}

func filterChannelIDsByRPMCapacity(channelIDs []int, now int64) ([]int, error) {
	if len(channelIDs) == 0 {
		return channelIDs, nil
	}
	filtered := make([]int, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		channel := channelsIDM[channelID]
		if channel == nil {
			filtered = append(filtered, channelID)
			continue
		}
		available, err := ChannelHasRPMCapacity(channel, now)
		if err != nil {
			return nil, err
		}
		if available {
			filtered = append(filtered, channelID)
		}
	}
	return filtered, nil
}

func filterChannelIDsByKickedSpeedProtection(channelIDs []int, group string) ([]int, error) {
	if len(channelIDs) == 0 || strings.TrimSpace(group) == "" || DB == nil {
		return channelIDs, nil
	}
	filtered := make([]int, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		channel := channelsIDM[channelID]
		kicked, err := IsSpeedProtectionChannelKicked(channel, group)
		if err != nil {
			return nil, err
		}
		if !kicked {
			filtered = append(filtered, channelID)
		}
	}
	return filtered, nil
}

// filterChannelsByRequestPathAndModel restricts candidates by request path and
// model. Only Advanced Custom (type 58) channels are path-checked: they are kept
// only when one of their configured routes matches requestPath and model. All
// other channel types always pass. When requestPath is empty, filtering is skipped.
// Caller must hold channelSyncLock (read lock). The cached slice is never mutated.
func filterChannelsByRequestPathAndModel(channels []int, requestPath string, model string) []int {
	if requestPath == "" || len(channels) == 0 {
		return channels
	}
	filtered := make([]int, 0, len(channels))
	for _, channelId := range channels {
		channel, ok := channelsIDM[channelId]
		if !ok {
			// keep it so the downstream consistency error is raised as before
			filtered = append(filtered, channelId)
			continue
		}
		if channel.Type != constant.ChannelTypeAdvancedCustom {
			filtered = append(filtered, channelId)
			continue
		}
		if config := channel2advancedCustomConfig[channelId]; config != nil && config.SupportsPathForModel(requestPath, model) {
			filtered = append(filtered, channelId)
		}
	}
	return filtered
}

func ChannelSupportsRequestPath(channel *Channel, requestPath string, model string) bool {
	if channel == nil {
		return false
	}
	if channel.Type != constant.ChannelTypeAdvancedCustom || requestPath == "" {
		return true
	}
	config := channel.GetOtherSettings().AdvancedCustom
	return config != nil && config.SupportsPathForModel(requestPath, model)
}

func CodeProviderChannelHasCapacity(channel *Channel, modelName string, now int64, checkRPM bool) (bool, error) {
	if channel == nil {
		return false, nil
	}
	if channel.Type != constant.ChannelTypeOpencode && channel.Type != constant.ChannelTypeCommand {
		return true, nil
	}
	if !channelSupportsCodeProviderModel(channel, modelName) {
		return false, nil
	}
	if channel.Type == constant.ChannelTypeOpencode {
		if checkRPM {
			available, err := OpencodeChannelHasCapacityForModel(channel, modelName, now)
			if !available && err == nil {
				err = ReconcileCodeProviderQuotaChannel(channel.Id, now)
			}
			return available, err
		}
		available, err := OpencodeChannelHasQuotaCapacityForModel(channel, modelName, now)
		if !available && err == nil {
			err = ReconcileCodeProviderQuotaChannel(channel.Id, now)
		}
		return available, err
	}
	if checkRPM {
		available, err := CommandChannelHasCapacityForModel(channel, modelName, now)
		if !available && err == nil {
			err = ReconcileCodeProviderQuotaChannel(channel.Id, now)
		}
		return available, err
	}
	available, err := CommandChannelHasQuotaCapacityForModel(channel, modelName, now)
	if !available && err == nil {
		err = ReconcileCodeProviderQuotaChannel(channel.Id, now)
	}
	return available, err
}

func CacheGetChannel(id int) (*Channel, error) {
	if !common.MemoryCacheEnabled {
		return GetChannelById(id, true)
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	c, ok := channelsIDM[id]
	if !ok {
		return nil, fmt.Errorf("渠道# %d，已不存在", id)
	}
	return c, nil
}

func CacheGetChannelInfo(id int) (*ChannelInfo, error) {
	if !common.MemoryCacheEnabled {
		channel, err := GetChannelById(id, true)
		if err != nil {
			return nil, err
		}
		return &channel.ChannelInfo, nil
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	c, ok := channelsIDM[id]
	if !ok {
		return nil, fmt.Errorf("渠道# %d，已不存在", id)
	}
	return &c.ChannelInfo, nil
}

func CacheUpdateChannelStatus(id int, status int) {
	if !common.MemoryCacheEnabled {
		return
	}
	channelSyncLock.Lock()
	defer channelSyncLock.Unlock()
	if channel, ok := channelsIDM[id]; ok {
		channel.Status = status
	}
	if status != common.ChannelStatusEnabled {
		// delete the channel from group2model2channels
		for group, model2channels := range group2model2channels {
			for model, channels := range model2channels {
				for i, channelId := range channels {
					if channelId == id {
						// remove the channel from the slice
						group2model2channels[group][model] = append(channels[:i], channels[i+1:]...)
						break
					}
				}
			}
		}
	} else if channel, ok := channelsIDM[id]; ok {
		// Re-enable the channel in the in-memory candidate index immediately.
		// Status updates remove disabled channels from this index, so only
		// changing the cached status would otherwise leave the channel absent
		// until the next full cache sync.
		if group2model2channels == nil {
			group2model2channels = make(map[string]map[string][]int)
		}
		for _, group := range channel.GetGroups() {
			if _, ok := group2model2channels[group]; !ok {
				group2model2channels[group] = make(map[string][]int)
			}
			for _, modelName := range channel.GetModels() {
				models := group2model2channels[group][modelName]
				alreadyPresent := false
				for _, channelID := range models {
					if channelID == id {
						alreadyPresent = true
						break
					}
				}
				if alreadyPresent {
					continue
				}
				models = append(models, id)
				sort.Slice(models, func(i, j int) bool {
					left := channelsIDM[models[i]]
					right := channelsIDM[models[j]]
					if left == nil || right == nil {
						return models[i] < models[j]
					}
					return left.GetPriority() > right.GetPriority()
				})
				group2model2channels[group][modelName] = models
			}
		}
	}
}

func CacheUpdateChannel(channel *Channel) {
	if !common.MemoryCacheEnabled {
		return
	}
	channelSyncLock.Lock()
	if channel == nil {
		channelSyncLock.Unlock()
		return
	}

	if channelsIDM == nil {
		channelsIDM = make(map[int]*Channel)
	}
	if oldChannel, ok := channelsIDM[channel.Id]; ok {
		logger.LogDebug(nil, "CacheUpdateChannel before: id=%d, name=%s, status=%d, polling_index=%d", channel.Id, channel.Name, channel.Status, oldChannel.ChannelInfo.MultiKeyPollingIndex)
	}
	channelsIDM[channel.Id] = channel
	if channel2advancedCustomConfig == nil {
		channel2advancedCustomConfig = make(map[int]*dto.AdvancedCustomConfig)
	}
	delete(channel2advancedCustomConfig, channel.Id)
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
			channel2advancedCustomConfig[channel.Id] = config
		}
	}
	logger.LogDebug(nil, "CacheUpdateChannel after: id=%d, name=%s, status=%d, polling_index=%d", channel.Id, channel.Name, channel.Status, channel.ChannelInfo.MultiKeyPollingIndex)
	// Lock ordering: do NOT hold channelSyncLock while calling
	// InvalidatePricingCache. GetPricing acquires updatePricingLock first and then
	// channelSyncLock.RLock (via loadPricingAdvancedCustomConfigs); acquiring
	// updatePricingLock while holding channelSyncLock would be an AB-BA deadlock.
	channelSyncLock.Unlock()
	InvalidatePricingCache()
}
