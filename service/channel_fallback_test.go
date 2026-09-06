package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryParamStartFallback(t *testing.T) {
	t.Run("deduplicates, drops non-positive and already-attempted ids, in order", func(t *testing.T) {
		p := &RetryParam{
			AttemptedChannelIDs: map[int]struct{}{2: {}},
		}
		p.StartFallback([]int{5, 2, 0, 5, 7, -3, 2, 9})

		require.True(t, p.InFallback)
		require.True(t, p.FallbackStarted)
		require.Equal(t, 0, p.FallbackQueueIndex)
		assert.Equal(t, []int{5, 7, 9}, p.FallbackQueue)
	})

	t.Run("empty or nil input yields an empty queue but enables fallback mode", func(t *testing.T) {
		p := &RetryParam{}
		p.StartFallback(nil)

		require.True(t, p.InFallback)
		require.True(t, p.FallbackStarted)
		assert.Empty(t, p.FallbackQueue)
		assert.False(t, p.HasNextFallback())
	})

	t.Run("nil attempted map does not panic and keeps all valid ids", func(t *testing.T) {
		p := &RetryParam{}
		p.StartFallback([]int{3, 8})

		assert.Equal(t, []int{3, 8}, p.FallbackQueue)
	})
}
