package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

const (
	ChannelRPMWindowSeconds    = int64(60)
	MinChannelRPMLimit         = 0
	MaxChannelRPMLimit         = 10000
	MinChannelConcurrencyLimit = 0
	MaxChannelConcurrencyLimit = 10000
)

// ChannelRPMUsage keeps legacy one-minute data and the active concurrency
// counter for ordinary channels. It is separate from the OpenCode/Command
// usage tables because those providers have their own quota lifecycle.
type ChannelRPMUsage struct {
	ID           int   `json:"id"`
	ChannelID    int   `json:"channel_id" gorm:"uniqueIndex"`
	WindowStart  int64 `json:"window_start"`
	RequestCount int   `json:"request_count"`
	// ConcurrencyCount is the number of requests currently executing on the
	// channel. It intentionally has no time window.
	ConcurrencyCount int `json:"concurrency_count"`
}

type ChannelRPMUsageSnapshot struct {
	ChannelID        int   `json:"channel_id"`
	RPMCount         int   `json:"rpm_count"`
	RPMLimit         int   `json:"rpm_limit"`
	RPMResetAt       int64 `json:"rpm_reset_at"`
	ConcurrencyCount int   `json:"concurrency_count"`
	ConcurrencyLimit int   `json:"concurrency_limit"`
}

func ValidateChannelRPMLimit(limit int) error {
	if limit < MinChannelRPMLimit || limit > MaxChannelRPMLimit {
		return fmt.Errorf("channel RPM must be between %d and %d, or 0 for unlimited", MinChannelRPMLimit, MaxChannelRPMLimit)
	}
	return nil
}

func ValidateChannelConcurrencyLimit(limit int) error {
	if limit < MinChannelConcurrencyLimit || limit > MaxChannelConcurrencyLimit {
		return fmt.Errorf("channel concurrency must be between %d and %d, or 0 for unlimited", MinChannelConcurrencyLimit, MaxChannelConcurrencyLimit)
	}
	return nil
}

func GetChannelRPMLimit(channel *Channel) int {
	return GetChannelConcurrencyLimit(channel)
}

func GetChannelConcurrencyLimit(channel *Channel) int {
	if channel == nil || channel.Type == constant.ChannelTypeOpencode || channel.Type == constant.ChannelTypeCommand {
		return 0
	}
	settings := channel.GetOtherSettings()
	limit := settings.ChannelConcurrencyLimit
	if limit == 0 {
		limit = settings.ChannelRPMLimit
	}
	if limit < MinChannelConcurrencyLimit || limit > MaxChannelConcurrencyLimit {
		return 0
	}
	return limit
}

func GetChannelConcurrencyUsageSnapshot(channelID int, now int64) (*ChannelRPMUsageSnapshot, error) {
	if channelID <= 0 {
		return nil, errors.New("invalid channel id")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	channel, err := GetChannelById(channelID, false)
	if err != nil {
		return nil, err
	}
	if channel.Type == constant.ChannelTypeOpencode || channel.Type == constant.ChannelTypeCommand {
		return nil, errors.New("channel-specific RPM is not used by code provider channels")
	}

	snapshot := &ChannelRPMUsageSnapshot{
		ChannelID:        channelID,
		RPMLimit:         GetChannelConcurrencyLimit(channel),
		ConcurrencyLimit: GetChannelConcurrencyLimit(channel),
	}
	if snapshot.RPMLimit == 0 {
		return snapshot, nil
	}
	usage, err := getOrCreateChannelRPMUsage(channelID, now)
	if err != nil {
		return nil, err
	}
	resetChannelRPMWindow(usage, now)
	snapshot.RPMCount = usage.RequestCount
	if snapshot.RPMCount < 0 {
		snapshot.RPMCount = 0
	}
	if snapshot.RPMCount > snapshot.RPMLimit {
		snapshot.RPMCount = snapshot.RPMLimit
	}
	snapshot.RPMResetAt = usage.WindowStart + ChannelRPMWindowSeconds
	snapshot.ConcurrencyCount = maxChannelConcurrency(usage.ConcurrencyCount)
	return snapshot, nil
}

func GetChannelRPMUsageSnapshot(channelID int, now int64) (*ChannelRPMUsageSnapshot, error) {
	return GetChannelConcurrencyUsageSnapshot(channelID, now)
}

func ChannelHasRPMCapacity(channel *Channel, now int64) (bool, error) {
	return ChannelHasConcurrencyCapacity(channel, now)
}

func ChannelHasConcurrencyCapacity(channel *Channel, now int64) (bool, error) {
	limit := GetChannelConcurrencyLimit(channel)
	if limit == 0 {
		return true, nil
	}
	if channel == nil || channel.Id <= 0 {
		return false, errors.New("channel is missing")
	}
	usage, err := getOrCreateChannelRPMUsage(channel.Id, now)
	if err != nil {
		return false, err
	}
	resetChannelRPMWindow(usage, now)
	return maxChannelConcurrency(usage.ConcurrencyCount) < limit, nil
}

func ReserveChannelConcurrency(channel *Channel, now int64) (bool, error) {
	limit := GetChannelConcurrencyLimit(channel)
	if limit == 0 {
		return true, nil
	}
	if channel == nil || channel.Id <= 0 {
		return false, errors.New("channel is missing")
	}
	var allowed bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateChannelRPMUsageTx(tx, channel.Id, now)
		if err != nil {
			return err
		}
		if usage.ConcurrencyCount >= limit {
			return nil
		}
		usage.ConcurrencyCount++
		allowed = true
		return tx.Save(usage).Error
	})
	return allowed, err
}

