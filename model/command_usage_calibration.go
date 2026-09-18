// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Command 用量校准：以 commandcode.ai 额度查询（/alpha/billing/credits 与
// /alpha/usage/summary）返回的小时/周/月窗口为准，覆盖本地按请求估算的
// 窗口累计。模型池（flash/4.1/4p）上游不提供拆分，仍以本地估算为准，
// 但以月窗口为权威锚定值做加权平均缩放，抑制长期漂移。
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
		// 记下覆盖前的旧月值：模型池校准需要它检测新账期（上游月用量骤降）。
		oldMonth := usage.MonthUsed
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
			// 月窗口是权威锚定值：同一事务内对模型池做加权平均校准。
			if err := applyCommandPoolCalibration(tx, channelID, calib, now, oldMonth); err != nil {
				return err
			}
		}
		usage.CalibrationLastError = ""
		return tx.Save(usage).Error
	})
}

// applyCommandPoolCalibration 以月窗口为权威锚定值，对当前启用的模型池做
// 加权平均校准：月滚动（上游月用量骤降过半）与上游显式清零时把启用池
// 清零，否则按 r = 月权威值 / 本地池总和 双向缩放，并 clamp 到各池限额。
// 仅在 calib.HasMonth 时调用；渠道不存在或非 Command 时跳过（窗口照常写）。
func applyCommandPoolCalibration(tx *gorm.DB, channelID int, calib CommandWindowCalibration, now, oldMonth int64) error {
	channel, found, err := getCommandChannelTx(tx, channelID)
	if err != nil {
		return err
	}
	if !found || channel.Type != constant.ChannelTypeCommand {
		return nil
	}

	// 确定当前启用的池：分池按模型逐个判定，否则走合并池。
	type poolTarget struct {
		key   string
		limit int64
	}
	var targets []poolTarget
	if commandChannelUsesSplitPools(channel) {
		if commandChannelServesModel(channel, CommandFlashModel) {
			targets = append(targets, poolTarget{CommandFlashPoolKey(channelID), CommandFlashPoolLimitMicroUSD})
		}
		if commandChannelServesModel(channel, CommandV41FlashModel) {
			targets = append(targets, poolTarget{CommandV41PoolKey(channelID), CommandV41PoolLimitMicroUSD})
		}
		if commandChannelServesModel(channel, CommandProModel) {
			targets = append(targets, poolTarget{CommandProPoolKey(channelID), CommandProPoolLimitMicroUSD})
		}
	} else {
		targets = append(targets, poolTarget{CommandPoolKey(channelID), CommandCombinedPoolLimitMicroUSD})
	}

	// 读取/创建池行（自带行锁），累加本地池总和 Σ。
	pools := make([]*CommandPoolUsage, 0, len(targets))
	sum := int64(0)
	for _, target := range targets {
		pool, err := getOrCreateCommandPoolUsageByKeyTx(tx, target.key, now)
		if err != nil {
			return err
		}
		sum += pool.Used
		pools = append(pools, pool)
	}

	// 月滚动（新账期检测）：上游月用量骤降超过一半 → 启用池清零，新账期从 0 开始。
	// 用减法比较（calib.MonthUsed < oldMonth-oldMonth/2）避免上游超大值在 *2 时
	// 溢出为负数而误判；oldMonth 为本地已 clamp 的月值（≤ 月限额），减法无溢出。
	if oldMonth > 0 && calib.MonthUsed < oldMonth-oldMonth/2 {
		for _, pool := range pools {
			pool.Used, pool.ResetTime = 0, now
			if err := tx.Save(pool).Error; err != nil {
				return err
			}
		}
		return nil
	}
	// 上游明确清零：月窗口权威值为 0 且允许清零 → 启用池清零。
	if calib.MonthUsed <= 0 && calib.ResetAllZeroOK {
		for _, pool := range pools {
			pool.Used, pool.ResetTime = 0, now
			if err := tx.Save(pool).Error; err != nil {
				return err
			}
		}
		return nil
	}
	// 加权平均比例缩放（双向）：本地无用量（Σ=0）时跳过，不凭空给池加量。
	if calib.MonthUsed > 0 && sum > 0 {
		ratio := decimal.NewFromInt(calib.MonthUsed).Div(decimal.NewFromInt(sum))
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
	// calib.MonthUsed == 0 且不允许清零时保持池不动。
	return nil
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
