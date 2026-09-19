package service

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useOpencodeSessionCapacityEnv(t *testing.T, limit int) *gorm.DB {
	t.Helper()
	require.NoError(t, resetOpencodeSessionStateForTest())
	db := useChannelAffinityRoutingDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	previousLogDB := model.LOG_DB
	model.LOG_DB = db
	previousLimit := operation_setting.GetOpencodeConcurrencyLimit()
	require.NoError(t, operation_setting.SetOpencodeConcurrencyLimit(strconv.Itoa(limit)))
	previousTimeout := opencodeSessionQueueTimeout
	previousWindow := opencodeActiveWindowSeconds
	previousWait := opencodeSessionWaitQueue
	opencodeSessionWaitQueue = nil
	t.Cleanup(func() {
		opencodeSessionWaitMu.Lock()
		opencodeSessionWaitQueue = previousWait
		opencodeSessionWaitMu.Unlock()
		opencodeSessionQueueTimeout = previousTimeout
		opencodeActiveWindowSeconds = previousWindow
		model.LOG_DB = previousLogDB
		require.NoError(t, operation_setting.SetOpencodeConcurrencyLimit(strconv.Itoa(previousLimit)))
	})
	return db
}

func createOpencodeCapacityChannels(t *testing.T, db *gorm.DB, n int) []*model.Channel {
	t.Helper()
	channels := make([]*model.Channel, n)
	for i := 0; i < n; i++ {
		channel := &model.Channel{
			Type:   constant.ChannelTypeOpencode,
			Status: common.ChannelStatusEnabled,
			Key:    fmt.Sprintf("opencode-session-cap-%d-%d", time.Now().UnixNano(), i),
			Models: model.OpencodeModel,
			Group:  "default",
		}
		require.NoError(t, db.Create(channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group:     "default",
			Model:     model.OpencodeModel,
			ChannelId: channel.Id,
			Enabled:   true,
		}).Error)
		channels[i] = channel
	}
	model.InitChannelCache()
	return channels
}

func newOpencodeCapacityContext(t *testing.T, conversation string) *gin.Context {
	t.Helper()
	return newOpencodeSessionTestContext(t, "", "X-Session-Id", conversation)
}

func newOpencodeCapacityParam(c *gin.Context) *RetryParam {
	return &RetryParam{
		Ctx:                 c,
		TokenGroup:          "default",
		ModelName:           model.OpencodeModel,
		RequestPath:         "/v1/chat/completions",
		Retry:               common.GetPointer(0),
		OpencodePool:        true,
		AttemptedChannelIDs: make(map[int]struct{}),
	}
}

func selectOpencodeCapacityChannel(t *testing.T, c *gin.Context) (*model.Channel, bool) {
	t.Helper()
	channel, queueRequired, err := SelectOpencodeSessionChannel(c, "default", model.OpencodeModel, "/v1/chat/completions", nil, map[int]struct{}{})
	require.NoError(t, err)
	require.NotNil(t, channel)
	return channel, queueRequired
}

func fillOpencodeActiveSessions(t *testing.T, channelID, n int, prefix string) []string {
	t.Helper()
	now := time.Now().Unix()
	keys := make([]string, n)
	for i := 0; i < n; i++ {
		sessionKey := opencodeSessionKey(fmt.Sprintf("%s-%d-%d", prefix, channelID, i))
		_, err := touchOpencodeActiveSession(channelID, sessionKey, now)
		require.NoError(t, err)
		keys[i] = sessionKey
	}
	return keys
}

func deleteOpencodeActiveSessionForTest(t *testing.T, channelID int, sessionKey string) {
	t.Helper()
	opencodeActiveMu.Lock()
	defer opencodeActiveMu.Unlock()
	active, found, err := getOpencodeActiveCache().Get(strconv.Itoa(channelID))
	require.NoError(t, err)
	if !found || active == nil {
		return
	}
	delete(active, sessionKey)
	if len(active) == 0 {
		_, err = getOpencodeActiveCache().DeleteMany([]string{strconv.Itoa(channelID)})
		require.NoError(t, err)
		return
	}
	require.NoError(t, getOpencodeActiveCache().SetWithTTL(strconv.Itoa(channelID), active, time.Duration(opencodeActiveWindowSeconds)*time.Second))
}

