// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Command Go 渠道账号池：一个渠道（constant.ChannelTypeCommandGo）下挂多个
// commandcode.ai 的 Go 账号，每个账号持有一把 user_* 上游密钥。
//
// ApiKey 沿用项目对渠道密钥的存储惯例：明文存储（Channel.Key 同样明文），
// 不做额外加密。若后续引入密钥加密，需在此统一处理读写。
const (
	CommandGoAccountStatusActive  = "active"
	CommandGoAccountStatusError   = "error"
	CommandGoAccountStatusNoQuota = "no_quota"

	// CommandGoAccountDefaultName 是导入后尚未拿到 whoami 账户名时的占位名。
	CommandGoAccountDefaultName = "Command Code 账户"

	// CommandGoAccountFailureThreshold 是自动标记 error（红标）的连续失败阈值。
	CommandGoAccountFailureThreshold = 20
)

// CommandGoAccount 是 Command Go 池中的单个上游账号。
type CommandGoAccount struct {
	Id                  uint   `json:"id" gorm:"primaryKey"`
	ChannelId           int    `json:"channel_id" gorm:"not null;index;uniqueIndex:idx_command_go_channel_api_key,priority:1"`
	Name                string `json:"name" gorm:"type:varchar(255);not null;default:''"`
	ApiKey              string `json:"-" gorm:"type:varchar(255);not null;uniqueIndex:idx_command_go_channel_api_key,priority:2"`
	Status              string `json:"status" gorm:"type:varchar(16);not null;default:'active';index"`
	ConsecutiveFailures int    `json:"consecutive_failures" gorm:"not null;default:0"`
	LastError           string `json:"last_error" gorm:"type:text"`
	LastUsedAt          int64  `json:"last_used_at" gorm:"bigint;not null;default:0;index"`
	LastRefreshedAt     int64  `json:"last_refreshed_at" gorm:"bigint;not null;default:0"`
	LastRefreshError    string `json:"last_refresh_error" gorm:"type:text"`
	PlanId              string `json:"plan_id" gorm:"type:varchar(128);not null;default:''"`
	// Credits 是信用余额 JSON 快照：{monthly,purchased,free,total_remaining}
	Credits string `json:"credits" gorm:"type:text"`
	// WindowUsage 是 5 小时/周/月窗口 JSON 快照。
	WindowUsage    string `json:"window_usage" gorm:"type:text"`
	QuotaAvailable bool   `json:"quota_available" gorm:"not null;default:false"`
	CreatedAt      int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt      int64  `json:"updated_at" gorm:"bigint"`
}

// CommandGoCreditsSnapshot 对应 Credits 字段的 JSON 结构。
type CommandGoCreditsSnapshot struct {
	Monthly        float64 `json:"monthly"`
	Purchased      float64 `json:"purchased"`
	Free           float64 `json:"free"`
	TotalRemaining float64 `json:"total_remaining"`
}

// CommandGoWindowSnapshot 对应 5 小时/周窗口的 JSON 结构。
type CommandGoWindowSnapshot struct {
	Used    float64 `json:"used"`
	Cap     float64 `json:"cap"`
	Percent float64 `json:"percent"`
	ResetAt int64   `json:"reset_at"`
}

// CommandGoMonthlyWindowSnapshot 对应月度窗口的 JSON 结构。
type CommandGoMonthlyWindowSnapshot struct {
	Used      float64 `json:"used"`
	Cap       float64 `json:"cap"`
	Percent   float64 `json:"percent"`
	Remaining float64 `json:"remaining"`
	Total     float64 `json:"total"`
	ResetAt   int64   `json:"reset_at"`
}

// CommandGoWindowUsageSnapshot 对应 WindowUsage 字段的 JSON 结构。
type CommandGoWindowUsageSnapshot struct {
	FiveHour CommandGoWindowSnapshot        `json:"five_hour"`
	Weekly   CommandGoWindowSnapshot        `json:"weekly"`
	Monthly  CommandGoMonthlyWindowSnapshot `json:"monthly"`
}

