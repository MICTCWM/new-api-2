package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpencodeQueueUsesSelectedAutoGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyAutoGroup, "fast")

	param := &RetryParam{Ctx: c, TokenGroup: "auto", ModelName: "deepseek-v4-flash"}
	require.Equal(t, "fast", queueGroupForParam(param))
}

func TestOpencodeQueueSeparatesGroupAndModel(t *testing.T) {
	previousQueue := opencodeQueue
	t.Cleanup(func() { opencodeQueue = previousQueue })

	first := &opencodeQueueEntry{queueKey: opencodeQueueKey{group: "default", model: "deepseek-v4-flash"}}
	sameKey := &opencodeQueueEntry{queueKey: opencodeQueueKey{group: "default", model: "deepseek-v4-flash"}}
	otherGroup := &opencodeQueueEntry{queueKey: opencodeQueueKey{group: "vip", model: "deepseek-v4-flash"}}
	otherModel := &opencodeQueueEntry{queueKey: opencodeQueueKey{group: "default", model: "deepseek-v4-pro"}}

	opencodeQueue = []*opencodeQueueEntry{first, sameKey, otherGroup, otherModel}
	require.True(t, isOpencodeQueueTurnLocked(first))
	require.False(t, isOpencodeQueueTurnLocked(sameKey))
	require.True(t, isOpencodeQueueTurnLocked(otherGroup))
	require.True(t, isOpencodeQueueTurnLocked(otherModel))
}
