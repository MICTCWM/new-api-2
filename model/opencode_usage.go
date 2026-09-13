package model

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	OpencodeModel                          = "deepseek-v4-flash"
	OpencodeGlmModel                       = "glm-5.3-flash"
	OpencodeFiveHourLimitMicroUSD    int64 = 12_000_000
	OpencodeWeekLimitMicroUSD        int64 = 30_000_000
	OpencodeMonthLimitMicroUSD       int64 = 60_000_000
	OpencodeUsageCalibrationInterval       = 10 * time.Minute
	// Each OpenCode channel has one combined pool (formerly peak + off-peak $30 lanes).
	OpencodePoolLimitMicroUSD        int64 = 60_000_000
	OpencodeMinimumRemainingMicroUSD int64 = 200_000
	OpencodePoolKeyPrefix                  = "opencode"
	// OpencodeRPMLimit is kept as the default for compatibility with existing callers.
	// Runtime checks use operation_setting.GetOpencodeRPMLimit().
	OpencodeRPMLimit = operation_setting.DefaultOpencodeRPMLimit
)

// OpencodeNativeModels are the upstream models this channel type can call
// directly (without model mapping).
var OpencodeNativeModels = []string{OpencodeModel, OpencodeGlmModel}

func IsOpencodeNativeModel(modelName string) bool {
	switch strings.TrimSpace(modelName) {
	case OpencodeModel, OpencodeGlmModel:
		return true
	default:
		return false
	}
}

const (
	OpencodePricingPeak    = "peak"
	OpencodePricingOffPeak = "off_peak"
)

type OpencodeChannelUsage struct {
	ID               int   `json:"id"`
	ChannelID        int   `json:"channel_id" gorm:"uniqueIndex"`
	FiveHourUsed     int64 `json:"five_hour_used"`
	FiveHourStart    int64 `json:"five_hour_start"`
	WeekUsed         int64 `json:"week_used"`
	WeekStart        int64 `json:"week_start"`
	MonthUsed        int64 `json:"month_used"`
	MonthStart       int64 `json:"month_start"`
	RPMWindowStart   int64 `json:"rpm_window_start"`
	RPMCount         int   `json:"rpm_count"`
	ConcurrencyCount int   `json:"concurrency_count"`

	// Calibration state is kept with the local counters so each channel has an
	// independent schedule and a failed upstream read cannot erase local usage.
	LastCalibrationAttemptAt int64  `json:"last_calibration_attempt_at" gorm:"index"`
	LastCalibratedAt         int64  `json:"last_calibrated_at" gorm:"index"`
	NextCalibrationAt        int64  `json:"next_calibration_at" gorm:"index"`
	LastCalibrationError     string `json:"last_calibration_error" gorm:"type:text"`
	// QuotaDisabledUntil is set only when the channel was auto-disabled because
	// an upstream rolling quota was exhausted. It is the marker that permits
	// the quota reconciler to re-enable the channel later without touching
	// manually disabled or error-disabled channels.
	QuotaDisabledUntil int64 `json:"quota_disabled_until" gorm:"index"`
}

type OpencodePoolUsage struct {
	ID        int    `json:"id"`
	PoolKey   string `json:"pool_key" gorm:"uniqueIndex;size:128"`
	Used      int64  `json:"used"`
	ResetTime int64  `json:"reset_time"`
}

type OpencodeUsageWindow struct {
	UsedMicroUSD      int64   `json:"used_micro_usd"`
	LimitMicroUSD     int64   `json:"limit_micro_usd"`
	RemainingMicroUSD int64   `json:"remaining_micro_usd"`
	UsedUSD           float64 `json:"used_usd"`
	LimitUSD          float64 `json:"limit_usd"`
	RemainingUSD      float64 `json:"remaining_usd"`
	Percent           float64 `json:"percent"`
	ResetAt           int64   `json:"reset_at"`
}

type OpencodeUsageSnapshot struct {
	ChannelID                   int                         `json:"channel_id"`
	FiveHour                    OpencodeUsageWindow         `json:"five_hour"`
	Week                        OpencodeUsageWindow         `json:"week"`
	Month                       OpencodeUsageWindow         `json:"month"`
	Pool                        OpencodeUsageWindow         `json:"pool"`
	PeakPool                    OpencodeUsageWindow         `json:"peak_pool"`     // deprecated: always empty; kept for API compatibility
	OffPeakPool                 OpencodeUsageWindow         `json:"off_peak_pool"` // deprecated: always empty; kept for API compatibility
	PricingPeriod               string                      `json:"pricing_period"`
	RPMCount                    int                         `json:"rpm_count"`
	RPMLimit                    int                         `json:"rpm_limit"`
	RPMResetAt                  int64                       `json:"rpm_reset_at"`
	AffinityReservedRPM         int                         `json:"affinity_reserved_rpm"`
	AvailableRPM                int                         `json:"available_rpm"`
	ConcurrencyCount            int                         `json:"concurrency_count"`
	ConcurrencyLimit            int                         `json:"concurrency_limit"`
	AffinityReservedConcurrency int                         `json:"affinity_reserved_concurrency"`
	AvailableConcurrency        int                         `json:"available_concurrency"`
	Calibration                 OpencodeCalibrationSnapshot `json:"calibration"`
}

type OpencodeCalibrationSnapshot struct {
	LastAttemptAt int64  `json:"last_attempt_at"`
	LastSuccessAt int64  `json:"last_success_at"`
	NextAt        int64  `json:"next_at"`
	LastError     string `json:"last_error,omitempty"`
}

