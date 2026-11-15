package service

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Live relay connections are tracked per node in memory: the admin 连接日志 panel
// polls a snapshot, so entries only need to outlive the request itself by a short
// retention window. Multi-instance deployments therefore show one node's traffic.
const (
	connectionLogRetention = 60 * time.Second
	connectionLogMaxItems  = 200
	connectionLogPruneEach = 256
	// How often an open stream republishes its estimated output tokens.
	connectionOutputCountInterval = 100 * time.Millisecond
)

// Connection states. 正在连接 covers waiting_first_token / streaming (stream)
// and waiting_response (non-stream); 完成连接 covers finished.
const (
	ConnectionStateWaitingFirstToken = "waiting_first_token"
	ConnectionStateStreaming         = "streaming"
	ConnectionStateWaitingResponse   = "waiting_response"
	ConnectionStateCompleted         = "completed"
)

type connectionRecord struct {
	requestID            string
	username             string
	userID               int
	group                string
	model                string
	stream               bool
	startedAt            int64 // unix ms, node clock
	channelID            atomic.Int64
	firstToken           atomic.Int64 // unix ms, 0 until the first token
	chunks               atomic.Int64
	input                atomic.Int64
	output               atomic.Int64
	outputVisible        atomic.Bool
	outputCountMu        sync.Mutex
	lastOutputCountAt    time.Time
	lastOutputTextLength int
	lastOutputExtraCount int
	// settled marks that upstream usage arrived; it replaces the live estimate.
	settled    atomic.Bool
	finishedAt atomic.Int64 // unix ms, 0 while the connection is open
	// retryCount counts same-channel retries spent on this request; retryWaitUntil
	// is the unix ms deadline of the current backoff, zero outside a wait.
	retryCount     atomic.Int64
	retryWaitUntil atomic.Int64
}

type ConnectionLogItem struct {
	RequestID    string `json:"request_id"`
	Username     string `json:"username"`
	UserID       int    `json:"user_id"`
	Group        string `json:"group"`
	Model        string `json:"model"`
	Stream       bool   `json:"stream"`
	ChannelID    int64  `json:"channel_id"`
	State        string `json:"state"`
	StartedAt    int64  `json:"started_at"`
	FirstTokenAt *int64 `json:"first_token_at"`
	FinishedAt   *int64 `json:"finished_at"`
	TTFTMS       *int64 `json:"ttft_ms"`
	DurationMS   int64  `json:"duration_ms"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"` // nil until stream text or settled usage exists
	StreamChunks int64  `json:"stream_chunks"`
	Active       bool   `json:"active"`
	RetryCount   int    `json:"retry_count"`
	// RetryWaitMS is the remaining backoff before the next attempt, zero when
	// the connection is not waiting to retry.
	RetryWaitMS int64 `json:"retry_wait_ms"`
}

type ConnectionLogSnapshot struct {
	Concurrent       int64               `json:"concurrent"`
	TotalInputTokens int64               `json:"total_input_tokens"`
	ServerTime       int64               `json:"server_time"`
	NodeName         string              `json:"node_name"`
	Items            []ConnectionLogItem `json:"items"`
}

