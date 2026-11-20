package model

import (
	"errors"
	"fmt"
	"sync"
	"time"

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
	MaxChannelRetryTimes       = 5
	MinChannelTPMLimit         = 0
	MaxChannelTPMLimit         = 100_000_000
	// ChannelTPMBufferDefault is the 浮动 headroom subtracted from an admin's
	// TPM cap before enforcement, so long streams settle before the upstream
	// capacity is exhausted.
	ChannelTPMBufferDefault = 500_000
)

// SQLite allows only one writer at a time. Serialize channel usage updates
// in-process to avoid read-to-write transaction upgrade failures during
// request bursts, which could otherwise leave concurrency reservations stale.
var channelRPMUsageWriteMu sync.Mutex

func lockChannelRPMUsageWrites() func() {
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		return func() {}
	}
	channelRPMUsageWriteMu.Lock()
	return channelRPMUsageWriteMu.Unlock
}

// Channel limit modes behind the per-channel switch.
const (
	ChannelLimitModeReject = "reject" // return 429 as soon as a limit is hit
	ChannelLimitModeQueue  = "queue"  // wait until capacity frees up
)

// ChannelQueueItem describes one request held behind a full channel limit.
type ChannelQueueItem struct {
	RequestID   string `json:"request_id"`
	UserID      int    `json:"user_id"`
	Username    string `json:"username"`
	Group       string `json:"group"`
	Model       string `json:"model"`
	InputTokens int64  `json:"input_tokens"`
	QueuedAt    int64  `json:"queued_at"` // unix ms
	Kind        string `json:"kind"`      // "concurrency" | "tpm"
	Position    int    `json:"position"`
}

// ChannelQueueEntry keeps waiting requests visible across API instances.
// LastSeenAt is refreshed by the waiting request so crashed instances do not
// leave permanent queue entries behind.
type ChannelQueueEntry struct {
	ID          int    `json:"id"`
	ChannelID   int    `json:"channel_id" gorm:"index:idx_channel_queue_channel_queued,priority:1;index:idx_channel_queue_channel_heartbeat,priority:1"`
	RequestID   string `json:"request_id" gorm:"size:128"`
	UserID      int    `json:"user_id"`
	Username    string `json:"username" gorm:"size:128"`
	Group       string `json:"group" gorm:"column:group_name;size:64"`
	Model       string `json:"model" gorm:"size:255"`
	InputTokens int64  `json:"input_tokens"`
	QueuedAt    int64  `json:"queued_at" gorm:"index:idx_channel_queue_channel_queued,priority:2;bigint"`
	Kind        string `json:"kind" gorm:"size:32"`
	LastSeenAt  int64  `json:"last_seen_at" gorm:"index:idx_channel_queue_channel_heartbeat,priority:2;bigint"`
}

func ValidateChannelTPMLimit(limit int) error {
	if limit < MinChannelTPMLimit || limit > MaxChannelTPMLimit {
		return fmt.Errorf("channel TPM must be between %d and %d, or 0 for unlimited", MinChannelTPMLimit, MaxChannelTPMLimit)
	}
	return nil
}

func ValidateChannelTPMBuffer(buffer int) error {
	if buffer < MinChannelTPMLimit || buffer > MaxChannelTPMLimit {
		return fmt.Errorf("channel TPM buffer must be between %d and %d", MinChannelTPMLimit, MaxChannelTPMLimit)
	}
	return nil
}

func validateChannelLimitMode(mode string) error {
	switch mode {
	case "", ChannelLimitModeReject, ChannelLimitModeQueue:
		return nil
	}
	return fmt.Errorf("channel limit mode must be %q or %q", ChannelLimitModeReject, ChannelLimitModeQueue)
}

func GetChannelTPMLimit(channel *Channel) int {
	if channel == nil {
		return 0
	}
	settings := channel.GetOtherSettings()
	limit := settings.ChannelTPMLimit
	if limit < MinChannelTPMLimit || limit > MaxChannelTPMLimit {
		return 0
	}
	return limit
}

func GetChannelTPMBuffer(channel *Channel) int {
	if channel == nil {
		return ChannelTPMBufferDefault
	}
	settings := channel.GetOtherSettings()
	if settings.ChannelTPMBuffer != nil &&
		*settings.ChannelTPMBuffer >= MinChannelTPMLimit &&
		*settings.ChannelTPMBuffer <= MaxChannelTPMLimit {
		return *settings.ChannelTPMBuffer
	}
	return ChannelTPMBufferDefault
}

