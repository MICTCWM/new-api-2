package controller

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const channelLimitQueuePollInterval = time.Second
const channelLimitQueueHeartbeatInterval = 20 * time.Second

// channelLimitQueuePollInterval bounds how long a queued request sleeps
// without a wake signal. The TPM window rolls over without any release event,
// so waiters must re-check capacity periodically regardless of wake-ups.

// channelLimitRejectError builds the gateway-side 429 returned in reject
// (默认 429) mode when the channel's concurrency or TPM limit is exhausted.
// It stays retryable so the relay loop can hand the request to another channel.
func channelLimitRejectError(reason string) *types.NewAPIError {
	message := constant.ChannelConcurrencyLimitMessage
	if reason == "tpm" {
		message = constant.ChannelTPMLimitMessage
	}
	return types.NewErrorWithStatusCode(
		errors.New(message),
		types.ErrorCodeChannelRateLimited,
		http.StatusTooManyRequests,
		types.ErrOptionWithStatusCode(http.StatusTooManyRequests),
	)
}

// channelQueueItem builds the shared queue entry shown in the admin gauge while
// a request waits for channel capacity.
func channelQueueItem(c *gin.Context, relayInfo *relaycommon.RelayInfo, kind string, tokens int) *model.ChannelQueueItem {
	return &model.ChannelQueueItem{
		RequestID:   relayInfo.RequestId,
		UserID:      c.GetInt("id"),
		Username:    c.GetString("username"),
		Group:       c.GetString("group"),
		Model:       relayInfo.OriginModelName,
		InputTokens: int64(tokens),
		QueuedAt:    time.Now().UnixMilli(),
		Kind:        kind,
	}
}

// waitChannelCapacity parks a request in the channel's queue-mode waiting room
// until a concurrency slot plus enough TPM headroom free up (the remaining TPM
// must cover the request's total input tokens), the client disconnects, or the
// request is cancelled. The entry is registered while waiting so
// GetChannelQueueStats can display the queue to admins.
func waitChannelCapacity(c *gin.Context, relayInfo *relaycommon.RelayInfo, channel *model.Channel, tokens int, initialReason string) (bool, *types.NewAPIError) {
	item := channelQueueItem(c, relayInfo, initialReason, tokens)
	handle, err := service.EnqueueChannelWait(channel.Id, item)
	if err != nil {
		return false, types.NewError(err, types.ErrorCodeDoRequestFailed)
	}
	defer func() {
		if err := service.LeaveChannelQueue(handle); err != nil {
			logger.LogWarn(c, fmt.Sprintf("failed to remove channel queue entry for channel %d: %s", channel.Id, err.Error()))
		}
	}()
	lastHeartbeat := time.Now()

	for {
		if service.IsChannelQueueTurn(handle) {
			allowed, reason, reserveErr := model.ReserveChannelCapacity(channel, common.GetTimestamp(), int64(tokens))
			if reserveErr != nil {
				return false, types.NewError(reserveErr, types.ErrorCodeDoRequestFailed)
			}
			if allowed {
				return true, nil
			}
			// Keep the displayed reason current: a queue can switch from being
			// blocked by concurrency to being blocked by TPM and vice versa.
			if err := service.SetChannelQueueKind(handle, reason); err != nil {
				logger.LogWarn(c, fmt.Sprintf("failed to update channel queue reason for channel %d: %s", channel.Id, err.Error()))
			}
		}

		wake := service.CurrentChannelQueueWake()
		select {
		case <-c.Request.Context().Done():
			return false, types.NewError(
				errors.New("client disconnected while waiting for channel capacity"),
				types.ErrorCodeDoRequestFailed,
				types.ErrOptionWithSkipRetry(),
			)
		case <-wake:
			// A slot or token budget was released; retry immediately.
		case <-time.After(channelLimitQueuePollInterval):
			// Covers TPM window rollovers and multi-node releases.
		}
		if time.Since(lastHeartbeat) >= channelLimitQueueHeartbeatInterval {
			if err := service.TouchChannelQueue(handle); err != nil {
				logger.LogWarn(c, fmt.Sprintf("failed to refresh channel queue entry for channel %d: %s", channel.Id, err.Error()))
			}
			lastHeartbeat = time.Now()
		}
	}
}

// releaseChannelCapacity returns a pre-upstream reservation and any held
// concurrency slot, then wakes queued requests to retry.
func releaseChannelCapacity(c *gin.Context, channel *model.Channel, tokens int, concurrencySlotReserved bool) {
	if channel == nil {
		return
	}
	if tokens > 0 {
		if err := model.RollbackChannelTPMTokens(channel, int64(tokens)); err != nil {
			logger.LogError(c, fmt.Sprintf("failed to rollback channel TPM tokens for channel %d: %s", channel.Id, err.Error()))
		}
	}
	if concurrencySlotReserved {
		if err := model.ReleaseChannelConcurrency(channel); err != nil {
			logger.LogError(c, fmt.Sprintf("failed to release channel concurrency for channel %d: %s", channel.Id, err.Error()))
		}
	}
	service.NotifyChannelLimitQueues()
}