func plantOpencodeActiveLastSeen(t *testing.T, channelID int, sessionKey string, lastSeen int64) {
	t.Helper()
	opencodeActiveMu.Lock()
	defer opencodeActiveMu.Unlock()
	active, found, err := getOpencodeActiveCache().Get(strconv.Itoa(channelID))
	require.NoError(t, err)
	if !found || active == nil {
		active = map[string]int64{}
	}
	active[sessionKey] = lastSeen
	require.NoError(t, getOpencodeActiveCache().SetWithTTL(strconv.Itoa(channelID), active, time.Duration(opencodeActiveWindowSeconds)*time.Second))
}

func countOpencodeActive(t *testing.T, channelID int) int {
	t.Helper()
	count, err := countOpencodeActiveSessions(channelID, time.Now().Unix())
	require.NoError(t, err)
	return count
}

func TestOpencodeSessionCapacity_NewSessionsSpreadAcrossLeastLoaded(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 3)
	channels := createOpencodeCapacityChannels(t, db, 5)

	assigned := make(map[int]int, len(channels))
	for i := 0; i < 10; i++ {
		c := newOpencodeCapacityContext(t, fmt.Sprintf("spread-conv-%d", i))
		channel, queueRequired := selectOpencodeCapacityChannel(t, c)
		require.False(t, queueRequired)
		assigned[channel.Id]++
	}
	for _, channel := range channels {
		assert.LessOrEqual(t, assigned[channel.Id], 2, "least-loaded gate must spread 10 sessions across 5 channels")
		assert.Equal(t, assigned[channel.Id], countOpencodeActive(t, channel.Id))
	}
}

func TestOpencodeSessionCapacity_FullChannelsHardFallbackWithoutError(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 3)
	channels := createOpencodeCapacityChannels(t, db, 2)
	for _, channel := range channels {
		fillOpencodeActiveSessions(t, channel.Id, 3, t.Name())
	}

	c := newOpencodeCapacityContext(t, t.Name()+"-new")
	channel, queueRequired := selectOpencodeCapacityChannel(t, c)
	require.True(t, queueRequired, "all-full new sessions must request the bounded wait")
	require.Contains(t, []int{channels[0].Id, channels[1].Id}, channel.Id)
	assert.Equal(t, 3, countOpencodeActive(t, channel.Id), "hard-fallback candidate must not occupy a slot before wait")

	opencodeSessionQueueTimeout = 0
	param := newOpencodeCapacityParam(newOpencodeCapacityContext(t, t.Name()+"-timeout"))
	selected, _, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.False(t, IsOpencodeSessionQueueRequired(param.Ctx))
	assert.Greater(t, countOpencodeActive(t, selected.Id), 3)
}

func TestOpencodeSessionCapacity_RetainedSessionReusesOriginalChannel(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 3)
	channels := createOpencodeCapacityChannels(t, db, 2)
	c := newOpencodeCapacityContext(t, t.Name()+"-conv")
	original, queueRequired := selectOpencodeCapacityChannel(t, c)
	require.False(t, queueRequired)
	sessionKey := opencodeSessionKey(opencodeConversationKeyForModel(c, model.OpencodeModel))
	deleteOpencodeActiveSessionForTest(t, original.Id, sessionKey)
	require.Equal(t, 0, countOpencodeActive(t, original.Id))

	reuse, queueRequired := selectOpencodeCapacityChannel(t, newOpencodeCapacityContext(t, t.Name()+"-conv"))
	require.False(t, queueRequired)
	require.Equal(t, original.Id, reuse.Id)
	require.Equal(t, 1, countOpencodeActive(t, original.Id))
	require.Equal(t, 0, countOpencodeActive(t, otherOpencodeChannelID(channels, original.Id)))
}

func TestOpencodeSessionCapacity_ActiveWindowExpiryReleasesSlot(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 1)
	channels := createOpencodeCapacityChannels(t, db, 1)
	c := newOpencodeCapacityContext(t, t.Name()+"-conv")
	original, queueRequired := selectOpencodeCapacityChannel(t, c)
	require.False(t, queueRequired)
	require.Equal(t, 1, countOpencodeActive(t, original.Id))

	sessionKey := opencodeSessionKey(opencodeConversationKeyForModel(c, model.OpencodeModel))
	plantOpencodeActiveLastSeen(t, original.Id, sessionKey, time.Now().Unix()-int64(opencodeActiveWindowSeconds)-1)
	require.Equal(t, 0, countOpencodeActive(t, original.Id))

	next, queueRequired := selectOpencodeCapacityChannel(t, newOpencodeCapacityContext(t, t.Name()+"-other"))
	require.False(t, queueRequired)
	require.Equal(t, channels[0].Id, next.Id)
	require.Equal(t, 1, countOpencodeActive(t, next.Id))
}

