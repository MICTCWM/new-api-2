// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// Command 用量校准：以 commandcode.ai 额度查询（/alpha/billing/credits 与
// /alpha/usage/summary）返回的小时/周/月窗口为准，覆盖本地按请求估算的
// 窗口累计。模型池（flash/4.1/4p）上游不提供拆分，仍以本地估算为准。
// HTTP 编排（客户端工厂、上游取数）位于 service 层，本文件只负责登记
// 与落库。

// CommandWindowCalibration 是一次校准得到的窗口用量（微美元）。
// ResetAt 为上游窗口重置时间（epoch 秒，0 表示未知）。
type CommandWindowCalibration struct {
	FiveHourUsed   int64
	FiveHourReset  int64
	WeekUsed       int64
	WeekReset      int64
	MonthUsed      int64
	HasFiveHour    bool
	HasWeek        bool
	HasMonth       bool
	ResetAllZeroOK bool // 上游明确返回 0 用量时允许清零
}

// commandUsageCalibrationInterval 与 OpenCode 校准保持一致的 10 分钟节拍，
// 按渠道 ID 错开，避免同一时刻并发请求上游。
const commandUsageCalibrationInterval = 10 * 60

func nextCommandUsageCalibrationAt(channelID int, now int64) int64 {
	interval := int64(commandUsageCalibrationInterval)
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

// ListCommandCalibrationCandidates 返回参与校准的 Command 渠道：启用中的
// 渠道，以及因额度窗口耗尽被自动停用（仍需跟踪恢复）的渠道。
func ListCommandCalibrationCandidates() ([]*Channel, error) {
	if DB == nil || !DB.Migrator().HasTable("channels") {
		return nil, nil
	}
	var channels []*Channel
	err := DB.Where("type = ? AND status IN ?", constant.ChannelTypeCommand,
		[]int{common.ChannelStatusEnabled, common.ChannelStatusAutoDisabled}).
		Order("id asc").Find(&channels).Error
	return channels, err
}

// ClaimCommandCalibrationAttempt 非强制认领一次校准：未到期返回 false。
// 认领即写入最近尝试时间并推进下次到期时间。
func ClaimCommandCalibrationAttempt(channelID int, now int64) (bool, error) {
	if channelID <= 0 {
		return false, errors.New("invalid command calibration input")
	}
	var claimed bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateCommandChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		if usage.NextCalibrationAt == 0 {
			usage.NextCalibrationAt = nextCommandUsageCalibrationAt(channelID, now)
			return tx.Save(usage).Error
		}
		if usage.NextCalibrationAt > now {
			return nil
		}
		usage.LastCalibrationAttemptAt = now
		usage.NextCalibrationAt = nextCommandUsageCalibrationAt(channelID, now)
		claimed = true
		return tx.Save(usage).Error
	})
	return claimed, err
}

// RecordCommandCalibrationFailure 记录一次失败校准（不推进到期时间之外的状态）。
func RecordCommandCalibrationFailure(channelID int, now int64, calibrationErr error) error {
	if channelID <= 0 || calibrationErr == nil {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateCommandChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		usage.CalibrationLastError = calibrationErr.Error()
		return tx.Save(usage).Error
	})
}

// alignCalibratedWindowStart 将本地窗口起点与上游重置时间对齐：
// 上游给了未来的 resetAt → start = resetAt - 窗口长度；
// resetAt 已过期 → 视为新窗口起点 now；否则沿用本地未过期的起点。
func alignCalibratedWindowStart(now, resetAt, windowLen, currentStart int64) int64 {
	if resetAt > 0 {
		if resetAt <= now {
			return now
		}
		start := resetAt - windowLen
		if start <= now && now-start < 2*windowLen {
			return start
		}
	}
	if currentStart == 0 || now-currentStart >= windowLen {
		return now
	}
	return currentStart
}

// ApplyCommandUsageCalibration 用上游校准值覆盖小时/周/月窗口的本地估算。
// 仅覆盖 Has* 标记的窗口；ResetAllZeroOK 允许上游 0 值清零本地累计。
func ApplyCommandUsageCalibration(channelID int, calib CommandWindowCalibration, now int64) error {
	if channelID <= 0 {
		return errors.New("invalid command calibration channel")
	}
	if !calib.HasFiveHour && !calib.HasWeek && !calib.HasMonth {
		return errors.New("command calibration payload has no window data")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateCommandChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		if calib.HasFiveHour {
			if calib.FiveHourUsed > 0 || calib.ResetAllZeroOK {
				usage.FiveHourUsed = minMaxOpencode(calib.FiveHourUsed, 0, CommandFiveHourLimitMicroUSD)
			}
			usage.FiveHourStart = alignCalibratedWindowStart(now, calib.FiveHourReset, 5*60*60, usage.FiveHourStart)
		}
		if calib.HasWeek {
			if calib.WeekUsed > 0 || calib.ResetAllZeroOK {
				usage.WeekUsed = minMaxOpencode(calib.WeekUsed, 0, CommandWeekLimitMicroUSD)
			}
			usage.WeekStart = alignCalibratedWindowStart(now, calib.WeekReset, 7*24*60*60, usage.WeekStart)
		}
		if calib.HasMonth {
			if calib.MonthUsed > 0 || calib.ResetAllZeroOK {
				usage.MonthUsed = minMaxOpencode(calib.MonthUsed, 0, CommandMonthLimitMicroUSD)
			}
			if usage.MonthStart == 0 {
				usage.MonthStart = now
			}
		}
		usage.CalibrationLastError = ""
		return tx.Save(usage).Error
	})
}

// CalibrateCommandChannelFromUpstream 供 service 层编排：认领 → 上游取数 →
// 落库。fetch 返回 (calibration, error)。返回值 applied 表示本次是否实际
// 完成了校准（非强制路径下未到期会返回 false, nil）。
func CalibrateCommandChannelFromUpstream(
	ctx context.Context,
	channel *Channel,
	now int64,
	force bool,
	fetch func(ctx context.Context, channel *Channel) (CommandWindowCalibration, error),
) (bool, error) {
	if channel == nil || channel.Type != constant.ChannelTypeCommand {
		return false, errors.New("channel is not command")
	}
	if fetch == nil {
		return false, errors.New("command calibration fetch is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !force {
		claimed, err := ClaimCommandCalibrationAttempt(channel.Id, now)
		if err != nil {
			return false, err
		}
		if !claimed {
			return false, nil
		}
	} else {
		// 强制路径不因未到期而跳过；登记字段存在即可。
		_, _ = ClaimCommandCalibrationAttempt(channel.Id, now)
	}
	calib, err := fetch(ctx, channel)
	if err != nil {
		if recordErr := RecordCommandCalibrationFailure(channel.Id, now, err); recordErr != nil {
			return false, errors.Join(err, recordErr)
		}
		return false, err
	}
	if err := ApplyCommandUsageCalibration(channel.Id, calib, now); err != nil {
		return false, err
	}
	return true, nil
}
