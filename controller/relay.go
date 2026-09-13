package controller

import (
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
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
			if newAPIError.Error() == constant.OpencodeUpstreamExhaustedMessage {
				// Keep the public exhaustion message exact; request IDs remain in logs.
				newAPIError.SetMessage(constant.OpencodeUpstreamExhaustedMessage)
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

	relayInfo, err := relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
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
	if model.IsCodeProviderModel(relayInfo.OriginModelName) && !retryParam.OpencodePool {
		retryParam.ExcludeCodeProviderChannels = true
	}

	relayInfo.LastError = nil

	opencodeAttempts := 0
	maxAttempts := common.RetryTimes + 1
	if retryParam.OpencodePool {
		maxAttempts = 4
	}
	// 兜底模式下即使超出 maxAttempts 也必须继续，直到兜底序列全部尝试完毕或耗尽。
	for attempt := 0; attempt < maxAttempts || retryParam.InFallback; attempt++ {
		if attempt > 0 {
			retryParam.IncreaseRetry()
		}
		if retryParam.OpencodePool && opencodeAttempts >= 4 {
			newAPIError = types.NewErrorWithStatusCode(errors.New(constant.OpencodeUpstreamExhaustedMessage), types.ErrorCodeDoRequestFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			break
		}
		relayInfo.RetryIndex = retryParam.GetRetry()
		channel, channelErr := getChannel(c, relayInfo, retryParam)

		if channelErr != nil {
			// 兜底序列全部失败：返回主渠道的首个错误，不再参与全局随机重试。
			if retryParam.InFallback && !retryParam.HasNextFallback() {
				if retryParam.FirstError != nil {
					newAPIError = retryParam.FirstError
				} else {
					newAPIError = channelErr
				}
				break
			}
			if retryParam.OpencodePool {
				newAPIError = types.NewErrorWithStatusCode(errors.New(constant.OpencodeUpstreamExhaustedMessage), types.ErrorCodeDoRequestFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			} else {
				logger.LogError(c, channelErr.Error())
				newAPIError = channelErr
			}
			break
		}

		concurrencyReserved := false
		channelConcurrencyReserved := false
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
			allowed, concurrencyErr := model.ReserveChannelConcurrency(channel, common.GetTimestamp())
			if concurrencyErr != nil {
				newAPIError = types.NewError(concurrencyErr, types.ErrorCodeDoRequestFailed)
				break
			}
			if !allowed {
				newAPIError = types.NewErrorWithStatusCode(errors.New("channel concurrency limit exceeded"), types.ErrorCodeDoRequestFailed, http.StatusTooManyRequests)
				if service.GetCurrentChannelAffinitySelection(c) == channel.Id {
					service.ClearCurrentChannelAffinitySelection(c)
				}
				continue
			}
			channelConcurrencyReserved = true
		}
		addUsedChannel(c, channel.Id)
		// 并发位已由本请求实际占用：释放该请求的亲和预留租约，避免"预留 + 实际"
		// 对同一请求重复计数；请求成功结束时 RecordChannelAffinity 会重新武装租约。
		if concurrencyReserved || channelConcurrencyReserved {
			service.ConvertChannelAffinityReservationToActual(c)
		}
		if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
			if concurrencyReserved {
				releaseOpencodeConcurrency(c, channel.Type, channel.Id)
			}
			if channelConcurrencyReserved {
				releaseChannelConcurrency(c, channel)
			}
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
				releaseChannelConcurrency(c, channel)
			}
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

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

		if concurrencyReserved {
			releaseOpencodeConcurrency(c, channel.Type, channel.Id)
			concurrencyReserved = false
		}
		if channelConcurrencyReserved {
			releaseChannelConcurrency(c, channel)
			channelConcurrencyReserved = false
		}

		if newAPIError == nil {
			relayInfo.LastError = nil
			return
		}

		newAPIError = service.NormalizeViolationFeeError(newAPIError)
		relayInfo.LastError = newAPIError

		processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError)
		if retryParam.OpencodePool && service.GetCurrentChannelAffinitySelection(c) == channel.Id {
			// A failed code-provider affinity must not be reused on the next
			// attempt. Keep the existing attempted-channel filter so retrying
			// still follows the normal multi-channel chain.
			service.ClearCurrentChannelAffinityCache(c)
		}

		// ── 兜底序列（Fallback Sequence）失败路由 ──
		// 仅当尚未启动兜底、且当前失败渠道非 Opencode/Command（不干扰 code-provider 流程）
		// 并配置了兜底序列时，记录主渠道首个错误并启动兜底。FallbackStarted 启动后即置位，
		// 兜底渠道自身失败不会再递归触发其自身兜底序列（防无限套娃）。
		isCodeProviderChannel := channel.Type == constant.ChannelTypeOpencode || channel.Type == constant.ChannelTypeCommand
		if !retryParam.FallbackStarted && !isCodeProviderChannel {
			fallbackIDs := channel.GetFallbackSequenceIDs()
			if len(fallbackIDs) > 0 {
				if retryParam.FirstError == nil {
					retryParam.FirstError = newAPIError
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
			newAPIError = types.NewErrorWithStatusCode(errors.New(constant.OpencodeUpstreamExhaustedMessage), types.ErrorCodeDoRequestFailed, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
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

func releaseChannelConcurrency(c *gin.Context, channel *model.Channel) {
	if channel == nil {
		return
	}
	if err := model.ReleaseChannelConcurrency(channel); err != nil {
		logger.LogError(c, fmt.Sprintf("failed to release channel concurrency for channel %d: %s", channel.Id, err.Error()))
	}
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

func shouldRetry(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	if openaiErr == nil {
		return false
	}
	channelType := common.GetContextKeyInt(c, constant.ContextKeyChannelType)
	isCodeProviderChannel := channelType == constant.ChannelTypeOpencode || channelType == constant.ChannelTypeCommand
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) && !isCodeProviderChannel {
		return false
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

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError) {
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
		// 保存错误日志到mysql中
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		channelId := c.GetInt("channel_id")
		other := make(map[string]interface{})
		if c.Request != nil && c.Request.URL != nil {
			other["request_path"] = c.Request.URL.Path
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
				types.NewOpenAIError(taskErr.Error, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode))
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