func TestOpencodeSessionCapacity_RetainedSessionMovesWhenOriginalIsFull(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 1)
	channels := createOpencodeCapacityChannels(t, db, 2)
	c := newOpencodeCapacityContext(t, t.Name()+"-conv")
	original, queueRequired := selectOpencodeCapacityChannel(t, c)
	require.False(t, queueRequired)
	sessionKey := opencodeSessionKey(opencodeConversationKeyForModel(c, model.OpencodeModel))
	deleteOpencodeActiveSessionForTest(t, original.Id, sessionKey)
	fillOpencodeActiveSessions(t, original.Id, 1, t.Name()+"-filler")
	require.Equal(t, 1, countOpencodeActive(t, original.Id))

	moved, queueRequired := selectOpencodeCapacityChannel(t, newOpencodeCapacityContext(t, t.Name()+"-conv"))
	require.False(t, queueRequired)
	require.NotEqual(t, original.Id, moved.Id)
	require.Equal(t, 1, countOpencodeActive(t, original.Id))
	require.Equal(t, 1, countOpencodeActive(t, moved.Id))
	require.Contains(t, []int{channels[0].Id, channels[1].Id}, moved.Id)
}

func TestOpencodeSessionCapacity_AllFullQueueFIFOAndTimeoutFallback(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 1)
	channels := createOpencodeCapacityChannels(t, db, 2)
	fillers := make([][]string, len(channels))
	for i, channel := range channels {
		fillers[i] = fillOpencodeActiveSessions(t, channel.Id, 1, t.Name())
	}

	opencodeSessionQueueTimeout = 30 * time.Second
	firstCtx := newOpencodeCapacityContext(t, t.Name()+"-wait-1")
	secondCtx, cancelSecond := context.WithCancel(firstCtx.Request.Context())
	secondGin := newOpencodeCapacityContext(t, t.Name()+"-wait-2")
	secondGin.Request = secondGin.Request.WithContext(secondCtx)

	type waitResult struct {
		channel *model.Channel
		err     error
	}
	firstCh := make(chan waitResult, 1)
	secondCh := make(chan waitResult, 1)
	go func() {
		channel, _, err := CacheGetRandomSatisfiedChannel(newOpencodeCapacityParam(firstCtx))
		firstCh <- waitResult{channel: channel, err: err}
	}()
	require.Eventually(t, func() bool {
		opencodeSessionWaitMu.Lock()
		defer opencodeSessionWaitMu.Unlock()
		return len(opencodeSessionWaitQueue) == 1
	}, time.Second, 10*time.Millisecond)

	go func() {
		channel, _, err := CacheGetRandomSatisfiedChannel(newOpencodeCapacityParam(secondGin))
		secondCh <- waitResult{channel: channel, err: err}
	}()
	require.Eventually(t, func() bool {
		opencodeSessionWaitMu.Lock()
		defer opencodeSessionWaitMu.Unlock()
		return len(opencodeSessionWaitQueue) == 2
	}, time.Second, 10*time.Millisecond)

	deleteOpencodeActiveSessionForTest(t, channels[0].Id, fillers[0][0])
	NotifyOpencodeSessionQueue()

	select {
	case result := <-firstCh:
		require.NoError(t, result.err)
		require.NotNil(t, result.channel)
		require.Equal(t, channels[0].Id, result.channel.Id)
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO head was not released after a session slot opened")
	}
	select {
	case result := <-secondCh:
		t.Fatalf("FIFO tail must keep waiting, got channel=%v err=%v", result.channel, result.err)
	case <-time.After(50 * time.Millisecond):
	}
	cancelSecond()
	select {
	case <-secondCh:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled FIFO tail did not return")
	}

	opencodeSessionQueueTimeout = 0
	timeoutParam := newOpencodeCapacityParam(newOpencodeCapacityContext(t, t.Name()+"-timeout"))
	selected, _, err := CacheGetRandomSatisfiedChannel(timeoutParam)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.False(t, IsOpencodeSessionQueueRequired(timeoutParam.Ctx))
}

