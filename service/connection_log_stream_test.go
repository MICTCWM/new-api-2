package service

import (
	"bytes"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// requireConnectionLogFrameContaining reads frames until one mentions the
// request, because a frame armed by another test's change may still be in
// flight when this subscription starts.
func requireConnectionLogFrameContaining(t *testing.T, events <-chan []byte, requestID string) []byte {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case frame, ok := <-events:
			require.True(t, ok, "the subscription must stay open until it is cancelled")
			if bytes.Contains(frame, []byte(requestID)) {
				return frame
			}
		case <-deadline:
			t.Fatalf("no connection log frame containing %q arrived", requestID)
		}
	}
}

func TestConnectionLogStreamPublishesNewConnections(t *testing.T) {
	events, unsubscribe := SubscribeConnectionLogs()
	t.Cleanup(unsubscribe)

	requestID := "conn-stream-register"
	cleanupConnection(t, requestID)

	RegisterConnection(newTestRelayInfo(requestID, true), "alice", 640)

	frame := requireConnectionLogFrameContaining(t, events, requestID)

	var snapshot ConnectionLogSnapshot
	require.NoError(t, common.Unmarshal(frame, &snapshot))
	item := findItem(snapshot.Items, requestID)
	require.NotNil(t, item, "the pushed frame must be a full snapshot that already lists the new connection")
	require.Equal(t, ConnectionStateWaitingFirstToken, item.State)
	require.EqualValues(t, 640, item.InputTokens)
	require.True(t, item.Active)
	require.Equal(t, common.NodeName, snapshot.NodeName)
	require.NotZero(t, snapshot.ServerTime)
}

func TestConnectionLogStreamClosesOnCancel(t *testing.T) {
	events, unsubscribe := SubscribeConnectionLogs()

	unsubscribe()

	// A cancelled subscriber must observe a closed channel so an open stream
	// handler stops reading instead of waiting for the next frame.
	for {
		select {
		case _, ok := <-events:
			require.False(t, ok, "cancel must close the subscriber channel")
			return
		case <-time.After(2 * time.Second):
			t.Fatal("cancel did not close the subscriber channel")
		}
	}
}
