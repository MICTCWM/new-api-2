package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterChannelIDsByFallback(t *testing.T) {
	tests := []struct {
		name        string
		channelIDs  []int
		fallbackSet map[int]struct{}
		want        []int
	}{
		{
			name:        "empty fallback set filters nothing",
			channelIDs:  []int{1, 2, 3},
			fallbackSet: map[int]struct{}{},
			want:        []int{1, 2, 3},
		},
		{
			name:        "partial hit keeps only non-fallback channels",
			channelIDs:  []int{1, 2, 3, 4, 5},
			fallbackSet: map[int]struct{}{2: {}, 4: {}},
			want:        []int{1, 3, 5},
		},
		{
			name:        "all hit excludes every channel",
			channelIDs:  []int{7, 9},
			fallbackSet: map[int]struct{}{7: {}, 9: {}},
			want:        []int{},
		},
		{
			name:        "nil channelIDs returns nil",
			channelIDs:  nil,
			fallbackSet: map[int]struct{}{1: {}},
			want:        nil,
		},
		{
			name:        "empty channelIDs returns empty",
			channelIDs:  []int{},
			fallbackSet: map[int]struct{}{1: {}},
			want:        []int{},
		},
		{
			name:        "nil fallback set returns input unchanged",
			channelIDs:  []int{1, 2},
			fallbackSet: nil,
			want:        []int{1, 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterChannelIDsByFallback(tt.channelIDs, tt.fallbackSet)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFilterAbilitiesByFallback(t *testing.T) {
	// filterAbilitiesByFallback reads the fallback marking set from the memory
	// cache path (GetFallbackChannelIDSet). Drive it without a database by
	// enabling the memory cache and populating the cached set directly.
	original := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	defer func() { common.MemoryCacheEnabled = original }()

	abilities := func(ids ...int) []Ability {
		out := make([]Ability, 0, len(ids))
		for _, id := range ids {
			out = append(out, Ability{ChannelId: id})
		}
		return out
	}

	tests := []struct {
		name        string
		abilities   []Ability
		fallbackSet map[int]struct{}
		want        []int
	}{
		{
			name:        "empty fallback set filters nothing",
			abilities:   abilities(1, 2, 3),
			fallbackSet: map[int]struct{}{},
			want:        []int{1, 2, 3},
		},
		{
			name:        "partial hit keeps only non-fallback abilities",
			abilities:   abilities(1, 2, 3, 4, 5),
			fallbackSet: map[int]struct{}{2: {}, 4: {}},
			want:        []int{1, 3, 5},
		},
		{
			name:        "all hit excludes every ability",
			abilities:   abilities(7, 9),
			fallbackSet: map[int]struct{}{7: {}, 9: {}},
			want:        []int{},
		},
		{
			name:        "nil abilities returns nil with no error",
			abilities:   nil,
			fallbackSet: map[int]struct{}{1: {}},
			want:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFallbackChannelIDSetForTest(t, tt.fallbackSet)
			got, err := filterAbilitiesByFallback(tt.abilities)
			require.NoError(t, err)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			gotIDs := make([]int, 0, len(got))
			for _, a := range got {
				gotIDs = append(gotIDs, a.ChannelId)
			}
			assert.Equal(t, tt.want, gotIDs)
		})
	}
}

// setFallbackChannelIDSetForTest swaps the memory-cache fallback set under the
// channel sync lock so filterAbilitiesByFallback sees the intended marking set.
func setFallbackChannelIDSetForTest(t *testing.T, set map[int]struct{}) {
	t.Helper()
	channelSyncLock.Lock()
	fallbackChannelIDSet = set
	channelSyncLock.Unlock()
}

func TestGetFallbackSequenceIDs(t *testing.T) {
	tests := []struct {
		name             string
		fallbackSequence string
		want             []int
	}{
		{
			name:             "empty string returns empty slice",
			fallbackSequence: "",
			want:             []int{},
		},
		{
			name:             "parses and preserves order",
			fallbackSequence: "3,5,7",
			want:             []int{3, 5, 7},
		},
		{
			name:             "trims surrounding whitespace",
			fallbackSequence: " 3 , 5 ",
			want:             []int{3, 5},
		},
		{
			name:             "skips invalid and non-positive entries",
			fallbackSequence: "3,-1,abc,0,5",
			want:             []int{3, 5},
		},
		{
			name:             "deduplicates keeping first occurrence order",
			fallbackSequence: "3,3,5,3",
			want:             []int{3, 5},
		},
		{
			name:             "unset field returns empty slice",
			fallbackSequence: "",
			want:             []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{FallbackSequence: tt.fallbackSequence}
			got := channel.GetFallbackSequenceIDs()
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetFallbackSequenceIDsDoesNotShareMutableState(t *testing.T) {
	channel := &Channel{FallbackSequence: "3,5,7"}

	first := channel.GetFallbackSequenceIDs()
	require.Equal(t, []int{3, 5, 7}, first)
	first[0] = 999

	// A second call must not observe the mutation applied to the first slice.
	second := channel.GetFallbackSequenceIDs()
	assert.Equal(t, []int{3, 5, 7}, second)
}

func TestBuildFallbackChannelIDSet(t *testing.T) {
	t.Run("aggregates fallback sequences across channels", func(t *testing.T) {
		channels := []*Channel{
			{FallbackSequence: "3,5"},
			{FallbackSequence: "7"},
			{FallbackSequence: "5,9,3"},
		}
		set := buildFallbackChannelIDSet(channels)
		require.Equal(t, map[int]struct{}{3: {}, 5: {}, 7: {}, 9: {}}, set)
	})

	t.Run("empty when no fallback sequence is set", func(t *testing.T) {
		channels := []*Channel{
			{FallbackSequence: ""},
			{FallbackSequence: " "},
		}
		set := buildFallbackChannelIDSet(channels)
		require.NotNil(t, set)
		assert.Empty(t, set)
	})

	t.Run("empty when channels list is empty", func(t *testing.T) {
		set := buildFallbackChannelIDSet(nil)
		require.NotNil(t, set)
		assert.Empty(t, set)
	})
}