func ReleaseChannelConcurrency(channel *Channel) error {
	if GetChannelConcurrencyLimit(channel) == 0 {
		return nil
	}
	if channel == nil || channel.Id <= 0 {
		return errors.New("channel is missing")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateChannelRPMUsageTx(tx, channel.Id, common.GetTimestamp())
		if err != nil {
			return err
		}
		if usage.ConcurrencyCount <= 0 {
			return nil
		}
		usage.ConcurrencyCount--
		return tx.Save(usage).Error
	})
}

func ReserveChannelRPM(channel *Channel, now int64) (bool, int64, error) {
	limit := GetChannelRPMLimit(channel)
	if limit == 0 {
		return true, 0, nil
	}
	if channel == nil || channel.Id <= 0 {
		return false, 0, errors.New("channel is missing")
	}
	var allowed bool
	var resetAt int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateChannelRPMUsageTx(tx, channel.Id, now)
		if err != nil {
			return err
		}
		resetChannelRPMWindow(usage, now)
		resetAt = usage.WindowStart + ChannelRPMWindowSeconds
		if usage.RequestCount >= limit {
			return nil
		}
		usage.RequestCount++
		usage.ConcurrencyCount++
		allowed = true
		return tx.Save(usage).Error
	})
	return allowed, resetAt, err
}

func ReleaseChannelRPM(channel *Channel, now int64) error {
	if GetChannelRPMLimit(channel) == 0 {
		return nil
	}
	if channel == nil || channel.Id <= 0 {
		return errors.New("channel is missing")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateChannelRPMUsageTx(tx, channel.Id, now)
		if err != nil {
			return err
		}
		wasInWindow := now-usage.WindowStart < ChannelRPMWindowSeconds
		resetChannelRPMWindow(usage, now)
		if !wasInWindow || usage.RequestCount <= 0 {
			return nil
		}
		usage.RequestCount--
		if usage.ConcurrencyCount > 0 {
			usage.ConcurrencyCount--
		}
		return tx.Save(usage).Error
	})
}

func getOrCreateChannelRPMUsage(channelID int, now int64) (*ChannelRPMUsage, error) {
	var usage ChannelRPMUsage
	err := DB.Where("channel_id = ?", channelID).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = ChannelRPMUsage{ChannelID: channelID, WindowStart: now}
		err = DB.Create(&usage).Error
	}
	return &usage, err
}

func getOrCreateChannelRPMUsageTx(tx *gorm.DB, channelID int, now int64) (*ChannelRPMUsage, error) {
	var usage ChannelRPMUsage
	err := lockForUpdate(tx).Where("channel_id = ?", channelID).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = ChannelRPMUsage{ChannelID: channelID, WindowStart: now}
		err = tx.Create(&usage).Error
	}
	return &usage, err
}

func resetChannelRPMWindow(usage *ChannelRPMUsage, now int64) {
	if usage == nil || usage.WindowStart <= 0 || now-usage.WindowStart < ChannelRPMWindowSeconds {
		return
	}
	usage.WindowStart = now
	usage.RequestCount = 0
}

func maxChannelConcurrency(count int) int {
	if count < 0 {
		return 0
	}
	return count
}