type OpencodeCapacitySnapshot struct {
	TotalCapacity     int `json:"total_capacity"`
	AvailableCapacity int `json:"available_capacity"`
}

// CodeProviderUsageOverride contains the already validated micro-dollar
// values used by the admin usage editor. Pool fields are optional because
// Command may use split model pools. OpenCode uses a single combined pool.
type CodeProviderUsageOverride struct {
	FiveHourUsedMicroUSD    int64
	WeekUsedMicroUSD        int64
	MonthUsedMicroUSD       int64
	PoolUsedMicroUSD        int64
	PeakPoolUsedMicroUSD    *int64 // deprecated for OpenCode
	OffPeakPoolUsedMicroUSD *int64 // deprecated for OpenCode
	FlashPoolUsedMicroUSD   *int64
	ProPoolUsedMicroUSD     *int64
}

func IsOpencodePeak(t time.Time) bool {
	hour := t.UTC().Hour()
	return (hour >= 1 && hour < 4) || (hour >= 6 && hour < 10)
}

func OpencodePricingPeriodAt(t time.Time) string {
	if IsOpencodePeak(t) {
		return OpencodePricingPeak
	}
	return OpencodePricingOffPeak
}

func OpencodePoolKeyAt(channelID int, _ time.Time) string {
	return opencodePoolKey(channelID)
}

func OpencodePoolKeysForChannel(channelID int) []string {
	return []string{opencodePoolKey(channelID)}
}

func opencodePoolKey(channelID int) string {
	return fmt.Sprintf("%s:%d", OpencodePoolKeyPrefix, channelID)
}

// Legacy peak/off-peak pool keys kept for one-time migration into the combined pool.
func opencodeLegacyPoolKeyForPeriod(channelID int, pricingPeriod string) string {
	return fmt.Sprintf("%s:%d:%s", OpencodePoolKeyPrefix, channelID, pricingPeriod)
}

func GetOpencodeCapacitySnapshot(now int64) (*OpencodeCapacitySnapshot, error) {
	var channels []*Channel
	if err := DB.Where("type = ? AND status = ?", constant.ChannelTypeOpencode, common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil, err
	}

	limit := operation_setting.GetOpencodeConcurrencyLimit()
	snapshot := &OpencodeCapacitySnapshot{TotalCapacity: len(channels) * limit}
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		quotaAvailable, err := OpencodeChannelHasQuotaCapacity(channel.Id, now)
		if err != nil {
			return nil, err
		}
		if !quotaAvailable {
			if quotaErr := ReconcileCodeProviderQuotaChannel(channel.Id, now); quotaErr != nil {
				return nil, quotaErr
			}
			continue
		}
		usage, err := getOrCreateOpencodeChannelUsage(channel.Id, now)
		if err != nil {
			return nil, err
		}
		resetOpencodeChannelWindows(usage, now)
		available := limit - normalizeOpencodeConcurrency(usage.ConcurrencyCount)
		if available > 0 {
			snapshot.AvailableCapacity += available
		}
	}
	return snapshot, nil
}

func OpencodeChannelHasCapacity(channelID int, now int64) (bool, error) {
	usage, err := getOrCreateOpencodeChannelUsage(channelID, now)
	if err != nil {
		return false, err
	}
	pool, err := getOrCreateOpencodePoolUsage(channelID, now)
	if err != nil {
		return false, err
	}
	resetOpencodeChannelWindows(usage, now)
	return remainingOpencode(usage.FiveHourUsed, OpencodeFiveHourLimitMicroUSD) > OpencodeMinimumRemainingMicroUSD &&
		remainingOpencode(usage.WeekUsed, OpencodeWeekLimitMicroUSD) > OpencodeMinimumRemainingMicroUSD &&
		remainingOpencode(usage.MonthUsed, OpencodeMonthLimitMicroUSD) > OpencodeMinimumRemainingMicroUSD &&
		remainingOpencode(pool.Used, OpencodePoolLimitMicroUSD) > OpencodeMinimumRemainingMicroUSD &&
		usage.ConcurrencyCount < operation_setting.GetOpencodeConcurrencyLimit(), nil
}

func OpencodeChannelHasQuotaCapacity(channelID int, now int64) (bool, error) {
	usage, err := getOrCreateOpencodeChannelUsage(channelID, now)
	if err != nil {
		return false, err
	}
	pool, err := getOrCreateOpencodePoolUsage(channelID, now)
	if err != nil {
		return false, err
	}
	resetOpencodeChannelWindows(usage, now)
	return remainingOpencode(usage.FiveHourUsed, OpencodeFiveHourLimitMicroUSD) > OpencodeMinimumRemainingMicroUSD &&
		remainingOpencode(usage.WeekUsed, OpencodeWeekLimitMicroUSD) > OpencodeMinimumRemainingMicroUSD &&
		remainingOpencode(usage.MonthUsed, OpencodeMonthLimitMicroUSD) > OpencodeMinimumRemainingMicroUSD &&
		remainingOpencode(pool.Used, OpencodePoolLimitMicroUSD) > OpencodeMinimumRemainingMicroUSD, nil
}

