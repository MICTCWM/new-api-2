package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var opencodeUsageEndpoint = "https://opencode.ai/zen/go/v1/usage"

var opencodeUsageHTTPClient = &http.Client{Timeout: 15 * time.Second}

// OpencodeCalibrationClientFactory 按渠道构建校准用的 HTTP 客户端。由调用方
// （service/controller 层）注入，以便校准请求遵循渠道的代理设置；为 nil 时
// 退化为直连（仅测试使用）。
type OpencodeCalibrationClientFactory func(channel *Channel) (*http.Client, error)

type opencodeRemoteWindow struct {
	Status   *string  `json:"status"`
	Percent  *float64 `json:"percent"`
	ResetsAt *string  `json:"resetsAt"`
}

type opencodeRemoteUsageResponse struct {
	Usage struct {
		Rolling *opencodeRemoteWindow `json:"rolling"`
		Weekly  *opencodeRemoteWindow `json:"weekly"`
		Monthly *opencodeRemoteWindow `json:"monthly"`
	} `json:"usage"`
}

type opencodeRemoteUsage struct {
	RollingPercent float64
	WeeklyPercent  float64
	MonthlyPercent float64
	RollingResetAt int64
	WeeklyResetAt  int64
	MonthlyResetAt int64
}

func nextOpencodeUsageCalibrationAt(channelID int, now int64) int64 {
	interval := int64(OpencodeUsageCalibrationInterval / time.Second)
	if interval <= 0 {
		return now + int64((10*time.Minute)/time.Second)
	}
	offset := int64(channelID) % interval
	if offset < 0 {
		offset += interval
	}
	base := now - now%interval
	next := base + offset
	if next <= now {
		next += interval
	}
	return next
}

func fetchOpencodeRemoteUsage(ctx context.Context, key string, client *http.Client) (*opencodeRemoteUsage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("opencode channel key is empty")
	}
	if client == nil {
		client = opencodeUsageHTTPClient
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, opencodeUsageEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create opencode usage request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request opencode usage: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("opencode usage returned HTTP %d", response.StatusCode)
	}

	var payload opencodeRemoteUsageResponse
	if err := common.DecodeJson(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode opencode usage response: %w", err)
	}

	rollingPercent, rollingResetAt, err := parseOpencodeRemoteWindow("rolling", payload.Usage.Rolling)
	if err != nil {
		return nil, err
	}
	weeklyPercent, weeklyResetAt, err := parseOpencodeRemoteWindow("weekly", payload.Usage.Weekly)
	if err != nil {
		return nil, err
	}
	monthlyPercent, monthlyResetAt, err := parseOpencodeRemoteWindow("monthly", payload.Usage.Monthly)
	if err != nil {
		return nil, err
	}

	return &opencodeRemoteUsage{
		RollingPercent: rollingPercent,
		WeeklyPercent:  weeklyPercent,
		MonthlyPercent: monthlyPercent,
		RollingResetAt: rollingResetAt,
		WeeklyResetAt:  weeklyResetAt,
		MonthlyResetAt: monthlyResetAt,
	}, nil
}

func parseOpencodeRemoteWindow(name string, window *opencodeRemoteWindow) (float64, int64, error) {
	if window == nil || window.Percent == nil || window.ResetsAt == nil || strings.TrimSpace(*window.ResetsAt) == "" {
		return 0, 0, fmt.Errorf("opencode usage response is missing %s window", name)
	}
	percent := *window.Percent
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 100 {
		return 0, 0, fmt.Errorf("opencode %s usage percent is invalid", name)
	}
	resetAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*window.ResetsAt))
	if err != nil || resetAt.Unix() <= 0 {
		return 0, 0, fmt.Errorf("opencode %s usage reset time is invalid", name)
	}
	return percent, resetAt.Unix(), nil
}

func opencodeRemoteUsedMicroUSD(percent float64, limit int64) int64 {
	if percent <= 0 || limit <= 0 {
		return 0
	}
	if percent >= 100 {
		return limit
	}
	amount := decimal.NewFromFloat(percent).
		Div(decimal.NewFromInt(100)).
		Mul(decimal.NewFromInt(limit)).
		Round(0).
		IntPart()
	if amount < 0 {
		return 0
	}
	if amount > limit {
		return limit
	}
	return amount
}

func markOpencodeCalibrationAttempt(channelID int, now int64, force bool) (bool, error) {
	if channelID <= 0 || DB == nil {
		return false, errors.New("invalid opencode calibration input")
	}
	var claimed bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		if !force {
			if usage.NextCalibrationAt == 0 {
				usage.NextCalibrationAt = nextOpencodeUsageCalibrationAt(channelID, now)
				return tx.Save(usage).Error
			}
			if usage.NextCalibrationAt > now {
				return nil
			}
		}
		usage.LastCalibrationAttemptAt = now
		usage.NextCalibrationAt = nextOpencodeUsageCalibrationAt(channelID, now)
		claimed = true
		return tx.Save(usage).Error
	})
	return claimed, err
}

