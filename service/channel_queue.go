package service

import (
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Ordinary-channel limit queues (排队模式). Requests that hit a channel's
// concurrency or TPM limit while the channel runs in queue mode register here
// so the admin gauge can show the queue, and get woken when capacity frees up.
// Queue membership is stored in the main database so admin snapshots include
// waiters across API instances. The wake channel is local; polling lets waiters
// on other instances observe capacity released elsewhere.

const (
	ChannelQueueKindConcurrency = "concurrency"
	ChannelQueueKindTPM         = "tpm"
)

type channelQueueHandle struct {
	channelID  int
	entry      *model.ChannelQueueItem
	persistent *model.ChannelQueueEntry
}

var (
	channelQueueMu sync.Mutex
	channelQueues  = make(map[int][]*model.ChannelQueueItem)
	// channelQueueWake is closed on every release so waiters retry at once.
	channelQueueWake = make(chan struct{})
)

func currentChannelQueueWake() chan struct{} {
	channelQueueMu.Lock()
	defer channelQueueMu.Unlock()
	return channelQueueWake
}

// NotifyChannelLimitQueues wakes every waiting request on every channel.
// Releases are infrequent relative to queue checks, and a global wake keeps
// the release paths cheap; waiters still re-try their own channel first.
func NotifyChannelLimitQueues() {
	channelQueueMu.Lock()
	wake := channelQueueWake
	channelQueueWake = make(chan struct{})
	channelQueueMu.Unlock()
	close(wake)
}

func EnqueueChannelWait(channelID int, item *model.ChannelQueueItem) (*channelQueueHandle, error) {
	persistent, err := model.CreateChannelQueueEntry(channelID, item, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	channelQueueMu.Lock()
	defer channelQueueMu.Unlock()
	channelQueues[channelID] = append(channelQueues[channelID], item)
	return &channelQueueHandle{channelID: channelID, entry: item, persistent: persistent}, nil
}

func HasChannelQueue(channelID int) bool {
	channelQueueMu.Lock()
	localQueueHasItems := len(channelQueues[channelID]) > 0
	channelQueueMu.Unlock()
	if localQueueHasItems {
		return true
	}
	hasItems, err := model.HasChannelQueueEntries(channelID, time.Now().Unix()-60)
	if err != nil {
		common.SysError("failed to check shared channel queue: " + err.Error())
		return false
	}
	return hasItems
}

func TouchChannelQueue(handle *channelQueueHandle) error {
	if handle == nil || handle.persistent == nil {
		return nil
	}
	channelQueueMu.Lock()
	kind := handle.entry.Kind
	channelQueueMu.Unlock()
	return model.TouchChannelQueueEntry(handle.persistent.ID, kind, time.Now().Unix())
}

func SetChannelQueueKind(handle *channelQueueHandle, kind string) error {
	if handle == nil || handle.entry == nil {
		return nil
	}
	channelQueueMu.Lock()
	if handle.entry.Kind == kind {
		channelQueueMu.Unlock()
		return nil
	}
	handle.entry.Kind = kind
	channelQueueMu.Unlock()
	if handle.persistent == nil {
		return nil
	}
	return model.TouchChannelQueueEntry(handle.persistent.ID, kind, time.Now().Unix())
}

func IsChannelQueueTurn(handle *channelQueueHandle) bool {
	if handle == nil {
		return true
	}
	if handle.persistent != nil {
		first, err := model.IsChannelQueueEntryFirst(handle.persistent.ID, handle.channelID, time.Now().Unix()-60)
		if err == nil {
			return first
		}
		common.SysError("failed to check channel queue order: " + err.Error())
	}
	channelQueueMu.Lock()
	defer channelQueueMu.Unlock()
	items := channelQueues[handle.channelID]
	return len(items) == 0 || items[0] == handle.entry
}

func LeaveChannelQueue(handle *channelQueueHandle) error {
	if handle == nil {
		return nil
	}
	channelQueueMu.Lock()
	items := channelQueues[handle.channelID]
	for i, item := range items {
		if item == handle.entry {
			channelQueues[handle.channelID] = append(items[:i], items[i+1:]...)
			break
		}
	}
	if len(channelQueues[handle.channelID]) == 0 {
		delete(channelQueues, handle.channelID)
	}
	channelQueueMu.Unlock()
	var err error
	if handle.persistent != nil {
		err = model.DeleteChannelQueueEntry(handle.persistent.ID)
	}
	NotifyChannelLimitQueues()
	return err
}

// WaitForChannelQueueWake blocks until a release wakes the queue or the poll
// timeout elapses. Waiters re-check their own capacity after returning.
func WaitForChannelQueueWake(wake chan struct{}, timeout time.Duration) {
	select {
	case <-wake:
	case <-time.After(timeout):
	}
}

// CurrentChannelQueueWake returns the wake channel a waiter should select on;
// releases close it and install a fresh one.
func CurrentChannelQueueWake() chan struct{} {
	return currentChannelQueueWake()
}

// GetChannelQueueStats reports queued counts, a wave-based ETA, and the
// waiting requests ordered oldest first for the admin gauge's queue list.
// Concurrency waiters estimate the clear time from the recent average request
// duration; TPM waiters from the remaining seconds of the token window.
func GetChannelQueueStats(
	channelID int,
	concurrencyLimit int,
	activeConcurrency int,
	tpmResetAt int64,
) (
	queuedConcurrency int,
	queuedTPM int,
	etaSeconds int64,
	items []model.ChannelQueueItem,
) {
	channelQueueMu.Lock()
	localItems := make([]model.ChannelQueueItem, 0, len(channelQueues[channelID]))
	for _, item := range channelQueues[channelID] {
		localItems = append(localItems, *item)
	}
	channelQueueMu.Unlock()

	items = localItems
	if model.DB != nil {
		sharedItems, err := model.GetChannelQueueEntries(channelID, time.Now().Unix()-60)
		if err == nil {
			items = sharedItems
		} else {
			common.SysError("failed to read shared channel queue entries: " + err.Error())
		}
	}
	for i := range items {
		switch items[i].Kind {
		case ChannelQueueKindTPM:
			queuedTPM++
		default:
			queuedConcurrency++
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].QueuedAt < items[j].QueuedAt })
	for i := range items {
		items[i].Position = i + 1
	}

	if queuedConcurrency+queuedTPM == 0 {
		return queuedConcurrency, queuedTPM, 0, items
	}

	// Waves: the requests already holding concurrency slots clear a wave per
	// average duration; TPM waiters additionally wait for the token window.
	if queuedConcurrency > 0 {
		slots := concurrencyLimit
		if slots <= 0 {
			slots = 1
		}
		inProgress := max(activeConcurrency, 0)
		waves := (queuedConcurrency + inProgress + slots - 1) / slots
		if waves < 1 {
			waves = 1
		}
		avgMS := averageChannelCompletionMS(channelID).Milliseconds()
		if avgMS <= 250 {
			avgMS = 250
		}
		etaSeconds = int64(avgMS) * int64(waves) / 1000
	}
	if queuedTPM > 0 && tpmResetAt > 0 {
		// TPM only recovers when the one-minute window resets.
		if tpmETA := tpmResetAt - time.Now().Unix(); tpmETA > etaSeconds {
			etaSeconds = tpmETA
		}
	}
	return queuedConcurrency, queuedTPM, etaSeconds, items
}

// averageChannelCompletionMS estimates how long one queued wave needs from
// recently completed connections on this node; 20s when unknown.
func averageChannelCompletionMS(channelID int) time.Duration {
	if recent := RecentChannelCompletionDuration(channelID); recent > 0 {
		return recent
	}
	return 20 * time.Second
}