func GetCodeProviderConcurrencyUsage(channelType, channelID int, now int64) (int, int, error) {
	if channelID <= 0 {
		return 0, 0, errors.New("invalid code provider channel id")
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	switch channelType {
	case constant.ChannelTypeOpencode:
		usage, err := getOrCreateOpencodeChannelUsage(channelID, now)
		if err != nil {
			return 0, 0, err
		}
		resetOpencodeChannelWindows(usage, now)
		return normalizeOpencodeConcurrency(usage.ConcurrencyCount), operation_setting.GetOpencodeConcurrencyLimit(), nil
	case constant.ChannelTypeCommand:
		usage, err := getOrCreateCommandChannelUsage(channelID, now)
		if err != nil {
			return 0, 0, err
		}
		resetCommandChannelWindows(usage, now)
		return normalizeCommandConcurrency(usage.ConcurrencyCount), operation_setting.GetOpencodeConcurrencyLimit(), nil
	default:
		return 0, 0, fmt.Errorf("unsupported code provider channel type: %d", channelType)
	}
}

func GetCodeProviderRPMUsage(channelType, channelID int, now int64) (int, int, error) {
	return GetCodeProviderConcurrencyUsage(channelType, channelID, now)
}

func CodeProviderChannelHasGeneralCapacity(channel *Channel, modelName string, now int64, reservedConcurrency int) (bool, error) {
	if channel == nil {
		return false, errors.New("code provider channel is missing")
	}
	quotaAvailable, err := CodeProviderChannelHasCapacity(channel, modelName, now, false)
	if err != nil || !quotaAvailable {
		return quotaAvailable, err
	}
	usedConcurrency, concurrencyLimit, err := GetCodeProviderConcurrencyUsage(channel.Type, channel.Id, now)
	if err != nil {
		return false, err
	}
	if concurrencyLimit <= 0 {
		return true, nil
	}
	if reservedConcurrency < 0 {
		reservedConcurrency = 0
	}
	if reservedConcurrency > concurrencyLimit {
		reservedConcurrency = concurrencyLimit
	}
	return usedConcurrency < concurrencyLimit-reservedConcurrency, nil
}

func CalculateOpencodeCostMicroUSD(usage *dto.Usage, now time.Time) (int64, error) {
	return CalculateOpencodeCostMicroUSDForModel(usage, OpencodeModel, now)
}

// CalculateOpencodeCostMicroUSDForModel estimates the OpenCode Go provider charge
// for one request. All OpenCode Go models use flat rates (no peak/off-peak split).
func CalculateOpencodeCostMicroUSDForModel(usage *dto.Usage, modelName string, _ time.Time) (int64, error) {
	if usage == nil {
		return 0, errors.New("opencode usage is missing")
	}
	prompt := int64(maxNonNegative(usage.PromptTokens))
	completion := int64(maxNonNegative(usage.CompletionTokens))
	cachedTokens := usage.PromptCacheHitTokens
	if cachedTokens <= 0 {
		cachedTokens = usage.PromptTokensDetails.CachedTokens
	}
	cached := int64(maxNonNegative(cachedTokens))
	if cached > prompt {
		cached = prompt
	}
	uncached := prompt - cached

	var inputPrice, outputPrice, cachePrice decimal.Decimal
	switch strings.TrimSpace(modelName) {
	case OpencodeGlmModel:
		// Official OpenCode Go flat pricing for GLM-5.3-Flash.
		inputPrice = decimal.RequireFromString("0.15")
		outputPrice = decimal.RequireFromString("0.50")
		cachePrice = decimal.RequireFromString("0.03")
	default:
		// DeepSeek V4 Flash flat rates (former off-peak estimates).
		inputPrice = decimal.RequireFromString("0.22")
		outputPrice = decimal.RequireFromString("0.66")
		cachePrice = decimal.RequireFromString("0.007")
	}
	// Prices are dollars per million tokens. Converting the resulting dollars to micro-dollars cancels the million-token denominator.
	costMicroUSD := decimal.NewFromInt(uncached).Mul(inputPrice).
		Add(decimal.NewFromInt(completion).Mul(outputPrice)).
		Add(decimal.NewFromInt(cached).Mul(cachePrice))
	if costMicroUSD.IsNegative() || costMicroUSD.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return math.MaxInt64, errors.New("opencode usage cost exceeded accounting bounds")
	}
	return costMicroUSD.Round(0).IntPart(), nil
}

func SettleOpencodeUsage(channelID int, usage *dto.Usage, now time.Time) error {
	return SettleOpencodeUsageForModel(channelID, OpencodeModel, usage, now)
}

func SettleOpencodeUsageForModel(channelID int, modelName string, usage *dto.Usage, now time.Time) error {
	if channelID <= 0 || usage == nil {
		return errors.New("invalid opencode settlement input")
	}
	cost, err := CalculateOpencodeCostMicroUSDForModel(usage, modelName, now)
	if err != nil {
		return err
	}
	return SettleOpencodeCost(channelID, cost, now)
}

// SettleOpencodeCost applies one already-calculated provider charge to the
// channel windows and the combined pool.
func SettleOpencodeCost(channelID int, costMicroUSD int64, now time.Time) error {
	if channelID <= 0 || costMicroUSD < 0 {
		return errors.New("invalid opencode settlement input")
	}
	if costMicroUSD <= 0 {
		return nil
	}
	ts := now.Unix()
	return DB.Transaction(func(tx *gorm.DB) error {
		channelUsage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, ts)
		if err != nil {
			return err
		}
		poolUsage, err := getOrCreateOpencodePoolUsageTx(tx, opencodePoolKey(channelID), ts)
		if err != nil {
			return err
		}
		resetOpencodeChannelWindows(channelUsage, ts)
		channelUsage.FiveHourUsed = addCapped(channelUsage.FiveHourUsed, costMicroUSD, OpencodeFiveHourLimitMicroUSD)
		channelUsage.WeekUsed = addCapped(channelUsage.WeekUsed, costMicroUSD, OpencodeWeekLimitMicroUSD)
		channelUsage.MonthUsed = addCapped(channelUsage.MonthUsed, costMicroUSD, OpencodeMonthLimitMicroUSD)
		poolUsage.Used = addCapped(poolUsage.Used, costMicroUSD, OpencodePoolLimitMicroUSD)
		if err := tx.Save(channelUsage).Error; err != nil {
			return err
		}
		return tx.Save(poolUsage).Error
	})
}