func recordOpencodeCalibrationFailure(channelID int, now int64, calibrationErr error) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		usage.LastCalibrationAttemptAt = now
		usage.NextCalibrationAt = nextOpencodeUsageCalibrationAt(channelID, now)
		if calibrationErr == nil {
			usage.LastCalibrationError = ""
		} else {
			usage.LastCalibrationError = calibrationErr.Error()
		}
		return tx.Save(usage).Error
	})
}

func persistOpencodeRemoteUsage(channelID int, remote *opencodeRemoteUsage, now int64) error {
	if remote == nil {
		return errors.New("opencode usage is missing")
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		// 记下覆盖前的旧月值：模型池校准需要它检测新账期（上游月用量骤降）。
		oldMonth := usage.MonthUsed
		remoteMonthUsed := opencodeRemoteUsedMicroUSD(remote.MonthlyPercent, OpencodeMonthLimitMicroUSD)
		usage.FiveHourUsed = opencodeRemoteUsedMicroUSD(remote.RollingPercent, OpencodeFiveHourLimitMicroUSD)
		usage.WeekUsed = opencodeRemoteUsedMicroUSD(remote.WeeklyPercent, OpencodeWeekLimitMicroUSD)
		usage.MonthUsed = remoteMonthUsed
		usage.FiveHourStart = remote.RollingResetAt - int64((5*time.Hour)/time.Second)
		usage.WeekStart = remote.WeeklyResetAt - int64((7*24*time.Hour)/time.Second)
		usage.MonthStart = remote.MonthlyResetAt - int64((30*24*time.Hour)/time.Second)
		usage.LastCalibratedAt = now
		usage.LastCalibrationError = ""
		usage.NextCalibrationAt = nextOpencodeUsageCalibrationAt(channelID, now)
		// 月窗口是权威锚定值：同一事务内对模型池做加权平均校准。
		if err := applyOpencodePoolCalibration(tx, channelID, remoteMonthUsed, now, oldMonth); err != nil {
			return err
		}
		return tx.Save(usage).Error
	}); err != nil {
		return err
	}
	return ReconcileCodeProviderQuotaChannel(channelID, now)
}

