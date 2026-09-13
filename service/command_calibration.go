// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Command 用量校准编排：复用 commandcode.ai 额度查询客户端，把上游返回的
// 小时/周/月窗口写回本地用量（模型池仍以本地按请求估算为准）。每次额度
// 查询（管理面板）与后台定时任务都走同一条链路，保证"校准为准"。

func commandWindowCalibrationFromReport(report *CommandcodeQuotaReport) (model.CommandWindowCalibration, error) {
	calib := model.CommandWindowCalibration{}
	if report == nil {
		return calib, errors.New("command quota report is missing")
	}
	if report.Credits != nil {
		if w := report.Credits.FiveHour; w != nil {
			calib.HasFiveHour = true
			calib.FiveHourUsed = usdToMicroUSD(w.Used)
			if w.ResetAt > 0 {
				calib.FiveHourReset = w.ResetAt / 1000
			}
		}
		if w := report.Credits.Weekly; w != nil {
			calib.HasWeek = true
			calib.WeekUsed = usdToMicroUSD(w.Used)
			if w.ResetAt > 0 {
				calib.WeekReset = w.ResetAt / 1000
			}
		}
	}
	if report.Usage != nil {
		calib.HasMonth = true
		calib.MonthUsed = usdToMicroUSD(report.Usage.TotalCredits)
	}
	if !calib.HasFiveHour && !calib.HasWeek && !calib.HasMonth {
		return calib, errors.New("command quota report has no window data")
	}
	return calib, nil
}

func usdToMicroUSD(value float64) int64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	micro := value * 1_000_000
	if micro >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(math.Round(micro))
}

// CalibrateCommandChannelWithClient 用给定客户端执行一次渠道校准并落库。
// 返回 applied 表示本次是否实际应用了校准。
func CalibrateCommandChannelWithClient(ctx context.Context, channel *model.Channel, client *http.Client, now time.Time, force bool) (bool, error) {
	if channel == nil {
		return false, errors.New("command channel is missing")
	}
	return model.CalibrateCommandChannelFromUpstream(ctx, channel, now.Unix(), force,
		func(fetchCtx context.Context, ch *model.Channel) (model.CommandWindowCalibration, error) {
			report, err := FetchCommandcodeQuotaReport(
				fetchCtx,
				client,
				CommandcodeOriginFromBaseURL(ch.GetBaseURL()),
				strings.TrimSpace(ch.Key),
			)
			if err != nil {
				return model.CommandWindowCalibration{}, err
			}
			return commandWindowCalibrationFromReport(report)
		})
}

// CalibrateDueCommandChannels 串行校准到期的 Command 渠道。逐渠道构建
// 代理感知客户端；单个渠道失败只记录校准错误，不中断整轮。
func CalibrateDueCommandChannels(ctx context.Context, now time.Time) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	channels, err := model.ListCommandCalibrationCandidates()
	if err != nil {
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
			quotaDisabledUntil, markerErr := model.GetCodeProviderQuotaDisabledUntil(channel.Id)
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
		client, clientErr := GetHttpClientWithProxy(channel.GetSetting().Proxy)
		if clientErr != nil {
			_, calibErr := model.CalibrateCommandChannelFromUpstream(ctx, channel, now.Unix(), false,
				func(context.Context, *model.Channel) (model.CommandWindowCalibration, error) {
					return model.CommandWindowCalibration{}, clientErr
				})
			if firstErr == nil {
				firstErr = calibErr
			}
			continue
		}
		applied, err := CalibrateCommandChannelWithClient(ctx, channel, client, now, false)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if applied {
			calibrated++
		}
	}
	return calibrated, firstErr
}