// SetOpencodeUsageOverride replaces the usage counters for one OpenCode
// channel. It intentionally leaves RPM/concurrency state untouched.
func SetOpencodeUsageOverride(channelID int, override CodeProviderUsageOverride, now int64) error {
	if channelID <= 0 {
		return errors.New("invalid opencode channel id")
	}
	if err := validateCodeProviderUsageOverride(override, OpencodeFiveHourLimitMicroUSD, OpencodeWeekLimitMicroUSD, OpencodeMonthLimitMicroUSD); err != nil {
		return err
	}
	poolUsed := override.PoolUsedMicroUSD
	// Backward-compatible: accept legacy peak+off-peak override payloads by summing them.
	if override.PeakPoolUsedMicroUSD != nil || override.OffPeakPoolUsedMicroUSD != nil {
		var peak, offPeak int64
		if override.PeakPoolUsedMicroUSD != nil {
			peak = *override.PeakPoolUsedMicroUSD
		}
		if override.OffPeakPoolUsedMicroUSD != nil {
			offPeak = *override.OffPeakPoolUsedMicroUSD
		}
		if peak < 0 || offPeak < 0 {
			return errors.New("opencode pool usage exceeds the limit")
		}
		poolUsed = peak + offPeak
	}
	if poolUsed < 0 || poolUsed > OpencodePoolLimitMicroUSD {
		return errors.New("opencode pool usage exceeds the limit")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		pool, err := getOrCreateOpencodePoolUsageTx(tx, opencodePoolKey(channelID), now)
		if err != nil {
			return err
		}
		usage.FiveHourUsed, usage.WeekUsed, usage.MonthUsed = override.FiveHourUsedMicroUSD, override.WeekUsedMicroUSD, override.MonthUsedMicroUSD
		usage.FiveHourStart, usage.WeekStart, usage.MonthStart = now, now, now
		usage.LastCalibrationError = ""
		usage.NextCalibrationAt = nextOpencodeUsageCalibrationAt(channelID, now)
		pool.Used = poolUsed
		pool.ResetTime = now

		if err := tx.Save(usage).Error; err != nil {
			return err
		}
		return tx.Save(pool).Error
	})
}

func validateCodeProviderUsageOverride(override CodeProviderUsageOverride, fiveHourLimit, weekLimit, monthLimit int64) error {
	if override.FiveHourUsedMicroUSD < 0 || override.FiveHourUsedMicroUSD > fiveHourLimit {
		return errors.New("5-hour usage exceeds the limit")
	}
	if override.WeekUsedMicroUSD < 0 || override.WeekUsedMicroUSD > weekLimit {
		return errors.New("weekly usage exceeds the limit")
	}
	if override.MonthUsedMicroUSD < 0 || override.MonthUsedMicroUSD > monthLimit {
		return errors.New("monthly usage exceeds the limit")
	}
	return nil
}

func GetOpencodeUsageSnapshot(channelID int, now int64) (*OpencodeUsageSnapshot, error) {
	if channelID <= 0 {
		return nil, errors.New("invalid channel id")
	}
	channelUsage, err := getOrCreateOpencodeChannelUsage(channelID, now)
	if err != nil {
		return nil, err
	}
	poolUsage, err := getOrCreateOpencodePoolUsage(channelID, now)
	if err != nil {
		return nil, err
	}
	resetOpencodeChannelWindows(channelUsage, now)
	if channelUsage.NextCalibrationAt == 0 {
		channelUsage.NextCalibrationAt = nextOpencodeUsageCalibrationAt(channelID, now)
		if err := DB.Save(channelUsage).Error; err != nil {
			return nil, err
		}
	}
	emptyPool := makeOpencodeWindow(0, 0, 0)
	return &OpencodeUsageSnapshot{
		ChannelID:        channelID,
		FiveHour:         makeOpencodeWindow(channelUsage.FiveHourUsed, OpencodeFiveHourLimitMicroUSD, channelUsage.FiveHourStart+5*60*60),
		Week:             makeOpencodeWindow(channelUsage.WeekUsed, OpencodeWeekLimitMicroUSD, channelUsage.WeekStart+7*24*60*60),
		Month:            makeOpencodeWindow(channelUsage.MonthUsed, OpencodeMonthLimitMicroUSD, channelUsage.MonthStart+30*24*60*60),
		Pool:             makeOpencodeWindow(poolUsage.Used, OpencodePoolLimitMicroUSD, poolUsage.ResetTime),
		PeakPool:         emptyPool,
		OffPeakPool:      emptyPool,
		PricingPeriod:    "",
		RPMCount:         normalizeOpencodeRPM(channelUsage.RPMCount),
		RPMLimit:         operation_setting.GetOpencodeConcurrencyLimit(),
		RPMResetAt:       channelUsage.RPMWindowStart + 60,
		ConcurrencyCount: normalizeOpencodeConcurrency(channelUsage.ConcurrencyCount),
		ConcurrencyLimit: operation_setting.GetOpencodeConcurrencyLimit(),
		Calibration: OpencodeCalibrationSnapshot{
			LastAttemptAt: channelUsage.LastCalibrationAttemptAt,
			LastSuccessAt: channelUsage.LastCalibratedAt,
			NextAt:        channelUsage.NextCalibrationAt,
			LastError:     channelUsage.LastCalibrationError,
		},
	}, nil
}

