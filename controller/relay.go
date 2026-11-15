package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func relayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	switch info.RelayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		err = relay.ImageHelper(c, info)
	case relayconstant.RelayModeAudioSpeech:
		fallthrough
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err = relay.AudioHelper(c, info)
	case relayconstant.RelayModeRerank:
		err = relay.RerankHelper(c, info)
	case relayconstant.RelayModeEmbeddings:
		err = relay.EmbeddingHelper(c, info)
	case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		err = relay.ResponsesHelper(c, info)
	case relayconstant.RelayModeAlphaSearch:
		err = relay.AlphaSearchHelper(c, info)
	default:
		err = relay.TextHelper(c, info)
	}
	return err
}

func shouldApplyNonStreamTimeout(format types.RelayFormat, info *relaycommon.RelayInfo) bool {
	if info == nil || info.IsStream || common.NonStreamTimeout <= 0 {
		return false
	}
	if info.RelayMode == relayconstant.RelayModeEmbeddings {
		return false
	}
	switch format {
	case types.RelayFormatOpenAI, types.RelayFormatClaude, types.RelayFormatGemini,
		types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		return true
	default:
		return false
	}
}

// applyNonStreamTimeoutError 把超时定时器到点引发的错误统一改写为 504，
// 无论超时发生在等响应头还是读 body 阶段。
func applyNonStreamTimeoutError(info *relaycommon.RelayInfo, err *types.NewAPIError) *types.NewAPIError {
	if err == nil || info == nil || info.UpstreamContext == nil {
		return err
	}
	if !errors.Is(info.UpstreamContext.Err(), context.DeadlineExceeded) {
		return err
	}
	return types.NewErrorWithStatusCode(
		fmt.Errorf("upstream non-stream response timed out after %d seconds", common.NonStreamTimeout),
		types.ErrorCodeDoRequestFailed,
		http.StatusGatewayTimeout,
		types.ErrOptionWithSkipRetry(),
	)
}

func geminiRelayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	if strings.Contains(c.Request.URL.Path, "embed") {
		err = relay.GeminiEmbeddingHandler(c, info)
	} else {
		err = relay.GeminiHelper(c, info)
	}
	return err
}