func CreateChannelQueueEntry(channelID int, item *ChannelQueueItem, now int64) (*ChannelQueueEntry, error) {
	if DB == nil || channelID <= 0 || item == nil {
		return nil, nil
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if err := DB.Where("channel_id = ? AND last_seen_at < ?", channelID, now-60).
		Delete(&ChannelQueueEntry{}).Error; err != nil {
		return nil, err
	}
	entry := &ChannelQueueEntry{
		ChannelID:   channelID,
		RequestID:   item.RequestID,
		UserID:      item.UserID,
		Username:    item.Username,
		Group:       item.Group,
		Model:       item.Model,
		InputTokens: item.InputTokens,
		QueuedAt:    item.QueuedAt,
		Kind:        item.Kind,
		LastSeenAt:  now,
	}
	if err := DB.Create(entry).Error; err != nil {
		return nil, err
	}
	return entry, nil
}

func TouchChannelQueueEntry(id int, kind string, now int64) error {
	if DB == nil || id <= 0 {
		return nil
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	return DB.Model(&ChannelQueueEntry{}).Where("id = ?", id).Updates(map[string]any{
		"last_seen_at": now,
		"kind":         kind,
	}).Error
}

func DeleteChannelQueueEntry(id int) error {
	if DB == nil || id <= 0 {
		return nil
	}
	return DB.Delete(&ChannelQueueEntry{}, id).Error
}

func GetChannelQueueEntries(channelID int, activeAfter int64) ([]ChannelQueueItem, error) {
	if DB == nil || channelID <= 0 {
		return nil, nil
	}
	if activeAfter <= 0 {
		activeAfter = time.Now().Unix() - 60
	}
	var entries []ChannelQueueEntry
	if err := DB.Where("channel_id = ? AND last_seen_at >= ?", channelID, activeAfter).
		Order("queued_at ASC").Order("id ASC").Find(&entries).Error; err != nil {
		return nil, err
	}
	items := make([]ChannelQueueItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, ChannelQueueItem{
			RequestID:   entry.RequestID,
			UserID:      entry.UserID,
			Username:    entry.Username,
			Group:       entry.Group,
			Model:       entry.Model,
			InputTokens: entry.InputTokens,
			QueuedAt:    entry.QueuedAt,
			Kind:        entry.Kind,
		})
	}
	return items, nil
}

func HasChannelQueueEntries(channelID int, activeAfter int64) (bool, error) {
	if DB == nil || channelID <= 0 {
		return false, nil
	}
	if activeAfter <= 0 {
		activeAfter = time.Now().Unix() - 60
	}
	var count int64
	err := DB.Model(&ChannelQueueEntry{}).
		Where("channel_id = ? AND last_seen_at >= ?", channelID, activeAfter).
		Count(&count).Error
	return count > 0, err
}

func IsChannelQueueEntryFirst(id, channelID int, activeAfter int64) (bool, error) {
	if DB == nil || id <= 0 || channelID <= 0 {
		return true, nil
	}
	if activeAfter <= 0 {
		activeAfter = time.Now().Unix() - 60
	}
	var firstID int
	err := DB.Model(&ChannelQueueEntry{}).
		Select("id").
		Where("channel_id = ? AND (last_seen_at >= ? OR id = ?)", channelID, activeAfter, id).
		Order("queued_at ASC").Order("id ASC").Limit(1).
		Scan(&firstID).Error
	return firstID == 0 || firstID == id, err
}

// GetChannelEffectiveTPMLimit subtracts the buffer headroom from the cap; when
// the buffer exceeds the cap the raw limit is enforced instead.
func GetChannelEffectiveTPMLimit(channel *Channel) int {
	limit := GetChannelTPMLimit(channel)
	if limit == 0 {
		return 0
	}
	effective := limit - GetChannelTPMBuffer(channel)
	if effective <= 0 {
		return limit
	}
	return effective
}

func GetChannelLimitMode(channel *Channel) string {
	if channel == nil {
		return ChannelLimitModeReject
	}
	mode := channel.GetOtherSettings().ChannelLimitMode
	if mode == ChannelLimitModeQueue {
		return ChannelLimitModeQueue
	}
	return ChannelLimitModeReject
}

func ShouldRewriteUpstream429(channel *Channel) bool {
	return channel != nil && channel.GetOtherSettings().RewriteUpstream429
}

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
	// TokensUsed is the total tokens charged to the current one-minute TPM
	// window, including the input-token reservation of queued starters.
	TokensUsed int64 `json:"tokens_used"`
}