func ResetAllOpencodeUsage() error {
	now := time.Now().Unix()
	return DB.Transaction(func(tx *gorm.DB) error {
		var channelUsages []OpencodeChannelUsage
		if err := lockForUpdate(tx).Find(&channelUsages).Error; err != nil {
			return err
		}
		for index := range channelUsages {
			usage := &channelUsages[index]
			usage.FiveHourUsed = 0
			usage.FiveHourStart = now
			usage.WeekUsed = 0
			usage.WeekStart = now
			usage.MonthUsed = 0
			usage.MonthStart = now
			usage.RPMCount = 0
			usage.RPMWindowStart = now
			usage.ConcurrencyCount = 0
			if err := tx.Save(usage).Error; err != nil {
				return err
			}
		}

		var pools []OpencodePoolUsage
		if err := lockForUpdate(tx).Find(&pools).Error; err != nil {
			return err
		}
		for index := range pools {
			pool := &pools[index]
			pool.Used = 0
			pool.ResetTime = now
			if err := tx.Save(pool).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func ResetOpencodePoolUsage() error {
	return ResetAllOpencodeUsage()
}

// EnsureOpencodePoolUsageRows creates the combined pool row during startup so
// concurrent settlements can acquire an existing row lock on every database.
// Legacy peak/off-peak rows are migrated once into the combined pool.
func EnsureOpencodePoolUsageRows() error {
	now := time.Now().Unix()
	var channels []Channel
	if err := DB.Where("type = ?", constant.ChannelTypeOpencode).Find(&channels).Error; err != nil {
		return err
	}
	for _, channel := range channels {
		poolKey := opencodePoolKey(channel.Id)
		var usage OpencodePoolUsage
		err := DB.Where("pool_key = ?", poolKey).First(&usage).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			migratedUsed, migrateErr := migrateLegacyOpencodePoolUsed(channel.Id)
			if migrateErr != nil {
				return migrateErr
			}
			if err := DB.Create(&OpencodePoolUsage{
				PoolKey:   poolKey,
				Used:      minMaxOpencode(migratedUsed, 0, OpencodePoolLimitMicroUSD),
				ResetTime: now,
			}).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	return ResetCodeProviderConcurrencyOnStartup()
}

func migrateLegacyOpencodePoolUsed(channelID int) (int64, error) {
	var total int64
	for _, period := range []string{OpencodePricingPeak, OpencodePricingOffPeak} {
		var legacy OpencodePoolUsage
		err := DB.Where("pool_key = ?", opencodeLegacyPoolKeyForPeriod(channelID, period)).First(&legacy).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if legacy.Used > 0 {
			total += legacy.Used
		}
	}
	return total, nil
}

// ResetCodeProviderConcurrencyOnStartup clears in-memory concurrency leases that
// cannot survive a process restart. Leaving stale counters would permanently
// shrink available capacity until an admin reset.
func ResetCodeProviderConcurrencyOnStartup() error {
	if DB.Migrator().HasTable(&OpencodeChannelUsage{}) {
		if err := DB.Model(&OpencodeChannelUsage{}).Where("concurrency_count <> 0").Update("concurrency_count", 0).Error; err != nil {
			return err
		}
	}
	if DB.Migrator().HasTable(&CommandChannelUsage{}) {
		if err := DB.Model(&CommandChannelUsage{}).Where("concurrency_count <> 0").Update("concurrency_count", 0).Error; err != nil {
			return err
		}
	}
	return nil
}

func getOrCreateOpencodeChannelUsage(channelID int, now int64) (*OpencodeChannelUsage, error) {
	var usage OpencodeChannelUsage
	err := DB.Where("channel_id = ?", channelID).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = OpencodeChannelUsage{ChannelID: channelID, FiveHourStart: now, WeekStart: now, MonthStart: now, RPMWindowStart: now}
		err = DB.Create(&usage).Error
	}
	return &usage, err
}

func getOrCreateOpencodeChannelUsageTx(tx *gorm.DB, channelID int, now int64) (*OpencodeChannelUsage, error) {
	var usage OpencodeChannelUsage
	err := lockForUpdate(tx).Where("channel_id = ?", channelID).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = OpencodeChannelUsage{ChannelID: channelID, FiveHourStart: now, WeekStart: now, MonthStart: now, RPMWindowStart: now}
		err = tx.Create(&usage).Error
	}
	return &usage, err
}

func getOrCreateOpencodePoolUsage(channelID int, now int64) (*OpencodePoolUsage, error) {
	return getOrCreateOpencodePoolUsageByKey(opencodePoolKey(channelID), now)
}

func getOrCreateOpencodePoolUsageByKey(poolKey string, now int64) (*OpencodePoolUsage, error) {
	var usage OpencodePoolUsage
	err := DB.Where("pool_key = ?", poolKey).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = OpencodePoolUsage{PoolKey: poolKey, ResetTime: now}
		err = DB.Create(&usage).Error
	}
	return &usage, err
}

func getOrCreateOpencodePoolUsageTx(tx *gorm.DB, poolKey string, now int64) (*OpencodePoolUsage, error) {
	var usage OpencodePoolUsage
	err := lockForUpdate(tx).Where("pool_key = ?", poolKey).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = OpencodePoolUsage{PoolKey: poolKey, ResetTime: now}
		err = tx.Create(&usage).Error
	}
	return &usage, err
}

func resetOpencodeChannelWindows(usage *OpencodeChannelUsage, now int64) {
	if now-usage.FiveHourStart >= 5*60*60 {
		usage.FiveHourUsed, usage.FiveHourStart = 0, now
	}
	if now-usage.WeekStart >= 7*24*60*60 {
		usage.WeekUsed, usage.WeekStart = 0, now
	}
	if now-usage.MonthStart >= 30*24*60*60 {
		usage.MonthUsed, usage.MonthStart = 0, now
	}
	if now-usage.RPMWindowStart >= 60 {
		usage.RPMCount, usage.RPMWindowStart = 0, now
	}
}

func remainingOpencode(used, limit int64) int64 {
	if used <= 0 {
		return limit
	}
	if used >= limit {
		return 0
	}
	return limit - used
}

func addCapped(current, amount, limit int64) int64 {
	current = minMaxOpencode(current, 0, limit)
	if amount <= 0 {
		return current
	}
	if current >= limit || amount >= limit-current {
		return limit
	}
	return current + amount
}

func makeOpencodeWindow(used, limit, resetAt int64) OpencodeUsageWindow {
	if limit <= 0 {
		return OpencodeUsageWindow{
			UsedMicroUSD: 0, LimitMicroUSD: 0, RemainingMicroUSD: 0,
			UsedUSD: 0, LimitUSD: 0, RemainingUSD: 0, Percent: 0, ResetAt: resetAt,
		}
	}
	used = minMaxOpencode(used, 0, limit)
	remaining := remainingOpencode(used, limit)
	percent := float64(used) * 100 / float64(limit)
	if percent > 100 {
		percent = 100
	}
	return OpencodeUsageWindow{
		UsedMicroUSD: used, LimitMicroUSD: limit, RemainingMicroUSD: remaining,
		UsedUSD: float64(used) / 1_000_000, LimitUSD: float64(limit) / 1_000_000,
		RemainingUSD: float64(remaining) / 1_000_000, Percent: percent, ResetAt: resetAt,
	}
}

func maxNonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func ReserveOpencodeRPM(channelID int, now int64) (bool, int64, error) {
	if channelID <= 0 {
		return false, 0, errors.New("invalid channel id")
	}
	var allowed bool
	var resetAt int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		resetOpencodeChannelWindows(usage, now)
		resetAt = usage.RPMWindowStart + 60
		if usage.RPMCount >= operation_setting.GetOpencodeRPMLimit() {
			return nil
		}
		usage.RPMCount++
		allowed = true
		return tx.Save(usage).Error
	})
	return allowed, resetAt, err
}