func TestOpencodeSessionCapacity_ActiveSessionDoesNotConsumeExtraSlotOrSessionQueue(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 1)
	_ = createOpencodeCapacityChannels(t, db, 1)
	first := newOpencodeCapacityContext(t, t.Name()+"-conv")
	original, queueRequired := selectOpencodeCapacityChannel(t, first)
	require.False(t, queueRequired)

	second := newOpencodeCapacityContext(t, t.Name()+"-conv")
	reuse, queueRequired := selectOpencodeCapacityChannel(t, second)
	require.False(t, queueRequired)
	require.Equal(t, original.Id, reuse.Id)
	require.Equal(t, 1, countOpencodeActive(t, original.Id))
	opencodeSessionWaitMu.Lock()
	waiting := len(opencodeSessionWaitQueue)
	opencodeSessionWaitMu.Unlock()
	require.Zero(t, waiting)
}

func TestOpencodeSessionCapacity_SessionWaitDoesNotReserveConcurrency(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 1)
	channels := createOpencodeCapacityChannels(t, db, 1)
	fillOpencodeActiveSessions(t, channels[0].Id, 1, t.Name())
	opencodeSessionQueueTimeout = 30 * time.Second

	c := newOpencodeCapacityContext(t, t.Name()+"-new")
	ctx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _ = TryReserveOpencodeAffinityChannel(newOpencodeCapacityParam(c))
	}()
	require.Eventually(t, func() bool {
		opencodeSessionWaitMu.Lock()
		defer opencodeSessionWaitMu.Unlock()
		return len(opencodeSessionWaitQueue) == 1
	}, time.Second, 10*time.Millisecond)

	used, _, err := model.GetCodeProviderConcurrencyUsage(constant.ChannelTypeOpencode, channels[0].Id, time.Now().Unix())
	require.NoError(t, err)
	require.Zero(t, used, "session wait must not take a request concurrency lease")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session wait did not observe cancellation")
	}
}

func TestOpencodeSessionCapacity_QueueRetryDoesNotRecreateSession(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 1)
	_ = createOpencodeCapacityChannels(t, db, 1)
	opencodeSessionQueueTimeout = 0
	c := newOpencodeCapacityContext(t, t.Name()+"-conv")
	param := newOpencodeCapacityParam(c)
	channels, err := model.GetCodeProviderQuotaCandidates("default", model.OpencodeModel, nil)
	require.NoError(t, err)
	require.NotEmpty(t, channels)
	fillOpencodeActiveSessions(t, channels[0].Id, 1, t.Name()+"-full")

	first, _, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, first)
	sessionKey := opencodeSessionKey(opencodeConversationKeyForModel(c, model.OpencodeModel))
	firstBinding, found, err := getOpencodeSessionBinding(sessionKey)
	require.NoError(t, err)
	require.True(t, found)

	second, _, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, first.Id, second.Id)
	secondBinding, found, err := getOpencodeSessionBinding(sessionKey)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, firstBinding.Session, secondBinding.Session)
}

func TestOpencodeSessionWaitTurnIsPerGroupModel(t *testing.T) {
	previous := opencodeSessionWaitQueue
	t.Cleanup(func() { opencodeSessionWaitQueue = previous })

	first := &opencodeSessionWaitEntry{key: opencodeSessionWaitKey{group: "default", model: "deepseek-v4-flash"}}
	sameKey := &opencodeSessionWaitEntry{key: opencodeSessionWaitKey{group: "default", model: "deepseek-v4-flash"}}
	otherGroup := &opencodeSessionWaitEntry{key: opencodeSessionWaitKey{group: "vip", model: "deepseek-v4-flash"}}
	otherModel := &opencodeSessionWaitEntry{key: opencodeSessionWaitKey{group: "default", model: "deepseek-v4-pro"}}
	opencodeSessionWaitQueue = []*opencodeSessionWaitEntry{first, sameKey, otherGroup, otherModel}

	require.True(t, isOpencodeSessionWaitTurnLocked(first))
	require.False(t, isOpencodeSessionWaitTurnLocked(sameKey), "same group+model must share one FIFO")
	require.True(t, isOpencodeSessionWaitTurnLocked(otherGroup))
	require.True(t, isOpencodeSessionWaitTurnLocked(otherModel))
}