func Relay(c *gin.Context, relayFormat types.RelayFormat) {

	requestId := c.GetString(common.RequestIdKey)
	//group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	//originalModel := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)

	var (
		newAPIError *types.NewAPIError
		ws          *websocket.Conn
		relayInfo   *relaycommon.RelayInfo
	)

	if relayFormat == types.RelayFormatOpenAIRealtime {
		var err error
		ws, err = upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			helper.WssError(c, ws, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry()).ToOpenAIError())
			return
		}
		defer ws.Close()
	}

	defer func() {
		if newAPIError != nil {
			if newAPIError.RewriteUpstream429 {
				newAPIError.StatusCode = http.StatusTooManyRequests
			}
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
			// 最终返回给下游的错误必须落使用日志，不依赖 ERROR_LOG_ENABLED。
			recordFinalRelayErrorLog(c, newAPIError, relayInfo)
			if newAPIError.Error() == constant.OpencodeUpstreamExhaustedMessage {
				// Keep the public exhaustion message exact; request IDs remain in logs.
				newAPIError.SetMessage(constant.OpencodeUpstreamExhaustedMessage)
			} else if newAPIError.RewriteUpstream429 {
				// Keep the upstream 429 status and replace its body with the
				// selected channel's configured public message.
				newAPIError.SetMessage(constant.ChannelUpstream429RewriteMessage)
			} else {
				newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
			}

			switch relayFormat {
			case types.RelayFormatOpenAIRealtime:
				helper.WssError(c, ws, newAPIError.ToOpenAIError())
			case types.RelayFormatClaude:
				c.JSON(newAPIError.StatusCode, gin.H{
					"type":  "error",
					"error": newAPIError.ToClaudeError(),
				})
			default:
				c.JSON(newAPIError.StatusCode, gin.H{
					"error": newAPIError.ToOpenAIError(),
				})
			}
		}
	}()

	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		// Map "request body too large" to 413 so clients can handle it correctly
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			newAPIError = types.NewError(err, types.ErrorCodeInvalidRequest)
		}
		return
	}

	relayInfo, err = relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
	}

	if shouldApplyNonStreamTimeout(relayFormat, relayInfo) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(common.NonStreamTimeout)*time.Second)
		defer cancel()
		relayInfo.UpstreamContext = ctx
	}

	needSensitiveCheck := setting.ShouldCheckPromptSensitive()
	needCountToken := constant.CountToken
	// Avoid building huge CombineText (strings.Join) when token counting and sensitive check are both disabled.
	var meta *types.TokenCountMeta
	if needSensitiveCheck || needCountToken {
		meta = request.GetTokenCountMeta()
	} else {
		meta = fastTokenCountMetaForPricing(request)
	}

	if needSensitiveCheck && meta != nil {
		contains, words := service.CheckSensitiveText(meta.CombineText)
		if contains {
			logger.LogWarn(c, fmt.Sprintf("user sensitive words detected: %s", strings.Join(words, ", ")))
			newAPIError = types.NewError(err, types.ErrorCodeSensitiveWordsDetected)
			return
		}
	}

	tokens, err := service.EstimateRequestToken(c, meta, relayInfo)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeCountTokenFailed)
		return
	}

	relayInfo.SetEstimatePromptTokens(tokens)
	var channelCapacityMeta *types.TokenCountMeta
	// 连接日志：请求一进入就登记并发数与输入 token，结束时只关闭连接、不删除记录。
	service.RegisterConnection(relayInfo, c.GetString("username"), tokens)
	defer service.FinishConnection(relayInfo.RequestId)

	priceData, err := helper.ModelPriceHelper(c, relayInfo, tokens, meta)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))
		return
	}

	// common.SetContextKey(c, constant.ContextKeyTokenCountMeta, meta)

	if priceData.FreeModel {
		logger.LogInfo(c, fmt.Sprintf("模型 %s 免费，跳过预扣费", relayInfo.OriginModelName))
	} else {
		newAPIError = service.PreConsumeBilling(c, priceData.QuotaToPreConsume, relayInfo)
		if newAPIError != nil {
			return
		}
	}

	defer func() {
		// Only return quota if downstream failed and quota was actually pre-consumed
		if newAPIError != nil {
			newAPIError = service.NormalizeViolationFeeError(newAPIError)
			if relayInfo.Billing != nil {
				relayInfo.Billing.Refund(c)
			}
			service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)
		}
	}()

	retryParam := &service.RetryParam{
		Ctx:                 c,
		TokenGroup:          relayInfo.TokenGroup,
		ModelName:           relayInfo.OriginModelName,
		RequestPath:         c.Request.URL.Path,
		Retry:               common.GetPointer(0),
		OpencodePool:        common.GetContextKeyInt(c, constant.ContextKeyChannelType) == constant.ChannelTypeOpencode || common.GetContextKeyInt(c, constant.ContextKeyChannelType) == constant.ChannelTypeCommand,
		AttemptedChannelIDs: make(map[int]struct{}),
	}
	retryRoutingModel := relayInfo.OriginModelName
	if target := service.RequestModelSplitTarget(c); target != "" {
		retryRoutingModel = target
	}
	if model.IsCodeProviderModel(retryRoutingModel) && !retryParam.OpencodePool {
		retryParam.ExcludeCodeProviderChannels = true
	}

	relayInfo.LastError = nil

	opencodeAttempts := 0
	maxAttempts := common.RetryTimes + 1
	if retryParam.OpencodePool {
		maxAttempts = 4
	}
	// 兜底模式下即使超出 maxAttempts 也必须继续，直到兜底序列全部尝试完毕或耗尽。
	// 同渠道重试不消耗跨渠道预算，所以有待重投渠道时也继续循环。
	for attempt := 0; attempt < maxAttempts || retryParam.InFallback || retryParam.HasPendingRetryChannel(); attempt++ {
		pendingRetryChannel := retryParam.TakePendingRetryChannel()
		if pendingRetryChannel == nil && attempt > 0 {
			retryParam.IncreaseRetry()
		}
		// 兜底模式优先于 code-provider 的 4 次上限：兜底链未走完前不能提前截断。
		if retryParam.OpencodePool && !retryParam.InFallback && opencodeAttempts >= 4 {
			// 耗尽仍掩盖为「上游错误」，但 400 保留真实原因（如 context length）。
			newAPIError = maskOpencodeExhaustedError(relayInfo.LastError)
			break
		}
		relayInfo.RetryIndex = retryParam.GetRetry()
		channel := pendingRetryChannel
		var channelErr *types.NewAPIError
		if channel == nil {
			channel, channelErr = getChannel(c, relayInfo, retryParam)
		}

		if channelErr != nil {
			// 兜底序列全部失败：返回主渠道的首个错误，不再参与全局随机重试。
			if retryParam.InFallback && !retryParam.HasNextFallback() {
				if retryParam.FirstError != nil {
					newAPIError = retryParam.FirstError
					if retryParam.FallbackFirstErrorCodeProvider {
						newAPIError = maskOpencodeExhaustedError(newAPIError)
					}
				} else {
					newAPIError = channelErr
				}
				break
			}
			if retryParam.OpencodePool {
				newAPIError = maskOpencodeExhaustedError(relayInfo.LastError)
			} else {
				logger.LogError(c, channelErr.Error())
				newAPIError = channelErr
			}
			break
		}

		concurrencyReserved := false
		channelConcurrencyReserved := false
		channelConcurrencySlotReserved := false
		channelCapacityReservedTokens := 0
		relayInfo.ChannelTPMReservedTokens = 0
		if channel.Type == constant.ChannelTypeOpencode || channel.Type == constant.ChannelTypeCommand {
			retryParam.OpencodePool = true
			if retryParam.AttemptedChannelIDs == nil {
				retryParam.AttemptedChannelIDs = make(map[int]struct{})
			}
			retryParam.AttemptedChannelIDs[channel.Id] = struct{}{}
			if retryParam.OpencodeConcurrencyReserved {
				retryParam.OpencodeConcurrencyReserved = false
			} else {
				allowed, concurrencyErr := model.ReserveCodeProviderConcurrency(channel.Type, channel.Id)
				if concurrencyErr != nil {
					newAPIError = types.NewError(concurrencyErr, types.ErrorCodeDoRequestFailed)
					break
				}
				if !allowed {
					newAPIError = types.NewErrorWithStatusCode(errors.New("opencode channel concurrency limit exceeded"), types.ErrorCodeDoRequestFailed, http.StatusTooManyRequests)
					continue
				}
			}
			opencodeAttempts++
			concurrencyReserved = true
		} else {
			// The distributor's initial channel is intentionally a lightweight
			// snapshot. Reload it here so the channel-specific RPM setting is
			// available before reserving concurrency.
			if channel.OtherSettings == "" {
				fullChannel, loadErr := model.CacheGetChannel(channel.Id)
				if loadErr != nil {
					newAPIError = types.NewError(loadErr, types.ErrorCodeGetChannelFailed)
					break
				}
				if fullChannel == nil {
					newAPIError = types.NewError(errors.New("selected channel is missing"), types.ErrorCodeGetChannelFailed)
					break
				}
				channel = fullChannel
			}
			if retryParam.AttemptedChannelIDs == nil {
				retryParam.AttemptedChannelIDs = make(map[int]struct{})
			}
			retryParam.AttemptedChannelIDs[channel.Id] = struct{}{}
			capacityTokens := tokens
			if model.GetChannelTPMLimit(channel) > 0 && !constant.CountToken {
				if channelCapacityMeta == nil {
					channelCapacityMeta = request.GetTokenCountMeta()
				}
				capacityTokens, err = service.EstimateChannelCapacityTokens(c, channelCapacityMeta, relayInfo)
				if err != nil {
					newAPIError = types.NewError(err, types.ErrorCodeCountTokenFailed)
					break
				}
			}
			if model.GetChannelTPMLimit(channel) > 0 && capacityTokens > model.GetChannelEffectiveTPMLimit(channel) {
				// This request can never fit within the channel's effective TPM
				// window, so queueing it would leave it waiting indefinitely.
				newAPIError = channelLimitRejectError("tpm")
				continue
			}
			// New requests do not bypass an existing queue. When there are no
			// waiters, try to reserve immediately before registering a new wait.
			queueMode := model.GetChannelLimitMode(channel) == model.ChannelLimitModeQueue
			allowed := false
			reason := ""
			if queueMode && service.HasChannelQueue(channel.Id) {
				reason = "concurrency"
			} else {
				var capacityErr error
				allowed, reason, capacityErr = model.ReserveChannelCapacity(channel, common.GetTimestamp(), int64(capacityTokens))
				if capacityErr != nil {
					newAPIError = types.NewError(capacityErr, types.ErrorCodeDoRequestFailed)
					break
				}
			}
			if !allowed && queueMode {
				if model.GetChannelTPMLimit(channel) == 0 && !constant.CountToken {
					if channelCapacityMeta == nil {
						channelCapacityMeta = request.GetTokenCountMeta()
					}
					capacityTokens, err = service.EstimateChannelCapacityTokens(c, channelCapacityMeta, relayInfo)
					if err != nil {
						newAPIError = types.NewError(err, types.ErrorCodeCountTokenFailed)
						break
					}
				}
				// 排队模式：登记到本渠道队列并等待容量释放，期间不换渠道。
				queued, waitErr := waitChannelCapacity(c, relayInfo, channel, capacityTokens, reason)
				if waitErr != nil {
					if concurrencyReserved {
						releaseOpencodeConcurrency(c, channel.Type, channel.Id)
						concurrencyReserved = false
					}
					newAPIError = waitErr
					break
				}
				allowed = queued
			}
			if !allowed {
				// 429 模式（默认）：超限直接返回，交给重试链换渠道。
				newAPIError = channelLimitRejectError(reason)
				if service.GetCurrentChannelAffinitySelection(c) == channel.Id {
					service.ClearCurrentChannelAffinitySelection(c)
				}
				continue
			}
			channelConcurrencySlotReserved = model.GetChannelConcurrencyLimit(channel) > 0
			if model.GetChannelTPMLimit(channel) > 0 {
				channelCapacityReservedTokens = capacityTokens
				relayInfo.ChannelTPMReservedTokens = int64(capacityTokens)
			}
			channelConcurrencyReserved = true
		}
		addUsedChannel(c, channel.Id)
		service.SetConnectionChannel(relayInfo.RequestId, channel.Id)
		if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
			if concurrencyReserved {
				releaseOpencodeConcurrency(c, channel.Type, channel.Id)
			}
			if channelConcurrencyReserved {
				releaseChannelCapacity(c, channel, channelCapacityReservedTokens, channelConcurrencySlotReserved)
			}
			relayInfo.ChannelTPMReservedTokens = 0
			newAPIError = billingErr
			break
		}

		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			// Ensure consistent 413 for oversized bodies even when error occurs later (e.g., retry path)
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
			} else {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			if concurrencyReserved {
				releaseOpencodeConcurrency(c, channel.Type, channel.Id)
			}
			if channelConcurrencyReserved {
				releaseChannelCapacity(c, channel, channelCapacityReservedTokens, channelConcurrencySlotReserved)
			}
			relayInfo.ChannelTPMReservedTokens = 0
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		// Install a fresh response rewriter for this attempt. It only rewrites
		// the outbound client copy (model id / response text); the upstream
		// request body, billing and logs are untouched.
		service.InstallResponseRewriter(c, relayInfo)

		switch relayFormat {
		case types.RelayFormatOpenAIRealtime:
			newAPIError = relay.WssHelper(c, relayInfo)
		case types.RelayFormatClaude:
			newAPIError = relay.ClaudeHelper(c, relayInfo)
		case types.RelayFormatGemini:
			newAPIError = geminiRelayHandler(c, relayInfo)
		default:
			newAPIError = relayHandler(c, relayInfo)
		}

		if newAPIError != nil {
			newAPIError = applyNonStreamTimeoutError(relayInfo, newAPIError)
		}

		if concurrencyReserved {
			releaseOpencodeConcurrency(c, channel.Type, channel.Id)
			concurrencyReserved = false
		}
		if channelConcurrencyReserved {
			releaseChannelConcurrency(c, channel, channelConcurrencySlotReserved)
			channelConcurrencyReserved = false
		}
		if newAPIError != nil &&
			upstreamStatusCode(newAPIError) == http.StatusTooManyRequests &&
			newAPIError.GetErrorCode() != types.ErrorCodeChannelRateLimited &&
			channelCapacityReservedTokens > 0 {
			if err := model.RollbackChannelTPMTokens(channel, int64(channelCapacityReservedTokens)); err != nil {
				logger.LogError(c, fmt.Sprintf("failed to rollback channel TPM tokens for channel %d after upstream 429: %s", channel.Id, err.Error()))
			}
			relayInfo.ChannelTPMReservedTokens = 0
			service.NotifyChannelLimitQueues()
		}

		if newAPIError == nil {
			relayInfo.LastError = nil
			return
		}

		newAPIError = service.NormalizeViolationFeeError(newAPIError)
		if newAPIError != nil &&
			upstreamStatusCode(newAPIError) == http.StatusTooManyRequests &&
			newAPIError.GetErrorCode() != types.ErrorCodeChannelRateLimited &&
			model.ShouldRewriteUpstream429(channel) {
			newAPIError.RewriteUpstream429 = true
			newAPIError.StatusCode = http.StatusTooManyRequests
		}
		relayInfo.LastError = newAPIError

		processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError, relayInfo)
		isCodeProviderChannel := channel.Type == constant.ChannelTypeOpencode || channel.Type == constant.ChannelTypeCommand
		if retryParam.OpencodePool && service.GetCurrentChannelAffinitySelection(c) == channel.Id {
			// A failed code-provider affinity must not be reused on the next
			// attempt. Keep the existing attempted-channel filter so retrying
			// still follows the normal multi-channel chain.
			service.ClearCurrentChannelAffinityCache(c)
		}
		if isCodeProviderChannel {
			// code provider 的任何上游错误都会触发转移；这里作废失败渠道的会话绑定，
			// 使下一轮为新渠道新建会话 ID，而不是把失败渠道的旧 session 带过去造成冲突。
			// 权衡：瞬时错误（如一次超时）也会丢弃会话粘性，但换渠道后不复用失败会话、
			// 且下次不会再命中该失败渠道，优先级高于粘性收益，故保持「失败即作废」。
			service.InvalidateOpencodeSessionBinding(c, relayInfo)
		}

		// ── 同渠道自动重试（Channel Retry）──
		// 渠道配置了重试次数时，任意上游错误都先在原渠道按 1s、2s、3s… 退避重试；
		// 重试不消耗跨渠道重试预算，也不会在 use_channel 里重复记录同一渠道。
		if !retryParam.InFallback && !isCodeProviderChannel &&
			!types.IsSkipRetryError(newAPIError) &&
			(c.Request == nil || c.Request.Context().Err() == nil) {
			if delay, ok := retryParam.TryStartChannelRetry(channel, model.GetChannelRetryTimes(channel)); ok {
				// Publish the count before the wait: a successful retry returns
				// early and the log is written from this context value.
				c.Set(service.ChannelRetryCountContextKey, retryParam.ChannelRetryCount())
				service.MarkConnectionRetry(relayInfo.RequestId, retryParam.ChannelRetryCount(), time.Now().Add(delay).UnixMilli())
				if !waitChannelRetryDelay(c, delay) {
					break
				}
				service.ClearConnectionRetryWait(relayInfo.RequestId)
				retryParam.SetPendingRetryChannel(channel)
				// 同渠道重投不占用 attempt 预算：抵消本次循环自增。
				attempt--
				continue
			}
		}

		// ── 兜底序列（Fallback Sequence）失败路由 ──
		// 当尚未启动兜底且当前失败渠道配置了兜底序列时，记录主渠道首个错误并启动兜底。
		// Opencode/Command 与普通渠道一致：兜底被标记后已退出常规候选池，只有这里能进入兜底链。
		// FallbackStarted 启动后即置位，兜底渠道自身失败不会再递归触发其自身兜底序列（防无限套娃）。
		if !retryParam.FallbackStarted {
			fallbackIDs := channel.GetFallbackSequenceIDs()
			if len(fallbackIDs) > 0 {
				if retryParam.FirstError == nil {
					retryParam.FirstError = newAPIError
					retryParam.FallbackFirstErrorCodeProvider = isCodeProviderChannel
				}
				retryParam.StartFallback(fallbackIDs)
			}
		}
		if retryParam.InFallback {
			// 兜底渠道失败：不进 shouldRetry 判断，直接 continue 让 getChannel 取出下一个兜底渠道。
			// 主渠道的首个错误（FirstError）在此过程中保持不变。
			continue
		}

		if retryParam.OpencodePool && opencodeAttempts >= 4 {
			newAPIError = maskOpencodeExhaustedError(newAPIError)
			break
		}
		if !shouldRetry(c, newAPIError, maxAttempts-attempt-1) {
			break
		}
	}

	useChannel := c.GetStringSlice("use_channel")

	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}
	if newAPIError != nil {
		gopool.Go(func() {
			perfmetrics.RecordRelaySample(relayInfo, false, 0)
		})
	}
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"realtime"}, // WS 握手支持的协议，如果有使用 Sec-WebSocket-Protocol，则必须在此声明对应的 Protocol TODO add other protocol
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

