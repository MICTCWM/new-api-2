package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelQueueStatsBucketAndOrder(t *testing.T) {
	nowMS := time.Now().UnixMilli()
	first := &model.ChannelQueueItem{RequestID: "a", Kind: ChannelQueueKindConcurrency, QueuedAt: nowMS - 30}
	second := &model.ChannelQueueItem{RequestID: "b", Kind: ChannelQueueKindTPM, QueuedAt: nowMS - 20}
	third := &model.ChannelQueueItem{RequestID: "c", Kind: ChannelQueueKindConcurrency, QueuedAt: nowMS - 10}

	firstHandle, err := EnqueueChannelWait(1, first)
	require.NoError(t, err)
	defer LeaveChannelQueue(firstHandle)
	secondHandle, err := EnqueueChannelWait(1, second)
	require.NoError(t, err)
	defer LeaveChannelQueue(secondHandle)
	thirdHandle, err := EnqueueChannelWait(1, third)
	require.NoError(t, err)
	defer LeaveChannelQueue(thirdHandle)

	queuedConcurrency, queuedTPM, _, items := GetChannelQueueStats(1, 2, 0, 0)
	assert.Equal(t, 2, queuedConcurrency)
	assert.Equal(t, 1, queuedTPM)
	assert.Equal(t, []string{"a", "b", "c"}, itemIDs(items))
	// Position is 1-based waiting order, oldest first.
	assert.Equal(t, 1, items[0].Position)
	assert.Equal(t, 3, items[2].Position)

	LeaveChannelQueue(secondHandle)
	queuedConcurrency, queuedTPM, _, items = GetChannelQueueStats(1, 2, 0, 0)
	assert.Equal(t, 2, queuedConcurrency)
	assert.Equal(t, 0, queuedTPM)
}

func TestChannelQueueStatsEmptyChannel(t *testing.T) {
	queuedConcurrency, queuedTPM, eta, items := GetChannelQueueStats(999999, 3, 0, 0)
	assert.Equal(t, 0, queuedConcurrency)
	assert.Equal(t, 0, queuedTPM)
	assert.Equal(t, int64(0), eta)
	assert.Empty(t, items)
}

func itemIDs(items []model.ChannelQueueItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.RequestID)
	}
	return ids
}
