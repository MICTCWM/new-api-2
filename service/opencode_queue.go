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

var errOpencodeQueueUnavailable = errors.New("opencode has no local quota capacity")

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
	group string
	model string
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

	entry := &opencodeQueueEntry{
		item:     newOpencodeQueueItem(param, inputTokens),
		queueKey: opencodeQueueKey{group: queueGroupForParam(param), model: param.ModelName},
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
				param.OpencodeRPMReserved = true
				opencodeQueueMu.Unlock()
				return channel, group, nil
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
				return nil, "", errOpencodeQueueUnavailable
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
		allowed, _, err := model.ReserveCodeProviderRPM(channel.Type, channel.Id, common.GetTimestamp())
		if err != nil {
			return nil, group, err
		}
		if allowed {
			return channel, group, nil
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
			if totalCapacity < operation_setting.GetOpencodeRPMLimit() {
				totalCapacity = operation_setting.GetOpencodeRPMLimit()
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