func addUsedChannel(c *gin.Context, channelId int) {
	useChannel := c.GetStringSlice("use_channel")
	useChannel = append(useChannel, fmt.Sprintf("%d", channelId))
	c.Set("use_channel", useChannel)
}

func releaseOpencodeConcurrency(c *gin.Context, channelType, channelID int) {
	if channelType == 0 {
		channelType = common.GetContextKeyInt(c, constant.ContextKeyChannelType)
	}
	if channelType == 0 {
		channelType = constant.ChannelTypeOpencode
	}
	if err := model.ReleaseCodeProviderConcurrency(channelType, channelID); err != nil {
		logger.LogError(c, fmt.Sprintf("failed to release code provider concurrency for channel %d: %s", channelID, err.Error()))
	}
	service.NotifyOpencodeQueue()
}

func releaseChannelConcurrency(c *gin.Context, channel *model.Channel, concurrencySlotReserved bool) {
	if channel == nil {
		return
	}
	if concurrencySlotReserved {
		if err := model.ReleaseChannelConcurrency(channel); err != nil {
			logger.LogError(c, fmt.Sprintf("failed to release channel concurrency for channel %d: %s", channel.Id, err.Error()))
		}
	}
	// A freed slot (and any settled TPM budget) can unblock queued requests.
	service.NotifyChannelLimitQueues()
}