type ChannelRPMUsageSnapshot struct {
	ChannelID        int   `json:"channel_id"`
	RPMCount         int   `json:"rpm_count"`
	RPMLimit         int   `json:"rpm_limit"`
	RPMResetAt       int64 `json:"rpm_reset_at"`
	ConcurrencyCount int   `json:"concurrency_count"`
	ConcurrencyLimit int   `json:"concurrency_limit"`
	// TPM limit and total-token window usage.
	TPMLimit     int    `json:"tpm_limit"`
	TPMBuffer    int    `json:"tpm_buffer"`
	TPMEffective int    `json:"tpm_effective"`
	TPMCount     int64  `json:"tpm_count"`
	TPMResetAt   int64  `json:"tpm_reset_at"`
	LimitMode    string `json:"limit_mode"`
	// Queue stats (filled by the service layer, left empty in reject mode).
	QueuedConcurrencyCount int                `json:"queued_concurrency_count"`
	QueuedTPMCount         int                `json:"queued_tpm_count"`
	QueueETASeconds        int64              `json:"queue_eta_seconds"`
	QueueItems             []ChannelQueueItem `json:"queue_items"`
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

func ValidateChannelRetryTimes(times int) error {
	if times < 0 || times > MaxChannelRetryTimes {
		return fmt.Errorf("channel retry times must be between 0 and %d", MaxChannelRetryTimes)
	}
	return nil
}

// GetChannelRetryTimes returns how many same-channel retries this channel
// allows after an upstream error, clamped to the supported range.
func GetChannelRetryTimes(channel *Channel) int {
	if channel == nil {
		return 0
	}
	return normalizeChannelRetryTimes(channel.GetOtherSettings().ChannelRetryTimes)
}

// GetFallbackChannelRetryTimes returns the retry budget for a channel that is
// currently serving as a fallback. The channel must explicitly opt in through
// fallback_retry_enabled; otherwise its retry setting keeps the historical
// behavior of being ignored while in fallback mode.
func GetFallbackChannelRetryTimes(channel *Channel) int {
	if channel == nil {
		return 0
	}
	settings := channel.GetOtherSettings()
	if !settings.FallbackRetryEnabled {
		return 0
	}
	return normalizeChannelRetryTimes(settings.ChannelRetryTimes)
}

func normalizeChannelRetryTimes(times int) int {
	if times < 0 {
		return 0
	}
	if times > MaxChannelRetryTimes {
		return MaxChannelRetryTimes
	}
	return times
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
		TPMLimit:         GetChannelTPMLimit(channel),
	}
	snapshot.TPMBuffer = GetChannelTPMBuffer(channel)
	snapshot.TPMEffective = GetChannelEffectiveTPMLimit(channel)
	snapshot.LimitMode = GetChannelLimitMode(channel)
	if snapshot.RPMLimit == 0 && snapshot.TPMLimit == 0 {
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
	if snapshot.RPMLimit > 0 && snapshot.RPMCount > snapshot.RPMLimit {
		snapshot.RPMCount = snapshot.RPMLimit
	}
	snapshot.RPMResetAt = usage.WindowStart + ChannelRPMWindowSeconds
	snapshot.ConcurrencyCount = maxChannelConcurrency(usage.ConcurrencyCount)
	snapshot.TPMCount = usage.TokensUsed
	if snapshot.TPMCount < 0 {
		snapshot.TPMCount = 0
	}
	snapshot.TPMResetAt = usage.WindowStart + ChannelRPMWindowSeconds
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
	allowed, _, err := ReserveChannelCapacity(channel, now, 0)
	return allowed, err
}

// ReserveChannelCapacity atomically reserves one concurrency slot and the
// request's input tokens against the one-minute TPM window. reason names the
// exhausted limit ("concurrency" / "tpm") when allowed is false.
func ReserveChannelCapacity(channel *Channel, now int64, inputTokens int64) (allowed bool, reason string, err error) {
	if channel == nil || channel.Id <= 0 {
		return false, "", errors.New("channel is missing")
	}
	if inputTokens < 0 {
		inputTokens = 0
	}
	concurrencyLimit := GetChannelConcurrencyLimit(channel)
	tpmLimit := GetChannelEffectiveTPMLimit(channel)
	if concurrencyLimit == 0 && tpmLimit == 0 {
		return true, "", nil
	}
	unlock := lockChannelRPMUsageWrites()
	defer unlock()
	err = DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateChannelRPMUsageTx(tx, channel.Id, now)
		if err != nil {
			return err
		}
		resetChannelRPMWindow(usage, now)
		if concurrencyLimit > 0 && usage.ConcurrencyCount >= concurrencyLimit {
			reason = "concurrency"
			return nil
		}
		if usage.TokensUsed < 0 {
			usage.TokensUsed = 0
		}
		if tpmLimit > 0 && inputTokens > int64(tpmLimit)-usage.TokensUsed {
			reason = "tpm"
			return nil
		}
		if concurrencyLimit > 0 {
			usage.ConcurrencyCount++
		}
		if tpmLimit > 0 {
			usage.TokensUsed += inputTokens
		}
		allowed = true
		return tx.Save(usage).Error
	})
	return allowed, reason, err
}

