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

func fetchOpencodeRemoteUsage(ctx context.Context, key string) (*opencodeRemoteUsage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("opencode channel key is empty")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, opencodeUsageEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create opencode usage request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")

	response, err := opencodeUsageHTTPClient.Do(request)
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
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		usage.FiveHourUsed = opencodeRemoteUsedMicroUSD(remote.RollingPercent, OpencodeFiveHourLimitMicroUSD)
		usage.WeekUsed = opencodeRemoteUsedMicroUSD(remote.WeeklyPercent, OpencodeWeekLimitMicroUSD)
		usage.MonthUsed = opencodeRemoteUsedMicroUSD(remote.MonthlyPercent, OpencodeMonthLimitMicroUSD)
		usage.FiveHourStart = remote.RollingResetAt - int64((5*time.Hour)/time.Second)
		usage.WeekStart = remote.WeeklyResetAt - int64((7*24*time.Hour)/time.Second)
		usage.MonthStart = remote.MonthlyResetAt - int64((30*24*time.Hour)/time.Second)
		usage.LastCalibratedAt = now
		usage.LastCalibrationError = ""
		usage.NextCalibrationAt = nextOpencodeUsageCalibrationAt(channelID, now)
		return tx.Save(usage).Error
	})
}

func calibrateClaimedOpencodeChannel(ctx context.Context, channelID int, now int64) error {
	channel, err := GetChannelById(channelID, true)
	if err != nil {
		return err
	}
	if channel.Type != constant.ChannelTypeOpencode {
		return errors.New("channel is not opencode")
	}
	remote, err := fetchOpencodeRemoteUsage(ctx, channel.Key)
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
func CalibrateOpencodeChannel(ctx context.Context, channelID int, now int64) (*OpencodeUsageSnapshot, error) {
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
	if err := calibrateClaimedOpencodeChannel(ctx, channelID, now); err != nil {
		return nil, err
	}
	return GetOpencodeUsageSnapshot(channelID, now)
}

// CalibrateDueOpencodeChannels processes due channels serially. The persisted
// next time is derived from the channel ID, so channels do not form one burst
// at the ten-minute boundary while the serial loop guarantees no parallel
// upstream calibration requests within this runner.
func CalibrateDueOpencodeChannels(ctx context.Context, now int64) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var channels []*Channel
	if err := DB.Where("type = ? AND status = ?", constant.ChannelTypeOpencode, common.ChannelStatusEnabled).
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
		if err := calibrateClaimedOpencodeChannel(ctx, channel.Id, now); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		calibrated++
	}
	return calibrated, firstErr
}