func ReleaseOpencodeRPM(channelID int, now int64) error {
	if channelID <= 0 {
		return errors.New("invalid channel id")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		wasInWindow := now-usage.RPMWindowStart < 60
		resetOpencodeChannelWindows(usage, now)
		if !wasInWindow || usage.RPMCount <= 0 {
			return nil
		}
		usage.RPMCount--
		return tx.Save(usage).Error
	})
}

func normalizeOpencodeRPM(count int) int {
	return int(minMaxOpencode(int64(count), 0, int64(operation_setting.GetOpencodeRPMLimit())))
}

func normalizeOpencodeConcurrency(count int) int {
	return int(minMaxOpencode(int64(count), 0, int64(operation_setting.GetOpencodeConcurrencyLimit())))
}

func ReserveOpencodeConcurrency(channelID int) (bool, error) {
	if channelID <= 0 {
		return false, errors.New("invalid channel id")
	}
	var allowed bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, common.GetTimestamp())
		if err != nil {
			return err
		}
		if usage.ConcurrencyCount >= operation_setting.GetOpencodeConcurrencyLimit() {
			return nil
		}
		usage.ConcurrencyCount++
		allowed = true
		return tx.Save(usage).Error
	})
	return allowed, err
}

func ReleaseOpencodeConcurrency(channelID int) error {
	if channelID <= 0 {
		return errors.New("invalid channel id")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, common.GetTimestamp())
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

func minMaxOpencode(value, minimum, maximum int64) int64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func GetOpencodeCandidates(group, modelName string, attempted map[int]struct{}) ([]*Channel, error) {
	return getOpencodeCandidates(group, modelName, attempted, true)
}

func GetOpencodeQuotaCandidates(group, modelName string, attempted map[int]struct{}) ([]*Channel, error) {
	return getOpencodeCandidates(group, modelName, attempted, false)
}

func getModelMappingSources(channel *Channel) (map[string]struct{}, error) {
	sources := make(map[string]struct{})
	if channel == nil {
		return sources, nil
	}
	rawMapping := strings.TrimSpace(channel.GetModelMapping())
	if rawMapping == "" {
		return sources, nil
	}
	mapping := make(map[string]string)
	if err := common.UnmarshalJsonStr(rawMapping, &mapping); err != nil {
		return nil, err
	}
	for source := range mapping {
		source = strings.TrimSpace(source)
		if source != "" {
			sources[source] = struct{}{}
		}
	}
	return sources, nil
}

func channelHasConfiguredModel(channel *Channel, modelName string) bool {
	if channel == nil || modelName == "" {
		return false
	}
	for _, configuredModel := range channel.GetModels() {
		if strings.TrimSpace(configuredModel) == modelName {
			return true
		}
	}
	return false
}

func channelSupportsCodeProviderModel(channel *Channel, modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if !channelHasConfiguredModel(channel, modelName) {
		return false
	}
	switch channel.Type {
	case constant.ChannelTypeOpencode:
		if IsOpencodeNativeModel(modelName) {
			return true
		}
	case constant.ChannelTypeCommand:
		if modelName == CommandFlashModel || modelName == CommandProModel {
			return true
		}
	default:
		return false
	}
	mappingSources, err := getModelMappingSources(channel)
	if err != nil {
		return false
	}
	_, ok := mappingSources[modelName]
	return ok
}

// IsCodeProviderModel identifies fixed OpenCode/Command models and model-
// mapping sources on either channel. Mapped requests need the same queue so
// they do not bypass the provider-specific quota checks.
func IsCodeProviderModel(modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if IsOpencodeNativeModel(modelName) || modelName == CommandFlashModel || modelName == CommandProModel {
		return true
	}
	if DB == nil || modelName == "" || !DB.Migrator().HasTable("channels") {
		return false
	}
	var count int64
	err := DB.Table("abilities").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.model = ? AND channels.type IN ? AND channels.status = ?", modelName, []int{constant.ChannelTypeOpencode, constant.ChannelTypeCommand}, common.ChannelStatusEnabled).
		Count(&count).Error
	return err == nil && count > 0
}

func GetCodeProviderCandidates(group, modelName string, attempted map[int]struct{}) ([]*Channel, error) {
	return getCodeProviderCandidates(group, modelName, attempted, true)
}

func GetCodeProviderQuotaCandidates(group, modelName string, attempted map[int]struct{}) ([]*Channel, error) {
	return getCodeProviderCandidates(group, modelName, attempted, false)
}

func getCodeProviderCandidates(group, modelName string, attempted map[int]struct{}, checkRPM bool) ([]*Channel, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, nil
	}
	var channels []*Channel
	query := DB.Table("channels").
		Select("channels.*").
		Joins("JOIN abilities ON abilities.channel_id = channels.id").
		Where("channels.type IN ? AND channels.status = ?", []int{constant.ChannelTypeOpencode, constant.ChannelTypeCommand}, common.ChannelStatusEnabled).
		Where("abilities."+commonGroupCol+" = ? AND abilities.model = ? AND abilities.enabled = ?", group, modelName, true)
	if err := query.Find(&channels).Error; err != nil {
		return nil, err
	}
	available := make([]*Channel, 0, len(channels))
	now := time.Now().Unix()
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		if _, ok := attempted[channel.Id]; ok {
			continue
		}
		if !channelSupportsCodeProviderModel(channel, modelName) {
			continue
		}
		kicked, kickedErr := IsSpeedProtectionChannelKicked(channel, group)
		if kickedErr != nil {
			return nil, kickedErr
		}
		if kicked {
			continue
		}
		var ok bool
		var err error
		switch channel.Type {
		case constant.ChannelTypeOpencode:
			if checkRPM {
				ok, err = OpencodeChannelHasCapacity(channel.Id, now)
			} else {
				ok, err = OpencodeChannelHasQuotaCapacity(channel.Id, now)
			}
		case constant.ChannelTypeCommand:
			if checkRPM {
				ok, err = CommandChannelHasCapacityForModel(channel, modelName, now)
			} else {
				ok, err = CommandChannelHasQuotaCapacityForModel(channel, modelName, now)
			}
		}
		if err != nil {
			return nil, err
		}
		if ok {
			available = append(available, channel)
		} else if quotaErr := ReconcileCodeProviderQuotaChannel(channel.Id, now); quotaErr != nil {
			return nil, quotaErr
		}
	}
	return available, nil
}