func listOpencodeSessionLogs(t *testing.T) []model.Log {
	t.Helper()
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", model.LogTypeSession).Order("id asc").Find(&logs).Error)
	return logs
}

func TestOpencodeSessionCapacity_CreateAndRetainedHitWriteSessionLogsOnce(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 3)
	_ = createOpencodeCapacityChannels(t, db, 1)

	c := newOpencodeCapacityContext(t, t.Name()+"-conv")
	_, queueRequired := selectOpencodeCapacityChannel(t, c)
	require.False(t, queueRequired)
	logs := listOpencodeSessionLogs(t)
	require.Len(t, logs, 1)
	require.Equal(t, "OpenCode session create", logs[0].Content)

	sessionKey := opencodeSessionKey(opencodeConversationKeyForModel(c, model.OpencodeModel))
	deleteOpencodeActiveSessionForTest(t, logs[0].ChannelId, sessionKey)

	_, queueRequired = selectOpencodeCapacityChannel(t, newOpencodeCapacityContext(t, t.Name()+"-conv"))
	require.False(t, queueRequired)
	logs = listOpencodeSessionLogs(t)
	require.Len(t, logs, 2)
	require.Equal(t, "OpenCode session hit", logs[1].Content)
}

func TestOpencodeSessionCapacity_ActiveReuseDoesNotWriteSessionLog(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 1)
	_ = createOpencodeCapacityChannels(t, db, 1)
	first := newOpencodeCapacityContext(t, t.Name()+"-conv")
	_, queueRequired := selectOpencodeCapacityChannel(t, first)
	require.False(t, queueRequired)
	require.Len(t, listOpencodeSessionLogs(t), 1)

	second := newOpencodeCapacityContext(t, t.Name()+"-conv")
	_, queueRequired = selectOpencodeCapacityChannel(t, second)
	require.False(t, queueRequired)
	require.Len(t, listOpencodeSessionLogs(t), 1)
}

func TestOpencodeSessionCapacity_SkipsAttemptedRememberedChannel(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 3)
	channels := createOpencodeCapacityChannels(t, db, 2)
	conv := t.Name() + "-conv"
	original, queueRequired := selectOpencodeCapacityChannel(t, newOpencodeCapacityContext(t, conv))
	require.False(t, queueRequired)

	retryCtx := newOpencodeCapacityContext(t, conv)
	rememberOpencodeSessionSelection(retryCtx, original.Id)
	attempted := map[int]struct{}{original.Id: {}}
	channel, queueRequired, err := SelectOpencodeSessionChannel(retryCtx, "default", model.OpencodeModel, "/v1/chat/completions", nil, attempted)
	require.NoError(t, err)
	require.NotNil(t, channel)
	require.False(t, queueRequired)
	require.NotEqual(t, original.Id, channel.Id)
	require.Equal(t, otherOpencodeChannelID(channels, original.Id), channel.Id)
}

func TestOpencodeSessionCapacity_MovesWhenBindingChannelAttempted(t *testing.T) {
	db := useOpencodeSessionCapacityEnv(t, 3)
	channels := createOpencodeCapacityChannels(t, db, 2)
	conv := t.Name() + "-conv"
	original, queueRequired := selectOpencodeCapacityChannel(t, newOpencodeCapacityContext(t, conv))
	require.False(t, queueRequired)
	require.Len(t, listOpencodeSessionLogs(t), 1)

	retryCtx := newOpencodeCapacityContext(t, conv)
	attempted := map[int]struct{}{original.Id: {}}
	channel, queueRequired, err := SelectOpencodeSessionChannel(retryCtx, "default", model.OpencodeModel, "/v1/chat/completions", nil, attempted)
	require.NoError(t, err)
	require.NotNil(t, channel)
	require.False(t, queueRequired)
	require.NotEqual(t, original.Id, channel.Id)
	require.Equal(t, otherOpencodeChannelID(channels, original.Id), channel.Id)

	logs := listOpencodeSessionLogs(t)
	require.Len(t, logs, 2)
	require.Equal(t, "OpenCode session move", logs[1].Content)
	require.Equal(t, channel.Id, logs[1].ChannelId)
}

func otherOpencodeChannelID(channels []*model.Channel, except int) int {
	for _, channel := range channels {
		if channel != nil && channel.Id != except {
			return channel.Id
		}
	}
	return 0
}