// CommandGoAccountSummary 是渠道账号池的状态汇总。
type CommandGoAccountSummary struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Error   int `json:"error"`
	NoQuota int `json:"no_quota"`
}

func (account *CommandGoAccount) BeforeCreate(tx *gorm.DB) error {
	if account.Status == "" {
		account.Status = CommandGoAccountStatusActive
	}
	if account.Name == "" {
		account.Name = CommandGoAccountDefaultName
	}
	return nil
}

func GetCommandGoAccount(channelID int, accountID uint) (*CommandGoAccount, error) {
	account := &CommandGoAccount{}
	if err := DB.Where("channel_id = ? AND id = ?", channelID, accountID).First(account).Error; err != nil {
		return nil, err
	}
	return account, nil
}

func ListCommandGoAccountsByChannel(channelID int) ([]CommandGoAccount, error) {
	var accounts []CommandGoAccount
	if err := DB.Where("channel_id = ?", channelID).Order("id ASC").Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

func CountCommandGoAccountsByChannel(channelID int) (int64, error) {
	var count int64
	if err := DB.Model(&CommandGoAccount{}).Where("channel_id = ?", channelID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func FindCommandGoAccountsByApiKeys(channelID int, apiKeys []string) ([]CommandGoAccount, error) {
	if len(apiKeys) == 0 {
		return nil, nil
	}
	var accounts []CommandGoAccount
	if err := DB.Where("channel_id = ? AND api_key IN ?", channelID, apiKeys).Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

func CreateCommandGoAccount(account *CommandGoAccount) error {
	return DB.Create(account).Error
}

// UpdateCommandGoAccountSnapshot 原子地写回一次刷新结果。
func UpdateCommandGoAccountSnapshot(channelID int, accountID uint, updates map[string]interface{}) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return tx.Model(&CommandGoAccount{}).
			Where("channel_id = ? AND id = ?", channelID, accountID).
			Updates(updates).Error
	})
}

// commandGoNow is overridable in tests so the round-robin cursor can be
// driven deterministically.
var commandGoNow = time.Now

// SelectCommandGoAccount 选取最久未用的可用账号并占用之（更新 last_used_at）。
// 可用账号：status=active 且（quota_available=true 或尚未刷新过的新账号）。
// 无可用账号时返回 (nil, nil)。
func SelectCommandGoAccount(channelID int) (*CommandGoAccount, error) {
	var selected *CommandGoAccount
	err := DB.Transaction(func(tx *gorm.DB) error {
		var account CommandGoAccount
		err := lockForUpdate(tx).
			Where("channel_id = ? AND status = ? AND (quota_available = ? OR last_refreshed_at = 0)",
				channelID, CommandGoAccountStatusActive, true).
			Order("last_used_at ASC").Order("id ASC").First(&account).Error
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		// Make the cursor strictly increasing even under same-second
		// concurrency, so N selections rotate evenly instead of always
		// picking the smallest id.
		next := commandGoNow().Unix()
		if account.LastUsedAt >= next {
			next = account.LastUsedAt + 1
		}
		if err := tx.Model(&CommandGoAccount{}).
			Where("id = ?", account.Id).
			Update("last_used_at", next).Error; err != nil {
			return err
		}
		account.LastUsedAt = next
		selected = &account
		return nil
	})
	if err != nil {
		return nil, err
	}
	return selected, nil
}

// PickCommandGoAccountForTest selects a usable account without touching the
// round-robin cursor or first-failure counters. Channel tests must not affect
// pool-account health accounting.
func PickCommandGoAccountForTest(channelID int) (*CommandGoAccount, error) {
	var account CommandGoAccount
	err := DB.Where("channel_id = ? AND status = ? AND (quota_available = ? OR last_refreshed_at = 0)",
		channelID, CommandGoAccountStatusActive, true).
		Order("id ASC").First(&account).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &account, nil
}

func RecordCommandGoAccountSuccess(accountID uint) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return tx.Model(&CommandGoAccount{}).
			Where("id = ?", accountID).
			Updates(map[string]interface{}{
				"consecutive_failures": 0,
				"last_error":           "",
			}).Error
	})
}