// GetCodeProviderCapacitySnapshot backs the shared request queue. A split
// Command channel contributes capacity when either of its model pools is open.
func GetCodeProviderCapacitySnapshot(now int64) (*OpencodeCapacitySnapshot, error) {
	return getCodeProviderCapacitySnapshot("", "", now)
}

// GetCodeProviderCapacitySnapshotForGroupModel returns queue capacity for one
// group/model key. Speed-protection kicked channels are excluded only for the
// group where they were kicked; the same channel can still contribute to a
// different queue key.
func GetCodeProviderCapacitySnapshotForGroupModel(group, modelName string, now int64) (*OpencodeCapacitySnapshot, error) {
	return getCodeProviderCapacitySnapshot(strings.TrimSpace(group), strings.TrimSpace(modelName), now)
}

func getCodeProviderCapacitySnapshot(group, modelName string, now int64) (*OpencodeCapacitySnapshot, error) {
	var channels []*Channel
	query := DB.Where("type IN ? AND status = ?", []int{constant.ChannelTypeOpencode, constant.ChannelTypeCommand}, common.ChannelStatusEnabled)
	if group != "" && modelName != "" {
		query = query.
			Joins("JOIN abilities ON abilities.channel_id = channels.id").
			Where("abilities."+commonGroupCol+" = ? AND abilities.model = ? AND abilities.enabled = ?", group, modelName, true)
	}
	if err := query.Find(&channels).Error; err != nil {
		return nil, err
	}

	limit := operation_setting.GetOpencodeConcurrencyLimit()
	snapshot := &OpencodeCapacitySnapshot{}
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		if group != "" && modelName != "" {
			if !channelSupportsCodeProviderModel(channel, modelName) {
				continue
			}
			kicked, kickedErr := IsSpeedProtectionChannelKicked(channel, group)
			if kickedErr != nil {
				return nil, kickedErr
			}
			if kicked {
				continue
			}
		}
		snapshot.TotalCapacity += limit
		var quotaAvailable bool
		var err error
		var rpmCount int
		switch channel.Type {
		case constant.ChannelTypeOpencode:
			quotaAvailable, err = OpencodeChannelHasQuotaCapacity(channel.Id, now)
			if err != nil {
				return nil, err
			}
			usage, usageErr := getOrCreateOpencodeChannelUsage(channel.Id, now)
			if usageErr != nil {
				return nil, usageErr
			}
			resetOpencodeChannelWindows(usage, now)
			rpmCount = normalizeOpencodeConcurrency(usage.ConcurrencyCount)
		case constant.ChannelTypeCommand:
			if group != "" && modelName != "" {
				quotaAvailable, err = CommandChannelHasQuotaCapacityForModel(channel, modelName, now)
			} else {
				quotaAvailable, err = CommandChannelHasAnyQuotaCapacity(channel, now)
			}
			if err != nil {
				return nil, err
			}
			usage, usageErr := getOrCreateCommandChannelUsage(channel.Id, now)
			if usageErr != nil {
				return nil, usageErr
			}
			resetCommandChannelWindows(usage, now)
			rpmCount = normalizeCommandConcurrency(usage.ConcurrencyCount)
		}
		if !quotaAvailable {
			if quotaErr := ReconcileCodeProviderQuotaChannel(channel.Id, now); quotaErr != nil {
				return nil, quotaErr
			}
			continue
		}
		available := limit - rpmCount
		if available > 0 {
			snapshot.AvailableCapacity += available
		}
	}
	return snapshot, nil
}

