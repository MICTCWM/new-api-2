package service

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// The admin 连接日志 panel keeps one SSE stream open instead of polling. Relay
// hot paths (one call per streamed chunk) only flag the snapshot as stale; a
// single timer goroutine builds and fans out the newest snapshot at most once
// per window, so a token-heavy request cannot turn into a per-token broadcast.
const (
	connectionLogStreamWindow = 200 * time.Millisecond
	// Buffering a few frames lets a briefly busy reader catch up before frames
	// are dropped; the payload is always a full snapshot, so a drop is harmless.
	connectionLogStreamBacklog = 4
)

var (
	connectionLogStreamDirty     atomic.Bool
	connectionLogStreamScheduled atomic.Bool
	connectionLogSubscribers     = struct {
		mu     sync.Mutex
		active map[chan []byte]struct{}
	}{active: make(map[chan []byte]struct{})}
)

// SubscribeConnectionLogs registers a live snapshot listener. The channel
// receives complete serialized snapshots; cancel unregisters and closes it.
func SubscribeConnectionLogs() (<-chan []byte, func()) {
	events := make(chan []byte, connectionLogStreamBacklog)
	connectionLogSubscribers.mu.Lock()
	connectionLogSubscribers.active[events] = struct{}{}
	connectionLogSubscribers.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			// Removal and the close are serialized by the subscriber lock: the
			// broadcaster only sends while holding it, so it can never send on a
			// closed channel.
			connectionLogSubscribers.mu.Lock()
			delete(connectionLogSubscribers.active, events)
			connectionLogSubscribers.mu.Unlock()
			close(events)
		})
	}
	return events, cancel
}

// notifyConnectionLogChange flags the live snapshot as stale. It only touches
// atomics because relay calls it once per chunk; building and serializing the
// snapshot happens on the timer goroutine.
func notifyConnectionLogChange() {
	connectionLogStreamDirty.Store(true)
	scheduleConnectionLogStream()
}

func scheduleConnectionLogStream() {
	if !connectionLogStreamScheduled.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(connectionLogStreamWindow, flushConnectionLogStream)
}

// flushConnectionLogStream publishes at most one snapshot per window. The
// snapshot is read when the window expires, so a burst of changes always
// collapses into the newest state instead of queueing stale frames.
func flushConnectionLogStream() {
	connectionLogStreamScheduled.Store(false)
	if !connectionLogStreamDirty.Swap(false) {
		return
	}
	payload, err := common.Marshal(GetConnectionLogSnapshot())
	if err != nil {
		common.SysError("failed to serialize connection log snapshot: " + err.Error())
	} else {
		broadcastConnectionLogSnapshot(payload)
	}
	// A change that landed while the snapshot was being built needs its own
	// window; without this re-arm it would wait for the next unrelated change.
	if connectionLogStreamDirty.Load() {
		scheduleConnectionLogStream()
	}
}

func broadcastConnectionLogSnapshot(payload []byte) {
	connectionLogSubscribers.mu.Lock()
	defer connectionLogSubscribers.mu.Unlock()
	for events := range connectionLogSubscribers.active {
		// Never block the relay path on a slow reader: a dropped frame only means
		// that reader waits for the next full snapshot.
		select {
		case events <- payload:
		default:
		}
	}
}
