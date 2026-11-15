package service

import (
	"fmt"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func newTestRelayInfo(requestID string, stream bool) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RequestId:       requestID,
		UserId:          7,
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		IsStream:        stream,
		StartTime:       time.Now().Add(-2 * time.Second),
	}
}

func findItem(items []ConnectionLogItem, requestID string) *ConnectionLogItem {
	for i := range items {
		if items[i].RequestID == requestID {
			return &items[i]
		}
	}
	return nil
}

func cleanupConnection(t *testing.T, requestID string) {
	t.Helper()
	t.Cleanup(func() {
		connectionLogs.Delete(requestID)
	})
}

func TestRegisterConnectionExposesInputTokensBeforeOutput(t *testing.T) {
	requestID := "conn-register"
	cleanupConnection(t, requestID)

	RegisterConnection(newTestRelayInfo(requestID, true), "alice", 1200)

	snapshot := GetConnectionLogSnapshot()
	item := findItem(snapshot.Items, requestID)
	require.NotNil(t, item)
	require.Equal(t, ConnectionStateWaitingFirstToken, item.State)
	require.True(t, item.Active)
	require.Equal(t, int64(1200), item.InputTokens)
	require.Nil(t, item.OutputTokens, "output tokens must stay hidden until the request finishes")
	require.Nil(t, item.TTFTMS, "TTFT must stay empty before the first token")
	require.EqualValues(t, 1, snapshot.Concurrent)
	require.EqualValues(t, 1200, snapshot.TotalInputTokens)
	require.NotEmpty(t, snapshot.ServerTime)
}

func TestConnectionStateTransitions(t *testing.T) {
	requestID := "conn-states"
	cleanupConnection(t, requestID)

	RegisterConnection(newTestRelayInfo(requestID, true), "alice", 100)
	require.Equal(t, ConnectionStateWaitingFirstToken, findItem(GetConnectionLogSnapshot().Items, requestID).State)

	MarkConnectionFirstToken(requestID)
	AddConnectionChunk(requestID)
	AddConnectionChunk(requestID)
	item := findItem(GetConnectionLogSnapshot().Items, requestID)
	require.Equal(t, ConnectionStateStreaming, item.State)
	require.NotNil(t, item.TTFTMS)
	require.GreaterOrEqual(t, *item.TTFTMS, int64(0))
	require.EqualValues(t, 2, item.StreamChunks)
	require.Nil(t, item.OutputTokens)
	// Repeated first-token marks must not move TTFT backwards.
	ttft := *item.TTFTMS
	MarkConnectionFirstToken(requestID)
	require.Equal(t, ttft, *findItem(GetConnectionLogSnapshot().Items, requestID).TTFTMS)

	SettleConnectionTokens(requestID, 1400, 320)
	item = findItem(GetConnectionLogSnapshot().Items, requestID)
	require.Nil(t, item.OutputTokens, "settling alone must not publish output tokens")
	require.EqualValues(t, 1400, item.InputTokens)

	FinishConnection(requestID)
	item = findItem(GetConnectionLogSnapshot().Items, requestID)
	require.Equal(t, ConnectionStateCompleted, item.State)
	require.False(t, item.Active)
	require.NotNil(t, item.FinishedAt)
	require.Greater(t, item.DurationMS, int64(0))
	require.NotNil(t, item.OutputTokens, "output tokens surface once the request finished")
	require.EqualValues(t, 320, *item.OutputTokens)

	snapshot := GetConnectionLogSnapshot()
	require.Zero(t, snapshot.Concurrent)
	require.Zero(t, snapshot.TotalInputTokens)
}

func TestUpdateConnectionOutputTextRecountsWholeTextForCadence(t *testing.T) {
	requestID := "conn-output-cadence"
	cleanupConnection(t, requestID)

	RegisterConnection(newTestRelayInfo(requestID, true), "alice", 100)
	MarkConnectionFirstToken(requestID)

	record := lookupConnection(requestID)
	require.NotNil(t, record)
	record.lastOutputCountAt = time.Now().Add(-time.Second)
	UpdateConnectionOutputText(requestID, "The quick brown fox", "gpt-4o", 0)

	// Force the next update past the 100ms throttle without sleeping.
	record.lastOutputCountAt = time.Now().Add(-time.Second)
	UpdateConnectionOutputText(requestID, "The quick brown fox jumps over the lazy dog", "gpt-4o", 0)

	item := findItem(GetConnectionLogSnapshot().Items, requestID)
	require.NotNil(t, item)
	require.NotNil(t, item.OutputTokens)
	// The whole-text recount must match the tokenizer, not a suffix delta that
	// loses the merge at the slice boundary.
	require.EqualValues(t, CountTextToken("The quick brown fox jumps over the lazy dog", "gpt-4o"), *item.OutputTokens)
	require.Len(t, item.OutputCadence, connectionOutputCadenceBuckets)
	var cadenceTotal int64
	for _, tokens := range item.OutputCadence {
		cadenceTotal += tokens
	}
	require.EqualValues(t, *item.OutputTokens, cadenceTotal)
}