func upstreamStatusCode(apiError *types.NewAPIError) int {
	if apiError == nil {
		return 0
	}
	if apiError.SourceStatusCode != 0 {
		return apiError.SourceStatusCode
	}
	return apiError.StatusCode
}

func fastTokenCountMetaForPricing(request dto.Request) *types.TokenCountMeta {
	if request == nil {
		return &types.TokenCountMeta{}
	}
	meta := &types.TokenCountMeta{
		TokenType: types.TokenTypeTokenizer,
	}
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		maxCompletionTokens := lo.FromPtrOr(r.MaxCompletionTokens, uint(0))
		maxTokens := lo.FromPtrOr(r.MaxTokens, uint(0))
		if maxCompletionTokens > maxTokens {
			meta.MaxTokens = int(maxCompletionTokens)
		} else {
			meta.MaxTokens = int(maxTokens)
		}
	case *dto.OpenAIResponsesRequest:
		meta.MaxTokens = int(lo.FromPtrOr(r.MaxOutputTokens, uint(0)))
	case *dto.ClaudeRequest:
		meta.MaxTokens = int(lo.FromPtr(r.MaxTokens))
	case *dto.ImageRequest:
		// Pricing for image requests depends on ImagePriceRatio; safe to compute even when CountToken is disabled.
		return r.GetTokenCountMeta()
	default:
		// Best-effort: leave CombineText empty to avoid large allocations.
	}
	return meta
}