type ConnectionLogProgressItem struct {
	RequestID    string `json:"request_id"`
	State        string `json:"state"`
	FirstTokenAt *int64 `json:"first_token_at"`
	FinishedAt   *int64 `json:"finished_at"`
	TTFTMS       *int64 `json:"ttft_ms"`
	DurationMS   int64  `json:"duration_ms"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	Active       bool   `json:"active"`
	RetryCount   int    `json:"retry_count"`
	RetryWaitMS  int64  `json:"retry_wait_ms"`
}

type ConnectionLogProgressSnapshot struct {
	Concurrent       int64                       `json:"concurrent"`
	TotalInputTokens int64                       `json:"total_input_tokens"`
	ServerTime       int64                       `json:"server_time"`
	Items            []ConnectionLogProgressItem `json:"items"`
}

var (
	connectionLogs   sync.Map // requestID -> *connectionRecord
	connectionLogSeq atomic.Uint64
)

func connectionLogNowMS() int64 {
	return time.Now().UnixMilli()
}

// RegisterConnection starts tracking an in-flight relay request. Input tokens are
// known from the moment the request is parsed, so they show up immediately.
func RegisterConnection(relayInfo *relaycommon.RelayInfo, username string, inputTokens int) {
	if relayInfo == nil || relayInfo.RequestId == "" {
		return
	}
	record := &connectionRecord{
		requestID: relayInfo.RequestId,
		username:  username,
		userID:    relayInfo.UserId,
		group:     relayInfo.UsingGroup,
		model:     relayInfo.OriginModelName,
		stream:    relayInfo.IsStream,
		startedAt: relayInfo.StartTime.UnixMilli(),
	}
	record.input.Store(int64(inputTokens))
	connectionLogs.Store(record.requestID, record)

	if connectionLogSeq.Add(1)%connectionLogPruneEach == 0 {
		pruneConnectionLogs()
	}
}

func lookupConnection(requestID string) *connectionRecord {
	if requestID == "" {
		return nil
	}
	value, ok := connectionLogs.Load(requestID)
	if !ok {
		return nil
	}
	record, ok := value.(*connectionRecord)
	if !ok {
		return nil
	}
	return record
}

// SetConnectionChannel records the channel selected for the attempt, which may
// change on auto-group retries; the last one before settlement is what shows.
func SetConnectionChannel(requestID string, channelID int) {
	if record := lookupConnection(requestID); record != nil {
		record.channelID.Store(int64(channelID))
	}
}

// MarkConnectionFirstToken flips 等待首 token into 正在流式输出.
func MarkConnectionFirstToken(requestID string) {
	record := lookupConnection(requestID)
	if record == nil {
		return
	}
	record.firstToken.CompareAndSwap(0, connectionLogNowMS())
}

func AddConnectionChunk(requestID string) {
	if record := lookupConnection(requestID); record != nil {
		record.chunks.Add(1)
	}
}

// SettleConnectionTokens records the real usage once upstream reported it.
// The panel replaces the live estimate with final usage after settlement.
func SettleConnectionTokens(requestID string, inputTokens, outputTokens int) {
	record := lookupConnection(requestID)
	if record == nil {
		return
	}
	if inputTokens > 0 {
		record.input.Store(int64(inputTokens))
	}
	record.output.Store(int64(outputTokens))
	record.settled.Store(true)
}

// UpdateConnectionOutputText publishes an estimated output-token count while a
// stream is active. The final upstream usage replaces this estimate at settle.
func UpdateConnectionOutputText(requestID, text, model string, extraTokens int) {
	if text == "" {
		return
	}
	record := lookupConnection(requestID)
	if record == nil {
		return
	}

	record.outputCountMu.Lock()
	defer record.outputCountMu.Unlock()
	// A short window keeps the estimate cheap while still feeding the panel
	// often enough for the counter to grow continuously.
	if time.Since(record.lastOutputCountAt) < connectionOutputCountInterval {
		return
	}
	record.lastOutputCountAt = time.Now()
	if len(text) < record.lastOutputTextLength {
		record.lastOutputTextLength = 0
	}
	newText := text[record.lastOutputTextLength:]
	record.lastOutputTextLength = len(text)
	record.outputVisible.Store(true)
	newTokens := CountTextToken(newText, model)
	if newTokens < 0 {
		newTokens = 0
	}
	newTokens += extraTokens - record.lastOutputExtraCount
	record.lastOutputExtraCount = extraTokens
	if newTokens > 0 {
		record.output.Add(int64(newTokens))
	}
}

// FinishConnection closes the record; it stays visible for the retention window
// so the panel can still show the completed connection.
// MarkConnectionRetry records a same-channel retry and the backoff deadline, so
// the live connection view can show that the request is still alive and waiting.
func MarkConnectionRetry(requestID string, count int, waitUntilMS int64) {
	record := lookupConnection(requestID)
	if record == nil {
		return
	}
	record.retryCount.Store(int64(count))
	record.retryWaitUntil.Store(waitUntilMS)
}

// ClearConnectionRetryWait ends the retry backoff for the next attempt.
func ClearConnectionRetryWait(requestID string) {
	record := lookupConnection(requestID)
	if record == nil {
		return
	}
	record.retryWaitUntil.Store(0)
}

func FinishConnection(requestID string) {
	record := lookupConnection(requestID)
	if record == nil {
		return
	}
	record.finishedAt.CompareAndSwap(0, connectionLogNowMS())
}

func (record *connectionRecord) state() (string, bool) {
	if record.finishedAt.Load() != 0 {
		return ConnectionStateCompleted, false
	}
	if !record.stream {
		return ConnectionStateWaitingResponse, true
	}
	if record.firstToken.Load() != 0 {
		return ConnectionStateStreaming, true
	}
	return ConnectionStateWaitingFirstToken, true
}

func (record *connectionRecord) toItem(nowMS int64) ConnectionLogItem {
	state, active := record.state()
	item := ConnectionLogItem{
		RequestID:    record.requestID,
		Username:     record.username,
		UserID:       record.userID,
		Group:        record.group,
		Model:        record.model,
		Stream:       record.stream,
		ChannelID:    record.channelID.Load(),
		State:        state,
		StartedAt:    record.startedAt,
		InputTokens:  record.input.Load(),
		StreamChunks: record.chunks.Load(),
		Active:       active,
		RetryCount:   int(record.retryCount.Load()),
	}
	if waitUntil := record.retryWaitUntil.Load(); active && waitUntil > nowMS {
		item.RetryWaitMS = waitUntil - nowMS
	}
	end := nowMS
	if finished := record.finishedAt.Load(); finished != 0 {
		end = finished
		item.FinishedAt = &finished
	}
	if end > record.startedAt {
		item.DurationMS = end - record.startedAt
	}
	if first := record.firstToken.Load(); first != 0 && record.stream {
		item.FirstTokenAt = &first
		if first >= record.startedAt {
			ttft := first - record.startedAt
			item.TTFTMS = &ttft
		}
	}
	// Streaming output is estimated from received text and replaced by upstream
	// usage at settlement. Non-stream output remains hidden until completion.
	if record.outputVisible.Load() || (record.finishedAt.Load() != 0 && record.settled.Load()) {
		output := record.output.Load()
		item.OutputTokens = &output
	}
	return item
}

func pruneConnectionLogs() {
	nowMS := connectionLogNowMS()
	cutoff := nowMS - connectionLogRetention.Milliseconds()
	removable := make([]*connectionRecord, 0, 32)
	var live int
	connectionLogs.Range(func(_, value any) bool {
		record, ok := value.(*connectionRecord)
		if !ok {
			return true
		}
		live++
		finished := record.finishedAt.Load()
		if finished == 0 {
			return true
		}
		if finished < cutoff {
			connectionLogs.Delete(record.requestID)
			live--
			return true
		}
		removable = append(removable, record)
		return true
	})
	// Cap on a flood of long-running streams: drop the oldest finished entries.
	excess := live - connectionLogMaxItems
	if excess <= 0 {
		return
	}
	sort.Slice(removable, func(i, j int) bool {
		return removable[i].finishedAt.Load() < removable[j].finishedAt.Load()
	})
	if excess > len(removable) {
		excess = len(removable)
	}
	for _, record := range removable[:excess] {
		connectionLogs.Delete(record.requestID)
	}
}

// GetConnectionLogSnapshot returns the live concurrency count, the summed input
// tokens of the connections still open, and per-connection detail ordered oldest
// first so the UI can append newcomers on the right.
func GetConnectionLogSnapshot() *ConnectionLogSnapshot {
	pruneConnectionLogs()
	nowMS := connectionLogNowMS()
	snapshot := &ConnectionLogSnapshot{
		ServerTime: nowMS,
		NodeName:   common.NodeName,
		Items:      make([]ConnectionLogItem, 0, connectionLogMaxItems),
	}
	connectionLogs.Range(func(_, value any) bool {
		record, ok := value.(*connectionRecord)
		if !ok {
			return true
		}
		item := record.toItem(nowMS)
		snapshot.Items = append(snapshot.Items, item)
		if item.Active {
			snapshot.Concurrent++
			snapshot.TotalInputTokens += item.InputTokens
		}
		return true
	})
	sort.Slice(snapshot.Items, func(i, j int) bool {
		if snapshot.Items[i].StartedAt != snapshot.Items[j].StartedAt {
			return snapshot.Items[i].StartedAt < snapshot.Items[j].StartedAt
		}
		return snapshot.Items[i].RequestID < snapshot.Items[j].RequestID
	})
	return snapshot
}

// RecentChannelCompletionDuration averages the durations of recently finished
// connections on the channel, giving limit queues a wave-based ETA. Zero when
// nothing finished recently.
func RecentChannelCompletionDuration(channelID int) time.Duration {
	if channelID <= 0 {
		return 0
	}
	nowMS := connectionLogNowMS()
	var count int
	var totalDurationMS int64
	connectionLogs.Range(func(_, value any) bool {
		record, ok := value.(*connectionRecord)
		if !ok {
			return true
		}
		finished := record.finishedAt.Load()
		if finished == 0 || int(record.channelID.Load()) != channelID {
			return true
		}
		// Ignore stale records older than the retention window.
		if nowMS-finished > connectionLogRetention.Milliseconds() {
			return true
		}
		if duration := finished - record.startedAt; duration > 0 {
			totalDurationMS += duration
			count++
		}
		return true
	})
	if count == 0 {
		return 0
	}
	return time.Duration(totalDurationMS/int64(count)) * time.Millisecond
}

// GetConnectionLogProgressSnapshot returns the small set of fields that change
// during a request, allowing the UI to refresh live progress separately from the
// five-second log-list refresh.
func GetConnectionLogProgressSnapshot() *ConnectionLogProgressSnapshot {
	snapshot := GetConnectionLogSnapshot()
	progress := &ConnectionLogProgressSnapshot{
		Concurrent:       snapshot.Concurrent,
		TotalInputTokens: snapshot.TotalInputTokens,
		ServerTime:       snapshot.ServerTime,
		Items:            make([]ConnectionLogProgressItem, 0, len(snapshot.Items)),
	}
	for _, item := range snapshot.Items {
		progress.Items = append(progress.Items, ConnectionLogProgressItem{
			RequestID:    item.RequestID,
			State:        item.State,
			FirstTokenAt: item.FirstTokenAt,
			FinishedAt:   item.FinishedAt,
			TTFTMS:       item.TTFTMS,
			DurationMS:   item.DurationMS,
			InputTokens:  item.InputTokens,
			OutputTokens: item.OutputTokens,
			Active:       item.Active,
			RetryCount:   item.RetryCount,
			RetryWaitMS:  item.RetryWaitMS,
		})
	}
	return progress
}