func TestSettledUsageWinsOverLateOutputEstimate(t *testing.T) {
	requestID := "conn-settled-wins"
	cleanupConnection(t, requestID)

	RegisterConnection(newTestRelayInfo(requestID, true), "alice", 100)
	MarkConnectionFirstToken(requestID)
	record := lookupConnection(requestID)
	require.NotNil(t, record)
	record.lastOutputCountAt = time.Now().Add(-time.Second)
	UpdateConnectionOutputText(requestID, "a short estimate", "gpt-4o", 0)

	SettleConnectionTokens(requestID, 100, 320)

	// A late stream update must not replace the real upstream usage.
	record.lastOutputCountAt = time.Now().Add(-time.Second)
	UpdateConnectionOutputText(requestID, "a much longer late estimate", "gpt-4o", 0)
	FinishConnection(requestID)

	item := findItem(GetConnectionLogSnapshot().Items, requestID)
	require.NotNil(t, item)
	require.NotNil(t, item.OutputTokens)
	require.EqualValues(t, 320, *item.OutputTokens)
}

func TestNonStreamConnectionHasNoTTFT(t *testing.T) {
	requestID := "conn-non-stream"
	cleanupConnection(t, requestID)

	RegisterConnection(newTestRelayInfo(requestID, false), "bob", 800)
	MarkConnectionFirstToken(requestID)
	SetConnectionChannel(requestID, 42)

	item := findItem(GetConnectionLogSnapshot().Items, requestID)
	require.Equal(t, ConnectionStateWaitingResponse, item.State)
	require.Nil(t, item.TTFTMS)
	require.Nil(t, item.FirstTokenAt)
	require.EqualValues(t, 42, item.ChannelID)
	require.Equal(t, "bob", item.Username)
	require.Equal(t, "gpt-test", item.Model)
}

func TestSnapshotOrdersOldestConnectionFirst(t *testing.T) {
	first, second := "conn-order-a", "conn-order-b"
	cleanupConnection(t, first)
	cleanupConnection(t, second)

	infoA := newTestRelayInfo(first, true)
	infoA.StartTime = time.Now().Add(-3 * time.Second)
	infoB := newTestRelayInfo(second, true)
	infoB.StartTime = time.Now().Add(-1 * time.Second)
	RegisterConnection(infoA, "alice", 10)
	RegisterConnection(infoB, "bob", 20)

	items := GetConnectionLogSnapshot().Items
	require.Equal(t, first, items[0].RequestID, "newcomers must append on the right")
	require.Equal(t, second, items[1].RequestID)
}

func TestPruneDropsExpiredEntries(t *testing.T) {
	expired := "conn-expired"
	expiredAt := time.Now().Add(-2 * time.Minute).UnixMilli()
	expiredRecord := &connectionRecord{requestID: expired, startedAt: expiredAt}
	expiredRecord.finishedAt.Store(expiredAt)
	connectionLogs.Store(expired, expiredRecord)
	t.Cleanup(func() { connectionLogs.Delete(expired) })

	pruneConnectionLogs()
	require.Nil(t, lookupConnection(expired))
	require.Nil(t, findItem(GetConnectionLogSnapshot().Items, expired))
}

func TestPruneCapsFinishedEntries(t *testing.T) {
	now := time.Now()
	ids := make([]string, 0, connectionLogMaxItems+5)
	for i := 0; i < connectionLogMaxItems+5; i++ {
		requestID := fmt.Sprintf("conn-cap-%d", i)
		ids = append(ids, requestID)
		record := &connectionRecord{requestID: requestID, startedAt: now.Add(-time.Duration(i) * time.Millisecond).UnixMilli()}
		// Finished inside the retention window, oldest first, so only the cap evicts them.
		record.finishedAt.Store(now.Add(-time.Duration(connectionLogMaxItems+5-i) * time.Millisecond).UnixMilli())
		connectionLogs.Store(requestID, record)
	}
	t.Cleanup(func() {
		for _, requestID := range ids {
			connectionLogs.Delete(requestID)
		}
	})

	pruneConnectionLogs()

	items := GetConnectionLogSnapshot().Items
	require.Len(t, items, connectionLogMaxItems)
	require.Nil(t, findItem(items, "conn-cap-0"), "oldest finished entry must be evicted")
	require.NotNil(t, findItem(items, "conn-cap-204"))
}

func TestConcurrencyStatsCountOnlyOpenConnections(t *testing.T) {
	open, done := "conn-stat-open", "conn-stat-done"
	cleanupConnection(t, open)
	cleanupConnection(t, done)

	RegisterConnection(newTestRelayInfo(open, true), "alice", 500)
	RegisterConnection(newTestRelayInfo(done, false), "bob", 900)
	FinishConnection(done)

	snapshot := GetConnectionLogSnapshot()
	require.EqualValues(t, 1, snapshot.Concurrent)
	require.EqualValues(t, 500, snapshot.TotalInputTokens, "closed connections must leave the totals")
	require.Len(t, snapshot.Items, 2, "finished connections stay listed during retention")
}

func TestConnectionHooksIgnoreUnknownRequests(t *testing.T) {
	const requestID = "conn-unknown"
	RegisterConnection(nil, "alice", 10)
	RegisterConnection(&relaycommon.RelayInfo{}, "alice", 10)
	SetConnectionChannel(requestID, 1)
	MarkConnectionFirstToken(requestID)
	AddConnectionChunk(requestID)
	SettleConnectionTokens(requestID, 1, 2)
	FinishConnection(requestID)

	require.Nil(t, findItem(GetConnectionLogSnapshot().Items, requestID))
}
