package service

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

var ErrOpencodeQueueUnavailable = errors.New("opencode has no local quota capacity")

type OpencodeQueueItem struct {
	ID          string `json:"id"`
	UserID      int    `json:"user_id"`
	Username    string `json:"username"`
	Group       string `json:"group"`
	Model       string `json:"model"`
	InputTokens int    `json:"input_tokens"`
	QueuedAt    int64  `json:"queued_at"`
	Position    int    `json:"position"`
}

type OpencodeQueueSnapshot struct {
	TotalCapacity        int                 `json:"total_capacity"`
	AvailableCapacity    int                 `json:"available_capacity"`
	QueuedCount          int                 `json:"queued_count"`
	EstimatedWaitMinutes int                 `json:"estimated_wait_minutes"`
	Items                []OpencodeQueueItem `json:"items"`
}

type opencodeQueueEntry struct {
	item     OpencodeQueueItem
	queueKey opencodeQueueKey
}

type opencodeQueueKey struct {
	group     string
	model     string
	channelID int
}

var (
	opencodeQueueMu   sync.Mutex
	opencodeQueue     []*opencodeQueueEntry
	opencodeQueueWake = make(chan struct{})
	opencodeQueueSeq  atomic.Uint64
)

func WaitForOpencodeChannel(param *RetryParam, inputTokens int) (*model.Channel, string, error) {
	if param == nil || param.Ctx == nil || param.Ctx.Request == nil {
		return nil, "", errors.New("opencode queue request context is missing")
	}
	if param.ModelName == "" {
		return nil, "", errors.New("unsupported code provider model")
	}

	// Session-capacity wait is independent of the in-flight request lease queue.
	// Do it first so a full session table cannot occupy concurrency slots, and
	// so timeout can switch away from an affinity channel.
	if IsOpencodeSessionQueueRequired(param.Ctx) {
		if _, _, err := waitForOpencodeSessionCapacity(param, queueGroupForParam(param)); err != nil {
			return nil, "", err
		}
	}

	affinityID := GetPendingChannelAffinitySelection(param.Ctx)
	if affinityID <= 0 {
		affinityID = GetCurrentChannelAffinitySelection(param.Ctx)
	}
	selectedID := rememberedOpencodeSessionChannelID(param.Ctx)
	channelID := affinityID
	if selectedID > 0 && affinityID > 0 && selectedID != affinityID {
		// Session wait moved or hard-fell back. Do not keep the request-lease
		// queue pinned to the previous affinity channel.
		channelID = 0
	}
	entry := &opencodeQueueEntry{
		item: newOpencodeQueueItem(param, inputTokens),
		queueKey: opencodeQueueKey{
			group:     queueGroupForParam(param),
			model:     param.ModelName,
			channelID: channelID,
		},
	}
	opencodeQueueMu.Lock()
	entry.item.Position = len(opencodeQueue) + 1
	opencodeQueue = append(opencodeQueue, entry)
	opencodeQueueMu.Unlock()

	ctx := param.Ctx.Request.Context()
	for {
		opencodeQueueMu.Lock()
		if isOpencodeQueueTurnLocked(entry) {
			if err := ctx.Err(); err != nil {
				removeOpencodeQueueEntryLocked(entry)
				opencodeQueueMu.Unlock()
				return nil, "", err
			}
			channel, group, err := tryReserveOpencodeChannel(param)
			if err != nil {
				removeOpencodeQueueEntryLocked(entry)
				opencodeQueueMu.Unlock()
				return nil, "", err
			}
			if channel != nil {
				removeOpencodeQueueEntryLocked(entry)
				param.OpencodeConcurrencyReserved = true
				param.OpencodeRPMReserved = true
				opencodeQueueMu.Unlock()
				return channel, group, nil
			}

			if entry.queueKey.channelID > 0 {
				pendingID := GetPendingChannelAffinitySelection(param.Ctx)
				currentID := GetCurrentChannelAffinitySelection(param.Ctx)
				selectedID := rememberedOpencodeSessionChannelID(param.Ctx)
				if pendingID <= 0 && currentID <= 0 {
					entry.queueKey.channelID = 0
				} else if selectedID > 0 && selectedID != entry.queueKey.channelID {
					entry.queueKey.channelID = 0
				} else {
					wake := opencodeQueueWake
					opencodeQueueMu.Unlock()
					timer := time.NewTimer(time.Second)
					select {
					case <-ctx.Done():
						if !timer.Stop() {
							<-timer.C
						}
						opencodeQueueMu.Lock()
						removeOpencodeQueueEntryLocked(entry)
						opencodeQueueMu.Unlock()
						return nil, "", ctx.Err()
					case <-wake:
						if !timer.Stop() {
							<-timer.C
						}
					case <-timer.C:
					}
					continue
				}
			}

			// A code-provider channel can have quota remaining while every
			// channel in this group is currently at its RPM limit. Do not make
			// requests wait behind that queue when an ordinary channel can serve
			// the same model in the same group.
			fallbackChannel, fallbackGroup, fallbackErr := selectOrdinaryChannelFallback(param)
			if fallbackErr != nil {
				removeOpencodeQueueEntryLocked(entry)
				opencodeQueueMu.Unlock()
				return nil, "", fallbackErr
			}
			if fallbackChannel != nil {
				removeOpencodeQueueEntryLocked(entry)
				opencodeQueueMu.Unlock()
				return fallbackChannel, fallbackGroup, nil
			}

			quotaParam := *param
			quotaParam.OpencodeIncludeRPMFull = true
			quotaChannel, _, quotaErr := CacheGetRandomSatisfiedChannel(&quotaParam)
			if quotaErr != nil {
				removeOpencodeQueueEntryLocked(entry)
				opencodeQueueMu.Unlock()
				return nil, "", quotaErr
			}
			if quotaChannel == nil {
				removeOpencodeQueueEntryLocked(entry)
				opencodeQueueMu.Unlock()
				return nil, "", ErrOpencodeQueueUnavailable
			}
		}
		wake := opencodeQueueWake
		opencodeQueueMu.Unlock()

		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			opencodeQueueMu.Lock()
			removeOpencodeQueueEntryLocked(entry)
			opencodeQueueMu.Unlock()
			return nil, "", ctx.Err()
		case <-wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

// TryReserveOpencodeAffinityChannel reserves the current affinity target
// without entering the shared queue. A full concurrency limit returns fast=false so
// the caller can keep the affinity target and use the normal queue.
func TryReserveOpencodeAffinityChannel(param *RetryParam) (*model.Channel, string, bool, error) {
	if param == nil || param.Ctx == nil {
		return nil, "", false, nil
	}
	selection := *param
	selection.OpencodeIncludeRPMFull = true
	channel, group, err := CacheGetRandomSatisfiedChannel(&selection)
	if err != nil || channel == nil {
		return nil, group, false, err
	}
	if IsOpencodeSessionQueueRequired(param.Ctx) {
		// Session capacity is still waiting. Do not take a request lease or
		// treat the least-loaded fallback as a successful fast path.
		return nil, group, false, nil
	}
	allowed, err := model.ReserveCodeProviderConcurrency(channel.Type, channel.Id)
	if err != nil || !allowed {
		return nil, group, false, err
	}
	param.OpencodeConcurrencyReserved = true
	param.OpencodeRPMReserved = true
	return channel, group, true, nil
}

func selectOrdinaryChannelFallback(param *RetryParam) (*model.Channel, string, error) {
	selection := *param
	selection.OpencodePool = false
	selection.OpencodeIncludeRPMFull = false
	selection.OpencodeRPMReserved = false
	selection.OpencodeConcurrencyReserved = false
	selection.ExcludeCodeProviderChannels = true
	selection.AttemptedChannelIDs = copyChannelIDs(param.AttemptedChannelIDs)

	channel, group, err := CacheGetRandomSatisfiedChannel(&selection)
	if err != nil || channel == nil {
		return channel, group, err
	}
	param.OpencodePool = false
	param.OpencodeIncludeRPMFull = false
	param.OpencodeRPMReserved = false
	param.OpencodeConcurrencyReserved = false
	param.ExcludeCodeProviderChannels = true
	return channel, group, nil
}

func queueGroupForParam(param *RetryParam) string {
	if param == nil {
		return ""
	}
	group := param.TokenGroup
	if group == "auto" && param.Ctx != nil {
		if selectedGroup := common.GetContextKeyString(param.Ctx, constant.ContextKeyAutoGroup); selectedGroup != "" {
			return selectedGroup
		}
	}
	return group
}

func isOpencodeQueueTurnLocked(entry *opencodeQueueEntry) bool {
	for _, current := range opencodeQueue {
		if current == entry {
			return true
		}
		if current.queueKey == entry.queueKey {
			return false
		}
	}
	return false
}

func newOpencodeQueueItem(param *RetryParam, inputTokens int) OpencodeQueueItem {
	userID := common.GetContextKeyInt(param.Ctx, constant.ContextKeyUserId)
	username := common.GetContextKeyString(param.Ctx, constant.ContextKeyUserName)
	if username == "" && userID > 0 {
		if user, err := model.GetUserById(userID, false); err == nil && user != nil {
			username = user.Username
		}
	}
	requestID := param.Ctx.GetString(common.RequestIdKey)
	if requestID == "" {
		requestID = fmt.Sprintf("opencode-%d", opencodeQueueSeq.Add(1))
	}
	return OpencodeQueueItem{
		ID:          requestID,
		UserID:      userID,
		Username:    username,
		Group:       queueGroupForParam(param),
		Model:       param.ModelName,
		InputTokens: max(inputTokens, 0),
		QueuedAt:    time.Now().Unix(),
		Position:    0,
	}
}

func tryReserveOpencodeChannel(param *RetryParam) (*model.Channel, string, error) {
	selection := *param
	selection.OpencodeIncludeRPMFull = false
	selection.AttemptedChannelIDs = copyChannelIDs(param.AttemptedChannelIDs)
	for {
		channel, group, err := CacheGetRandomSatisfiedChannel(&selection)
		if err != nil || channel == nil {
			return channel, group, err
		}
		if IsOpencodeSessionQueueRequired(param.Ctx) {
			return nil, group, nil
		}
		allowed, err := model.ReserveCodeProviderConcurrency(channel.Type, channel.Id)
		if err != nil {
			return nil, group, err
		}
		if allowed {
			return channel, group, nil
		}
		if rememberedOpencodeSessionChannelID(param.Ctx) == channel.Id {
			// Same active session: wait for this channel's request lease instead
			// of spinning through other channels or occupying a new session slot.
			return nil, group, nil
		}
		selection.AttemptedChannelIDs[channel.Id] = struct{}{}
	}
}

func copyChannelIDs(source map[int]struct{}) map[int]struct{} {
	copy := make(map[int]struct{}, len(source))
	for id := range source {
		copy[id] = struct{}{}
	}
	return copy
}

func removeOpencodeQueueEntryLocked(entry *opencodeQueueEntry) {
	for index, current := range opencodeQueue {
		if current != entry {
			continue
		}
		copy(opencodeQueue[index:], opencodeQueue[index+1:])
		opencodeQueue = opencodeQueue[:len(opencodeQueue)-1]
		return
	}
}

func NotifyOpencodeQueue() {
	opencodeQueueMu.Lock()
	close(opencodeQueueWake)
	opencodeQueueWake = make(chan struct{})
	opencodeQueueMu.Unlock()
}

func GetOpencodeQueueSnapshot() (*OpencodeQueueSnapshot, error) {
	opencodeQueueMu.Lock()
	items := make([]OpencodeQueueItem, len(opencodeQueue))
	keyCounts := make(map[opencodeQueueKey]int)
	for index, entry := range opencodeQueue {
		items[index] = entry.item
		items[index].Position = index + 1
		keyCounts[entry.queueKey]++
	}
	queuedCount := len(items)
	opencodeQueueMu.Unlock()

	var capacity *model.OpencodeCapacitySnapshot
	waitMinutes := 0
	if queuedCount == 0 {
		var err error
		capacity, err = model.GetCodeProviderCapacitySnapshot(common.GetTimestamp())
		if err != nil {
			return nil, err
		}
	} else {
		capacity = &model.OpencodeCapacitySnapshot{}
		for key, count := range keyCounts {
			keyCapacity, err := model.GetCodeProviderCapacitySnapshotForGroupModel(key.group, key.model, common.GetTimestamp())
			if err != nil {
				return nil, err
			}
			capacity.TotalCapacity += keyCapacity.TotalCapacity
			capacity.AvailableCapacity += keyCapacity.AvailableCapacity
			totalCapacity := keyCapacity.TotalCapacity
			if totalCapacity < operation_setting.GetOpencodeConcurrencyLimit() {
				totalCapacity = operation_setting.GetOpencodeConcurrencyLimit()
			}
			keyWaitMinutes := (count + totalCapacity - 1) / totalCapacity
			if keyWaitMinutes < 1 {
				keyWaitMinutes = 1
			}
			if keyWaitMinutes > waitMinutes {
				waitMinutes = keyWaitMinutes
			}
		}
	}
	return &OpencodeQueueSnapshot{
		TotalCapacity:        capacity.TotalCapacity,
		AvailableCapacity:    capacity.AvailableCapacity,
		QueuedCount:          queuedCount,
		EstimatedWaitMinutes: waitMinutes,
		Items:                items,
	}, nil
}