// applyOpencodePoolCalibration 以月窗口为权威锚定值，对当前启用的模型池做
// 加权平均校准：月滚动（上游月用量骤降过半）与上游清零时把启用池清零，否则
// 按 r = 月权威值 / 本地池总和 双向缩放，并 clamp 到各池限额。OpenCode 渠道
// 始终分池，因此只处理四个模型池。渠道不存在或非 OpenCode 时跳过（窗口照常写）。
func applyOpencodePoolCalibration(tx *gorm.DB, channelID int, remoteMonthUsed, now, oldMonth int64) error {
	channel, found, err := getOpencodeChannelTx(tx, channelID)
	if err != nil {
		return err
	}
	if !found || channel.Type != constant.ChannelTypeOpencode {
		return nil
	}
	// 先物化四个池并把遗留合并池用量迁入 flash 池，避免校准先创建空 flash
	// 行导致遗留用量无法迁移。
	if err := ensureOpencodePoolUsageRowsTx(tx, channel, now); err != nil {
		return err
	}

	type poolTarget struct {
		key   string
		limit int64
	}
	var targets []poolTarget
	if opencodeChannelServesModel(channel, OpencodeModel) {
		targets = append(targets, poolTarget{OpencodeFlashPoolKey(channelID), OpencodeFlashPoolLimitMicroUSD})
	}
	if opencodeChannelServesModel(channel, OpencodeGlmModel) {
		targets = append(targets, poolTarget{OpencodeGlmPoolKey(channelID), OpencodeGlmPoolLimitMicroUSD})
	}
	if opencodeChannelServesModel(channel, OpencodeV41FlashModel) {
		targets = append(targets, poolTarget{OpencodeV41PoolKey(channelID), OpencodeV41PoolLimitMicroUSD})
	}
	if opencodeChannelServesModel(channel, OpencodeProModel) {
		targets = append(targets, poolTarget{OpencodeProPoolKey(channelID), OpencodeProPoolLimitMicroUSD})
	}

	// 读取/创建池行（自带行锁），累加本地池总和 Σ。
	pools := make([]*OpencodePoolUsage, 0, len(targets))
	sum := int64(0)
	for _, target := range targets {
		pool, err := getOrCreateOpencodePoolUsageByKeyTx(tx, target.key, now)
		if err != nil {
			return err
		}
		sum += pool.Used
		pools = append(pools, pool)
	}

	// 月滚动（新账期检测）或上游清零：启用池清零，新账期从 0 开始。用减法比较
	// （remoteMonthUsed < oldMonth-oldMonth/2）避免上游超大值在 *2 时溢出为负数；
	// oldMonth 为本地已 clamp 的月值（≤ 月限额），减法无溢出。
	if remoteMonthUsed <= 0 || (oldMonth > 0 && remoteMonthUsed < oldMonth-oldMonth/2) {
		for _, pool := range pools {
			pool.Used, pool.ResetTime = 0, now
			if err := tx.Save(pool).Error; err != nil {
				return err
			}
		}
		return nil
	}
	// 加权平均比例缩放（双向）：本地无用量（Σ=0）时跳过，不凭空给池加量。
	if remoteMonthUsed > 0 && sum > 0 {
		ratio := decimal.NewFromInt(remoteMonthUsed).Div(decimal.NewFromInt(sum))
		for i, target := range targets {
			newUsed := decimal.NewFromInt(pools[i].Used).Mul(ratio).Round(0).IntPart()
			newUsed = minMaxOpencode(newUsed, 0, target.limit)
			if newUsed != pools[i].Used {
				pools[i].Used = newUsed
				if err := tx.Save(pools[i]).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func calibrateClaimedOpencodeChannel(ctx context.Context, channelID int, now int64, clientFactory OpencodeCalibrationClientFactory) error {
	channel, err := GetChannelById(channelID, true)
	if err != nil {
		return err
	}
	if channel.Type != constant.ChannelTypeOpencode {
		return errors.New("channel is not opencode")
	}
	var client *http.Client
	if clientFactory != nil {
		client, err = clientFactory(channel)
		if err != nil {
			if recordErr := recordOpencodeCalibrationFailure(channelID, now, err); recordErr != nil {
				return errors.Join(err, recordErr)
			}
			return err
		}
	}
	remote, err := fetchOpencodeRemoteUsage(ctx, channel.Key, client)
	if err != nil {
		if recordErr := recordOpencodeCalibrationFailure(channelID, now, err); recordErr != nil {
			return errors.Join(err, recordErr)
		}
		return err
	}
	return persistOpencodeRemoteUsage(channelID, remote, now)
}

// CalibrateOpencodeChannel forces one account-level OpenCode usage read. The
// local request accounting remains active between calibrations and is only
// replaced after a valid upstream response has been parsed completely.
func CalibrateOpencodeChannel(ctx context.Context, channelID int, now int64, clientFactory OpencodeCalibrationClientFactory) (*OpencodeUsageSnapshot, error) {
	if now <= 0 {
		now = time.Now().Unix()
	}
	claimed, err := markOpencodeCalibrationAttempt(channelID, now, true)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errors.New("opencode calibration was not claimed")
	}
	if err := calibrateClaimedOpencodeChannel(ctx, channelID, now, clientFactory); err != nil {
		return nil, err
	}
	return GetOpencodeUsageSnapshot(channelID, now)
}

// CalibrateDueOpencodeChannels processes due channels serially. The persisted
// next time is derived from the channel ID, so channels do not form one burst
// at the ten-minute boundary while the serial loop guarantees no parallel
// upstream calibration requests within this runner. clientFactory builds the
// per-channel HTTP client (proxy-aware); nil falls back to a direct client.
func CalibrateDueOpencodeChannels(ctx context.Context, now int64, clientFactory OpencodeCalibrationClientFactory) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var channels []*Channel
	if err := DB.Where("type = ? AND status IN ?", constant.ChannelTypeOpencode, []int{common.ChannelStatusEnabled, common.ChannelStatusAutoDisabled}).
		Order("id asc").Find(&channels).Error; err != nil {
		return 0, err
	}
	calibrated := 0
	var firstErr error
	for _, channel := range channels {
		if err := ctx.Err(); err != nil {
			return calibrated, err
		}
		if channel == nil {
			continue
		}
		if channel.Status == common.ChannelStatusAutoDisabled {
			quotaDisabledUntil, markerErr := GetCodeProviderQuotaDisabledUntil(channel.Id)
			if markerErr != nil {
				if firstErr == nil {
					firstErr = markerErr
				}
				continue
			}
			if quotaDisabledUntil <= 0 {
				continue
			}
		}
		claimed, err := markOpencodeCalibrationAttempt(channel.Id, now, false)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !claimed {
			continue
		}
		if err := calibrateClaimedOpencodeChannel(ctx, channel.Id, now, clientFactory); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		calibrated++
	}
	return calibrated, firstErr
}