func ReserveCodeProviderConcurrency(channelType, channelID int) (bool, error) {
	switch channelType {
	case constant.ChannelTypeOpencode:
		return ReserveOpencodeConcurrency(channelID)
	case constant.ChannelTypeCommand:
		return ReserveCommandConcurrency(channelID)
	default:
		return false, fmt.Errorf("unsupported code provider channel type: %d", channelType)
	}
}

func ReleaseCodeProviderConcurrency(channelType, channelID int) error {
	switch channelType {
	case constant.ChannelTypeOpencode:
		return ReleaseOpencodeConcurrency(channelID)
	case constant.ChannelTypeCommand:
		return ReleaseCommandConcurrency(channelID)
	default:
		return fmt.Errorf("unsupported code provider channel type: %d", channelType)
	}
}

func ReserveCodeProviderRPM(channelType, channelID int, now int64) (bool, int64, error) {
	switch channelType {
	case constant.ChannelTypeOpencode:
		return ReserveOpencodeRPM(channelID, now)
	case constant.ChannelTypeCommand:
		return ReserveCommandRPM(channelID, now)
	default:
		return false, 0, fmt.Errorf("unsupported code provider channel type: %d", channelType)
	}
}

func ReleaseCodeProviderRPM(channelType, channelID int, now int64) error {
	switch channelType {
	case constant.ChannelTypeOpencode:
		return ReleaseOpencodeRPM(channelID, now)
	case constant.ChannelTypeCommand:
		return ReleaseCommandRPM(channelID, now)
	default:
		return fmt.Errorf("unsupported code provider channel type: %d", channelType)
	}
}

func getOpencodeCandidates(group, modelName string, attempted map[int]struct{}, checkRPM bool) ([]*Channel, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, nil
	}
	var channels []*Channel
	query := DB.Table("channels").
		Select("channels.*").
		Joins("JOIN abilities ON abilities.channel_id = channels.id").
		Where("channels.type = ? AND channels.status = ?", constant.ChannelTypeOpencode, common.ChannelStatusEnabled).
		Where("abilities."+commonGroupCol+" = ? AND abilities.model = ? AND abilities.enabled = ?", group, modelName, true)
	if err := query.Find(&channels).Error; err != nil {
		return nil, err
	}
	available := make([]*Channel, 0, len(channels))
	now := time.Now().Unix()
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		if _, ok := attempted[channel.Id]; ok {
			continue
		}
		if !channelSupportsCodeProviderModel(channel, modelName) {
			continue
		}
		ok, err := OpencodeChannelHasCapacity(channel.Id, now)
		if !checkRPM {
			ok, err = OpencodeChannelHasQuotaCapacity(channel.Id, now)
		}
		if err != nil {
			return nil, err
		}
		if ok {
			available = append(available, channel)
		} else if quotaErr := ReconcileCodeProviderQuotaChannel(channel.Id, now); quotaErr != nil {
			return nil, quotaErr
		}
	}
	return available, nil
}
func ValidateOpencodeChannel(channel *Channel) error {
	if channel == nil || channel.Type != constant.ChannelTypeOpencode {
		return fmt.Errorf("not an opencode channel")
	}
	if strings.TrimSpace(channel.Key) == "" {
		return fmt.Errorf("invalid opencode channel configuration")
	}
	mappingSources, err := getModelMappingSources(channel)
	if err != nil {
		return fmt.Errorf("invalid opencode model mapping")
	}
	hasNativeModel := false
	for _, configuredModel := range channel.GetModels() {
		configuredModel = strings.TrimSpace(configuredModel)
		if configuredModel == "" {
			continue
		}
		if IsOpencodeNativeModel(configuredModel) {
			hasNativeModel = true
			continue
		}
		if _, ok := mappingSources[configuredModel]; !ok {
			return fmt.Errorf("opencode channel only supports deepseek-v4-flash, glm-5.3-flash, or model mapping source models")
		}
	}
	if !hasNativeModel {
		return fmt.Errorf("opencode channel must include deepseek-v4-flash or glm-5.3-flash")
	}
	return nil
}