func getChannel(c *gin.Context, info *relaycommon.RelayInfo, retryParam *service.RetryParam) (*model.Channel, *types.NewAPIError) {
	// ── 兜底序列（Fallback Sequence）路由 ──
	// 处于兜底模式时，严格按主渠道配置的兜底序列从左到右逐个尝试，绕过 info.ChannelMeta 的池选择。
	// 若兜底渠道加载失败、已禁用或 Setup 失败，视为该兜底无效并继续尝试下一个；全部耗尽返回哨兵错误。
	if retryParam != nil && retryParam.InFallback {
		for retryParam.HasNextFallback() {
			id, _ := retryParam.NextFallbackID()
			fallbackChannel, loadErr := model.CacheGetChannel(id)
			if loadErr != nil || fallbackChannel == nil {
				// 该兜底渠道不可用，继续尝试下一个
				if loadErr != nil {
					logger.LogWarn(c, fmt.Sprintf("fallback channel #%d unavailable: %s", id, loadErr.Error()))
				} else {
					logger.LogWarn(c, fmt.Sprintf("fallback channel #%d is nil", id))
				}
				continue
			}
			if fallbackChannel.Status != common.ChannelStatusEnabled {
				logger.LogWarn(c, fmt.Sprintf("fallback channel #%d skipped: not enabled (status=%d)", id, fallbackChannel.Status))
				continue
			}
			info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
			setupErr := middleware.SetupContextForSelectedChannel(c, fallbackChannel, info.OriginModelName)
			if setupErr != nil {
				// Setup 失败（如无可用 key），视为该兜底无效，继续尝试下一个
				logger.LogWarn(c, fmt.Sprintf("fallback channel #%d setup failed: %v", id, setupErr))
				continue
			}
			return fallbackChannel, nil
		}
		// 兜底序列已全部失败，返回哨兵错误；主循环据此返回主渠道的首个错误。
		return nil, types.NewError(service.ErrFallbackExhausted, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	if retryParam.OpencodePool {
		if affinityChannel, _, fast, err := service.TryReserveOpencodeAffinityChannel(retryParam); err != nil {
			return nil, types.NewErrorWithStatusCode(err, types.ErrorCodeDoRequestFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		} else if fast {
			info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
			newAPIError := middleware.SetupContextForSelectedChannel(c, affinityChannel, info.OriginModelName)
			if newAPIError != nil {
				retryParam.OpencodeConcurrencyReserved = false
				releaseOpencodeConcurrency(c, affinityChannel.Type, affinityChannel.Id)
				return nil, newAPIError
			}
			return affinityChannel, nil
		}
		channel, _, err := service.WaitForOpencodeChannel(retryParam, info.GetEstimatePromptTokens())
		if err != nil {
			if errors.Is(err, service.ErrOpencodeQueueUnavailable) {
				fallbackParam := *retryParam
				fallbackParam.OpencodePool = false
				fallbackParam.OpencodeIncludeRPMFull = false
				fallbackParam.OpencodeConcurrencyReserved = false
				fallbackParam.ExcludeCodeProviderChannels = true
				channel, _, fallbackErr := service.CacheGetRandomSatisfiedChannel(&fallbackParam)
				if fallbackErr == nil && channel != nil {
					retryParam.OpencodePool = false
					retryParam.ExcludeCodeProviderChannels = true
					info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
					newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName)
					if newAPIError != nil {
						return nil, newAPIError
					}
					return channel, nil
				}
			}
			return nil, types.NewErrorWithStatusCode(errors.New(constant.OpencodeUpstreamExhaustedMessage), types.ErrorCodeDoRequestFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		if channel == nil {
			return nil, types.NewErrorWithStatusCode(errors.New(constant.OpencodeUpstreamExhaustedMessage), types.ErrorCodeDoRequestFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)
		newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName)
		if newAPIError != nil {
			retryParam.OpencodeConcurrencyReserved = false
			releaseOpencodeConcurrency(c, channel.Type, channel.Id)
			return nil, newAPIError
		}
		return channel, nil
	}
	if info.ChannelMeta == nil {
		autoBan := c.GetBool("auto_ban")
		autoBanInt := 1
		if !autoBan {
			autoBanInt = 0
		}
		return &model.Channel{
			Id:      c.GetInt("channel_id"),
			Type:    c.GetInt("channel_type"),
			Name:    c.GetString("channel_name"),
			AutoBan: &autoBanInt,
		}, nil
	}
	channel, selectGroup, err := service.CacheGetRandomSatisfiedChannel(retryParam)
	if err != nil {
		if retryParam.OpencodePool {
			return nil, types.NewErrorWithStatusCode(errors.New(constant.OpencodeUpstreamExhaustedMessage), types.ErrorCodeDoRequestFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		return nil, types.NewError(fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %s", selectGroup, info.OriginModelName, err.Error()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if channel == nil {
		if retryParam.OpencodePool {
			return nil, types.NewErrorWithStatusCode(errors.New(constant.OpencodeUpstreamExhaustedMessage), types.ErrorCodeDoRequestFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		return nil, types.NewError(fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", selectGroup, info.OriginModelName), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)

	newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName)
	if newAPIError != nil {
		return nil, newAPIError
	}
	return channel, nil
}

// waitChannelRetryDelay 等待同渠道重试的退避时间，客户端提前断开时返回 false。
func waitChannelRetryDelay(c *gin.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	if c == nil || c.Request == nil {
		<-timer.C
		return true
	}
	select {
	case <-timer.C:
		return true
	case <-c.Request.Context().Done():
		return false
	}
}

func shouldRetry(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	if openaiErr == nil {
		return false
	}
	channelType := common.GetContextKeyInt(c, constant.ContextKeyChannelType)
	isCodeProviderChannel := channelType == constant.ChannelTypeOpencode || channelType == constant.ChannelTypeCommand
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) && !isCodeProviderChannel {
		return false
	}
	// code provider（Opencode / Command）上游错误默认转移；400 是客户端请求问题
	// （如 context length），换渠道无法修复，立即停止并原样返回。其它护栏：重试次数、
	// specific_channel_id、客户端已取消。失败渠道的会话绑定由失败分支作废。
	if isCodeProviderChannel {
		if retryTimes <= 0 {
			return false
		}
		if _, ok := c.Get("specific_channel_id"); ok {
			return false
		}
		if c.Request != nil && c.Request.Context().Err() != nil {
			return false
		}
		if openaiErr.StatusCode == http.StatusBadRequest {
			return false
		}
		return true
	}
	if types.IsChannelError(openaiErr) {
		return true
	}
	if types.IsSkipRetryError(openaiErr) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	code := openaiErr.StatusCode
	if code >= 200 && code < 300 {
		return false
	}
	if code < 100 || code > 599 {
		return true
	}
	if operation_setting.IsAlwaysSkipRetryCode(openaiErr.GetErrorCode()) {
		return false
	}
	return operation_setting.ShouldRetryByStatusCode(code)
}

// maskOpencodeExhaustedError 在 code-provider 重试耗尽时掩盖为「上游错误」。
// 400 和启用了覆写的上游 429 原样返回，不掩盖。
func maskOpencodeExhaustedError(last *types.NewAPIError) *types.NewAPIError {
	if last != nil && (last.StatusCode == http.StatusBadRequest || last.RewriteUpstream429) {
		return last
	}
	return types.NewErrorWithStatusCode(
		errors.New(constant.OpencodeUpstreamExhaustedMessage),
		types.ErrorCodeDoRequestFailed,
		http.StatusBadGateway,
		types.ErrOptionWithSkipRetry(),
	)
}

const relayFinalErrorLogWrittenKey = "relay_final_error_log_written"

// recordFinalRelayErrorLog 将最终返回给下游的错误写入使用日志（type=5），
// 不依赖 ERROR_LOG_ENABLED，避免下游已收到错误但后台排查不到。
func recordFinalRelayErrorLog(c *gin.Context, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	if c == nil || err == nil || !types.IsRecordErrorLog(err) {
		return
	}
	if c.GetBool(relayFinalErrorLogWrittenKey) {
		return
	}
	c.Set(relayFinalErrorLogWrittenKey, true)
	writeRelayErrorLog(c, err, relayInfo)
}

func relayInfoFirstResponseMillis(info *relaycommon.RelayInfo) float64 {
	if info == nil || !info.FirstResponseTime.After(info.StartTime) {
		return 0
	}
	return float64(info.FirstResponseTime.UnixMilli() - info.StartTime.UnixMilli())
}

// errorLogGroup 与消费日志取同一来源：auto 分组在选渠后才解析出真实分组并写入
// relayInfo，而 context 里的 "group" 仍是 "auto"。
func errorLogGroup(c *gin.Context, info *relaycommon.RelayInfo) string {
	if info != nil && strings.TrimSpace(info.UsingGroup) != "" {
		return info.UsingGroup
	}
	return c.GetString("group")
}

func writeRelayErrorLog(c *gin.Context, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	userId := c.GetInt("id")
	tokenName := c.GetString("token_name")
	modelName := c.GetString("original_model")
	tokenId := c.GetInt("token_id")
	userGroup := errorLogGroup(c, relayInfo)
	frtMs := relayInfoFirstResponseMillis(relayInfo)
	channelId := c.GetInt("channel_id")
	other := make(map[string]interface{})
	if c.Request != nil && c.Request.URL != nil {
		other["request_path"] = c.Request.URL.Path
	}
	if frtMs > 0 {
		other["frt"] = frtMs
	}
	other["error_type"] = err.GetErrorType()
	other["error_code"] = err.GetErrorCode()
	other["status_code"] = err.StatusCode
	other["channel_id"] = channelId
	other["channel_name"] = c.GetString("channel_name")
	other["channel_type"] = c.GetInt("channel_type")
	adminInfo := make(map[string]interface{})
	adminInfo["use_channel"] = c.GetStringSlice("use_channel")
	isMultiKey := common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey)
	if isMultiKey {
		adminInfo["is_multi_key"] = true
		adminInfo["multi_key_index"] = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
	}
	service.AppendChannelRetryAdminInfo(c, adminInfo)
	service.AppendChannelAffinityAdminInfo(c, adminInfo)
	other["admin_info"] = adminInfo
	startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if startTime.IsZero() {
		startTime = time.Now()
	}
	useTimeSeconds := int(time.Since(startTime).Seconds())
	model.RecordErrorLog(c, userId, channelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
}

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, relayInfo *relaycommon.RelayInfo) {
	service.RecordChannelRetryFailure(c, channelError.ChannelId, channelError.ChannelName, channelError.ChannelType, err)
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.Error())))
	// 不要使用context获取渠道信息，异步处理时可能会出现渠道信息不一致的情况
	// do not use context to get channel info, there may be inconsistent channel info when processing asynchronously
	if service.ShouldDisableChannel(err) && channelError.AutoBan {
		gopool.Go(func() {
			service.DisableChannel(channelError, err.ErrorWithStatusCode())
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		writeRelayErrorLog(c, err, relayInfo)
	}

}

func RelayMidjourney(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatMjProxy, nil, nil)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"description": fmt.Sprintf("failed to generate relay info: %s", err.Error()),
			"type":        "upstream_error",
			"code":        4,
		})
		return
	}

	var mjErr *taskdto.MidjourneyResponse
	switch relayInfo.RelayMode {
	case relayconstant.RelayModeMidjourneyNotify:
		mjErr = relay.RelayMidjourneyNotify(c)
	case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		mjErr = relay.RelayMidjourneyTask(c, relayInfo.RelayMode)
	case relayconstant.RelayModeMidjourneyTaskImageSeed:
		mjErr = relay.RelayMidjourneyTaskImageSeed(c)
	case relayconstant.RelayModeSwapFace:
		mjErr = relay.RelaySwapFace(c, relayInfo)
	default:
		mjErr = relay.RelayMidjourneySubmit(c, relayInfo)
	}
	//err = relayMidjourneySubmit(c, relayMode)
	log.Println(mjErr)
	if mjErr != nil {
		statusCode := http.StatusBadRequest
		if mjErr.Code == 30 {
			mjErr.Result = "当前分组负载已饱和，请稍后再试，或升级账户以提升服务质量。"
			statusCode = http.StatusTooManyRequests
		}
		c.JSON(statusCode, gin.H{
			"description": fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result),
			"type":        "upstream_error",
			"code":        mjErr.Code,
		})
		channelId := c.GetInt("channel_id")
		logger.LogError(c, fmt.Sprintf("relay error (channel #%d, status code %d): %s", channelId, statusCode, fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result)))
	}
}

func RelayNotImplemented(c *gin.Context) {
	err := types.OpenAIError{
		Message: "API not implemented",
		Type:    "new_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	err := types.OpenAIError{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

func RelayTaskFetch(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	if taskErr := relay.RelayTaskFetch(c, relayInfo.RelayMode); taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

func RelayTask(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}

	if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
		respondTaskError(c, taskErr)
		return
	}

	var result *relay.TaskSubmitResult
	var taskErr *taskdto.TaskError
	defer func() {
		if taskErr != nil && relayInfo.Billing != nil {
			relayInfo.Billing.Refund(c)
		}
	}()

	retryParam := &service.RetryParam{
		Ctx:         c,
		TokenGroup:  relayInfo.TokenGroup,
		ModelName:   relayInfo.OriginModelName,
		RequestPath: c.Request.URL.Path,
		Retry:       common.GetPointer(0),
	}

	for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
		var channel *model.Channel

		if lockedCh, ok := relayInfo.LockedChannel.(*model.Channel); ok && lockedCh != nil {
			channel = lockedCh
			if retryParam.GetRetry() > 0 {
				if setupErr := middleware.SetupContextForSelectedChannel(c, channel, relayInfo.OriginModelName); setupErr != nil {
					taskErr = service.TaskErrorWrapperLocal(setupErr.Err, "setup_locked_channel_failed", http.StatusInternalServerError)
					break
				}
			}
		} else {
			var channelErr *types.NewAPIError
			channel, channelErr = getChannel(c, relayInfo, retryParam)
			if channelErr != nil {
				logger.LogError(c, channelErr.Error())
				taskErr = service.TaskErrorWrapperLocal(channelErr.Err, "get_channel_failed", http.StatusInternalServerError)
				break
			}
		}

		addUsedChannel(c, channel.Id)
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusRequestEntityTooLarge)
			} else {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusBadRequest)
			}
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		result, taskErr = relay.RelayTaskSubmit(c, relayInfo)
		if taskErr == nil {
			break
		}

		if !taskErr.LocalError {
			processChannelError(c,
				*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
					common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()),
				types.NewOpenAIError(taskErr.Error, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode),
				relayInfo)
		}

		if !shouldRetryTaskRelay(c, channel.Id, taskErr, common.RetryTimes-retryParam.GetRetry()) {
			break
		}
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("重试：%s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}

	// ── 成功：结算 + 日志 + 插入任务 ──
	if taskErr == nil {
		if settleErr := service.SettleBilling(c, relayInfo, result.Quota); settleErr != nil {
			common.SysError("settle task billing error: " + settleErr.Error())
		}
		service.LogTaskConsumption(c, relayInfo)

		task := model.InitTask(result.Platform, relayInfo)
		task.PrivateData.UpstreamTaskID = result.UpstreamTaskID
		task.PrivateData.BillingSource = relayInfo.BillingSource
		task.PrivateData.SubscriptionId = relayInfo.SubscriptionId
		task.PrivateData.TokenId = relayInfo.TokenId
		task.PrivateData.NodeName = common.NodeName
		task.PrivateData.BillingContext = &model.TaskBillingContext{
			ModelPrice:      relayInfo.PriceData.ModelPrice,
			GroupRatio:      relayInfo.PriceData.GroupRatioInfo.GroupRatio,
			ModelRatio:      relayInfo.PriceData.ModelRatio,
			OtherRatios:     relayInfo.PriceData.OtherRatios(),
			OriginModelName: relayInfo.OriginModelName,
			PerCallBilling:  common.StringsContains(constant.TaskPricePatches, relayInfo.OriginModelName) || relayInfo.PriceData.UsePrice,
		}
		task.Quota = result.Quota
		task.Data = result.TaskData
		task.Action = relayInfo.Action
		if insertErr := task.Insert(); insertErr != nil {
			common.SysError("insert task error: " + insertErr.Error())
		}
	}

	if taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

// respondTaskError 统一输出 Task 错误响应（含 429 限流提示改写）
func respondTaskError(c *gin.Context, taskErr *taskdto.TaskError) {
	if taskErr.StatusCode == http.StatusTooManyRequests {
		taskErr.Message = "当前分组上游负载已饱和，请稍后再试"
	}
	c.JSON(taskErr.StatusCode, taskErr)
}

func shouldRetryTaskRelay(c *gin.Context, channelId int, taskErr *taskdto.TaskError, retryTimes int) bool {
	if taskErr == nil {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	if taskErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if taskErr.StatusCode == 307 {
		return true
	}
	if taskErr.StatusCode/100 == 5 {
		// 超时不重试
		if operation_setting.IsAlwaysSkipRetryStatusCode(taskErr.StatusCode) {
			return false
		}
		return true
	}
	if taskErr.StatusCode == http.StatusBadRequest {
		return false
	}
	if taskErr.StatusCode == 408 {
		// azure处理超时不重试
		return false
	}
	if taskErr.LocalError {
		return false
	}
	if taskErr.StatusCode/100 == 2 {
		return false
	}
	return true
}