// RollbackChannelTPMTokens returns unused token reservations to the window,
// for attempts that never reached the upstream.
func RollbackChannelTPMTokens(channel *Channel, inputTokens int64) error {
	if inputTokens <= 0 {
		return nil
	}
	if channel == nil || channel.Id <= 0 {
		return errors.New("channel is missing")
	}
	unlock := lockChannelRPMUsageWrites()
	defer unlock()
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateChannelRPMUsageTx(tx, channel.Id, common.GetTimestamp())
		if err != nil {
			return err
		}
		resetChannelRPMWindow(usage, common.GetTimestamp())
		if usage.TokensUsed > 0 {
			usage.TokensUsed -= inputTokens
			if usage.TokensUsed < 0 {
				usage.TokensUsed = 0
			}
			return tx.Save(usage).Error
		}
		return nil
	})
}

// SettleChannelTPMUsage replaces a request's reserved input tokens with the
// real prompt+completion total once the upstream reported usage, so the
// one-minute TPM window reflects throughput instead of the estimate. Requests
// on code-provider channels (and channels without a TPM limit) are skipped.
func SettleChannelTPMUsage(channelID int, reservedTokens, totalTokens int64) error {
	if channelID <= 0 || totalTokens < 0 {
		return nil
	}
	if reservedTokens < 0 {
		reservedTokens = 0
	}
	channel, err := GetChannelById(channelID, false)
	if err != nil || channel == nil {
		return err
	}
	if channel.Type == constant.ChannelTypeOpencode || channel.Type == constant.ChannelTypeCommand {
		return nil
	}
	if GetChannelTPMLimit(channel) == 0 && reservedTokens == 0 {
		return nil
	}
	unlock := lockChannelRPMUsageWrites()
	defer unlock()
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateChannelRPMUsageTx(tx, channel.Id, common.GetTimestamp())
		if err != nil {
			return err
		}
		resetChannelRPMWindow(usage, common.GetTimestamp())
		// Drop this request's reservation before charging the real usage so
		// the window converges on actual throughput either direction.
		usage.TokensUsed -= reservedTokens
		if usage.TokensUsed < 0 {
			usage.TokensUsed = 0
		}
		if totalTokens > 0 && usage.TokensUsed > int64(^uint64(0)>>1)-totalTokens {
			usage.TokensUsed = int64(^uint64(0) >> 1)
		} else {
			usage.TokensUsed += totalTokens
		}
		return tx.Save(usage).Error
	})
}

func ReleaseChannelConcurrency(channel *Channel) error {
	if channel == nil || channel.Id <= 0 {
		return errors.New("channel is missing")
	}
	unlock := lockChannelRPMUsageWrites()
	defer unlock()
	return DB.Model(&ChannelRPMUsage{}).
		Where("channel_id = ? AND concurrency_count <> 0", channel.Id).
		UpdateColumn("concurrency_count", gorm.Expr("CASE WHEN concurrency_count > 0 THEN concurrency_count - 1 ELSE 0 END")).Error
}

func ReserveChannelRPM(channel *Channel, now int64) (bool, int64, error) {
	limit := GetChannelRPMLimit(channel)
	if limit == 0 {
		return true, 0, nil
	}
	if channel == nil || channel.Id <= 0 {
		return false, 0, errors.New("channel is missing")
	}
	unlock := lockChannelRPMUsageWrites()
	defer unlock()
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
	if channel == nil || channel.Id <= 0 {
		return errors.New("channel is missing")
	}
	unlock := lockChannelRPMUsageWrites()
	defer unlock()
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateChannelRPMUsageTx(tx, channel.Id, now)
		if err != nil {
			return err
		}
		wasInWindow := now-usage.WindowStart < ChannelRPMWindowSeconds
		resetChannelRPMWindow(usage, now)
		changed := false
		if wasInWindow && usage.RequestCount > 0 {
			usage.RequestCount--
			changed = true
		}
		if usage.ConcurrencyCount > 0 {
			usage.ConcurrencyCount--
			changed = true
		}
		if !changed {
			return nil
		}
		return tx.Save(usage).Error
	})
}

func getOrCreateChannelRPMUsage(channelID int, now int64) (*ChannelRPMUsage, error) {
	unlock := lockChannelRPMUsageWrites()
	defer unlock()
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
	usage.TokensUsed = 0
}

func maxChannelConcurrency(count int) int {
	if count < 0 {
		return 0
	}
	return count
}