// RecordCommandGoAccountFailure 累加连续失败次数；达到阈值后置 error（红标）。
// 该状态只能通过 RecoverCommandGoAccounts 手动恢复。
func RecordCommandGoAccountFailure(accountID uint, errMsg string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var account CommandGoAccount
		if err := lockForUpdate(tx).Where("id = ?", accountID).First(&account).Error; err != nil {
			return err
		}
		account.ConsecutiveFailures++
		account.LastError = errMsg
		if account.ConsecutiveFailures >= CommandGoAccountFailureThreshold {
			account.Status = CommandGoAccountStatusError
		}
		return tx.Model(&CommandGoAccount{}).
			Where("id = ?", accountID).
			Updates(map[string]interface{}{
				"consecutive_failures": account.ConsecutiveFailures,
				"last_error":           account.LastError,
				"status":               account.Status,
			}).Error
	})
}

func MarkCommandGoAccountNoQuota(accountID uint, reason string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{"status": CommandGoAccountStatusNoQuota}
		if reason != "" {
			updates["last_error"] = reason
		}
		return tx.Model(&CommandGoAccount{}).
			Where("id = ?", accountID).
			Updates(updates).Error
	})
}

// RecoverCommandGoAccounts 手动恢复账号：status→active、连续失败清零，并清除
// quota_available 的旧判定（管理员覆盖）。后续刷新会重新判定额度可用性。
func RecoverCommandGoAccounts(channelID int, ids []uint) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var affected int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&CommandGoAccount{}).
			Where("channel_id = ? AND id IN ?", channelID, ids).
			Updates(map[string]interface{}{
				"status":               CommandGoAccountStatusActive,
				"consecutive_failures": 0,
				"quota_available":      true,
			})
		affected = result.RowsAffected
		return result.Error
	})
	return affected, err
}

// DeleteCommandGoAccounts 按 id 删除；ids 为空时按 status 批量删除（仅 error/no_quota）。
func DeleteCommandGoAccounts(channelID int, ids []uint, status string) (int64, error) {
	if len(ids) == 0 {
		if status != CommandGoAccountStatusError && status != CommandGoAccountStatusNoQuota {
			return 0, fmt.Errorf("必须提供账号 id，或指定 error/no_quota 状态进行批量删除")
		}
	}
	var affected int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("channel_id = ?", channelID)
		if len(ids) > 0 {
			query = query.Where("id IN ?", ids)
		} else {
			query = query.Where("status = ?", status)
		}
		result := query.Delete(&CommandGoAccount{})
		affected = result.RowsAffected
		return result.Error
	})
	return affected, err
}

// deleteCommandGoAccountsForChannels 删除渠道下的全部池账号（渠道删除时级联）。
// 表尚未迁移时跳过，兼容旧库与测试夹具。
func deleteCommandGoAccountsForChannels(tx *gorm.DB, channelIDs []int) error {
	if len(channelIDs) == 0 || !tx.Migrator().HasTable(&CommandGoAccount{}) {
		return nil
	}
	return tx.Where("channel_id IN ?", channelIDs).Delete(&CommandGoAccount{}).Error
}

// CopyCommandGoAccounts 复制源渠道的池账号到目标渠道。
func CopyCommandGoAccounts(srcChannelID, dstChannelID int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return copyCommandGoAccountsTx(tx, srcChannelID, dstChannelID)
	})
}

// copyCommandGoAccountsTx copies pool accounts inside an existing transaction.
func copyCommandGoAccountsTx(tx *gorm.DB, srcChannelID, dstChannelID int) error {
	if !tx.Migrator().HasTable(&CommandGoAccount{}) {
		return nil
	}
	var accounts []CommandGoAccount
	if err := tx.Where("channel_id = ?", srcChannelID).Find(&accounts).Error; err != nil {
		return err
	}
	for i := range accounts {
		clone := accounts[i]
		clone.Id = 0
		clone.ChannelId = dstChannelID
		clone.LastUsedAt = 0
		clone.CreatedAt = 0
		clone.UpdatedAt = 0
		if err := tx.Create(&clone).Error; err != nil {
			return err
		}
	}
	return nil
}
