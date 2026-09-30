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
	CommandFlashModel                      = "deepseek/deepseek-v4-flash"
	CommandProModel                        = "deepseek/deepseek-v4-pro"
	CommandV41FlashModel                   = "deepseek/deepseek-v4.1-flash"
	CommandModel                           = CommandFlashModel
	CommandFiveHourLimitMicroUSD     int64 = 14_000_000
	CommandWeekLimitMicroUSD         int64 = 35_000_000
	CommandMonthLimitMicroUSD        int64 = 70_000_000
	CommandCombinedPoolLimitMicroUSD int64 = 60_000_000
	CommandFlashPoolLimitMicroUSD    int64 = 50_000_000
	CommandProPoolLimitMicroUSD      int64 = 20_000_000
	// CommandV41PoolLimitMicroUSD 是 v4.1-flash 专用池（4.1 池）额度。
	CommandV41PoolLimitMicroUSD int64 = 40_000_000
	// CommandPoolLimitMicroUSD is the legacy single-model pool limit.
	CommandPoolLimitMicroUSD        = CommandCombinedPoolLimitMicroUSD
	CommandMinimumRemainingMicroUSD = 200_000
	CommandPoolKeyPrefix            = "command"
	CommandFlashPoolKeySuffix       = "flash"
	CommandProPoolKeySuffix         = "pro"
	CommandV41PoolKeySuffix         = "v41"
)

// commandModelPricing 是一个 Command 上游模型在某计费时段的
// 输入/输出/缓存命中单价（USD / 百万 token）。
type commandModelPricing struct {
	input  string
	output string
	cache  string
}

var commandModelPricingTable = map[string]struct {
	offPeak commandModelPricing
	peak    commandModelPricing
}{
	CommandFlashModel: {
		offPeak: commandModelPricing{input: "0.22", output: "0.66", cache: "0.007"},
		peak:    commandModelPricing{input: "0.44", output: "1.32", cache: "0.014"},
	},
	CommandV41FlashModel: {
		offPeak: commandModelPricing{input: "0.15", output: "0.60", cache: "0.003"},
		peak:    commandModelPricing{input: "0.30", output: "1.20", cache: "0.006"},
	},
	CommandProModel: {
		offPeak: commandModelPricing{input: "0.66", output: "1.98", cache: "0.022"},
		peak:    commandModelPricing{input: "1.32", output: "3.96", cache: "0.044"},
	},
}

type CommandChannelUsage struct {
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
	// QuotaDisabledUntil identifies an automatic disable caused by the rolling
	// quota windows. A zero value means this channel is not quota-disabled.
	QuotaDisabledUntil int64 `json:"quota_disabled_until" gorm:"index"`
	// 校准（commandcode.ai 额度查询）登记：下次到期时间、最近尝试与错误。
	LastCalibrationAttemptAt int64  `json:"last_calibration_attempt_at"`
	NextCalibrationAt        int64  `json:"next_calibration_at"`
	CalibrationLastError     string `json:"calibration_last_error"`
}

type CommandPoolUsage struct {
	ID        int    `json:"id"`
	PoolKey   string `json:"pool_key" gorm:"uniqueIndex;size:128"`
	Used      int64  `json:"used"`
	ResetTime int64  `json:"reset_time"`
}

type CommandUsageSnapshot struct {
	ChannelID  int                 `json:"channel_id"`
	FiveHour   OpencodeUsageWindow `json:"five_hour"`
	Week       OpencodeUsageWindow `json:"week"`
	Month      OpencodeUsageWindow `json:"month"`
	Pool       OpencodeUsageWindow `json:"pool"`
	FlashPool  OpencodeUsageWindow `json:"flash_pool"`
	V41Pool    OpencodeUsageWindow `json:"v41_pool"`
	ProPool    OpencodeUsageWindow `json:"pro_pool"`
	SplitPools bool                `json:"split_pools"`
	// *_pool_active 标记该模型池是否随当前渠道模型列表启用，
	// 前端只展示启用的池。
	FlashPoolActive  bool   `json:"flash_pool_active"`
	V41PoolActive    bool   `json:"v41_pool_active"`
	ProPoolActive    bool   `json:"pro_pool_active"`
	PricingPeriod    string `json:"pricing_period"`
	RPMCount         int    `json:"rpm_count"`
	RPMLimit         int    `json:"rpm_limit"`
	RPMResetAt       int64  `json:"rpm_reset_at"`
	ConcurrencyCount int    `json:"concurrency_count"`
	ConcurrencyLimit int    `json:"concurrency_limit"`
}

func CommandPoolKey(channelID int) string {
	return fmt.Sprintf("%s:%d", CommandPoolKeyPrefix, channelID)
}

func CommandFlashPoolKey(channelID int) string {
	return fmt.Sprintf("%s:%d:%s", CommandPoolKeyPrefix, channelID, CommandFlashPoolKeySuffix)
}

func CommandProPoolKey(channelID int) string {
	return fmt.Sprintf("%s:%d:%s", CommandPoolKeyPrefix, channelID, CommandProPoolKeySuffix)
}

func CommandV41PoolKey(channelID int) string {
	return fmt.Sprintf("%s:%d:%s", CommandPoolKeyPrefix, channelID, CommandV41PoolKeySuffix)
}

// commandChannelServesModel 报告渠道是否以该上游模型计费：模型在模型列表中，
// 或某个模型映射的解析目标命中该模型。
func commandChannelServesModel(channel *Channel, targetModel string) bool {
	if channel == nil {
		return false
	}
	for _, modelName := range channel.GetModels() {
		if strings.TrimSpace(modelName) == targetModel {
			return true
		}
	}
	mapping := make(map[string]string)
	if raw := strings.TrimSpace(channel.GetModelMapping()); raw != "" {
		if common.UnmarshalJsonStr(raw, &mapping) == nil {
			for sourceModel := range mapping {
				if ResolveCommandModel(channel, sourceModel) == targetModel {
					return true
				}
			}
		}
	}
	return false
}

// commandChannelUsesSplitPools 报告渠道是否启用模型分池：渠道可以服务
// v4-pro 或 v4.1-flash（可独立启用，也可与 v4-flash 组合）时，按模型拆分额度池。
func commandChannelUsesSplitPools(channel *Channel) bool {
	if channel == nil || channel.Type != constant.ChannelTypeCommand {
		return false
	}
	return commandChannelServesModel(channel, CommandProModel) ||
		commandChannelServesModel(channel, CommandV41FlashModel)
}

// commandChannelPoolStats 描述一个已启用的模型池。
type commandChannelPoolStats struct {
	active    bool
	key       string
	limit     int64
	used      int64
	resetTime int64
}

func CommandChannelUsesSplitPools(channelID int) (bool, error) {
	channel, found, err := getCommandChannel(channelID)
	if err != nil {
		return false, err
	}
	return found && commandChannelUsesSplitPools(channel), nil
}

// ResolveCommandModel follows a channel's model mapping to determine which
// upstream model is billed. Unknown mapped targets intentionally fall back to
// Flash pricing because each Command model has its own dedicated pool only
// when the channel serves several Command models.
func ResolveCommandModel(channel *Channel, modelName string) string {
	currentModel := strings.TrimSpace(modelName)
	if currentModel == "" {
		return CommandFlashModel
	}
	mapping := make(map[string]string)
	if channel != nil {
		if raw := strings.TrimSpace(channel.GetModelMapping()); raw != "" {
			_ = common.UnmarshalJsonStr(raw, &mapping)
		}
	}
	visited := map[string]struct{}{currentModel: {}}
	for len(mapping) > 0 {
		nextModel, ok := mapping[currentModel]
		nextModel = strings.TrimSpace(nextModel)
		if !ok || nextModel == "" {
			break
		}
		if _, seen := visited[nextModel]; seen {
			break
		}
		visited[nextModel] = struct{}{}
		currentModel = nextModel
	}
	return currentModel
}

// commandPoolForModel 返回某请求应计入的模型池及其限额：
// v4.1-flash → 4.1 池（$40）；v4-pro → 4p 池（$20）；
// v4-flash 在分池渠道计入 flash 池（$50），单模型渠道走合并总池（$60）。
// channel 必须非 nil（调用方保证）。
func commandPoolForModel(channel *Channel, modelName string) (string, int64) {
	resolved := ResolveCommandModel(channel, modelName)
	if commandChannelUsesSplitPools(channel) {
		switch resolved {
		case CommandV41FlashModel:
			return CommandV41PoolKey(channel.Id), CommandV41PoolLimitMicroUSD
		case CommandProModel:
			return CommandProPoolKey(channel.Id), CommandProPoolLimitMicroUSD
		}
		return CommandFlashPoolKey(channel.Id), CommandFlashPoolLimitMicroUSD
	}
	switch resolved {
	case CommandV41FlashModel:
		return CommandV41PoolKey(channel.Id), CommandV41PoolLimitMicroUSD
	case CommandProModel:
		return CommandProPoolKey(channel.Id), CommandProPoolLimitMicroUSD
	}
	return CommandPoolKey(channel.Id), CommandCombinedPoolLimitMicroUSD
}

func CommandChannelHasCapacity(channelID int, now int64) (bool, error) {
	return CommandChannelHasCapacityForModelID(channelID, CommandFlashModel, now)
}

func CommandChannelHasQuotaCapacity(channelID int, now int64) (bool, error) {
	return CommandChannelHasQuotaCapacityForModelID(channelID, CommandFlashModel, now)
}

func CommandChannelHasCapacityForModelID(channelID int, modelName string, now int64) (bool, error) {
	channel, found, err := getCommandChannel(channelID)
	if err != nil {
		return false, err
	}
	if !found {
		return commandChannelHasCapacity(nil, channelID, modelName, now)
	}
	return commandChannelHasCapacity(channel, channelID, modelName, now)
}

func CommandChannelHasQuotaCapacityForModelID(channelID int, modelName string, now int64) (bool, error) {
	channel, found, err := getCommandChannel(channelID)
	if err != nil {
		return false, err
	}
	if !found {
		return commandChannelHasQuotaCapacity(nil, channelID, modelName, now)
	}
	return commandChannelHasQuotaCapacity(channel, channelID, modelName, now)
}

func CommandChannelHasCapacityForModel(channel *Channel, modelName string, now int64) (bool, error) {
	if channel == nil {
		return false, errors.New("command channel is missing")
	}
	return commandChannelHasCapacity(channel, channel.Id, modelName, now)
}

func CommandChannelHasQuotaCapacityForModel(channel *Channel, modelName string, now int64) (bool, error) {
	if channel == nil {
		return false, errors.New("command channel is missing")
	}
	return commandChannelHasQuotaCapacity(channel, channel.Id, modelName, now)
}

func commandChannelHasCapacity(channel *Channel, channelID int, modelName string, now int64) (bool, error) {
	if channel != nil {
		if err := ensureCommandPoolUsageRows(channel, now); err != nil {
			return false, err
		}
	}
	usage, err := getOrCreateCommandChannelUsage(channelID, now)
	if err != nil {
		return false, err
	}
	poolKey, poolLimit := CommandPoolKey(channelID), CommandCombinedPoolLimitMicroUSD
	if channel != nil {
		poolKey, poolLimit = commandPoolForModel(channel, modelName)
	}
	pool, err := getOrCreateCommandPoolUsageByKey(poolKey, now)
	if err != nil {
		return false, err
	}
	resetCommandChannelWindows(usage, now)
	return remainingOpencode(usage.FiveHourUsed, CommandFiveHourLimitMicroUSD) > CommandMinimumRemainingMicroUSD &&
		remainingOpencode(usage.WeekUsed, CommandWeekLimitMicroUSD) > CommandMinimumRemainingMicroUSD &&
		remainingOpencode(usage.MonthUsed, CommandMonthLimitMicroUSD) > CommandMinimumRemainingMicroUSD &&
		remainingOpencode(pool.Used, poolLimit) > CommandMinimumRemainingMicroUSD &&
		usage.ConcurrencyCount < operation_setting.GetOpencodeConcurrencyLimit(), nil
}

func commandChannelHasQuotaCapacity(channel *Channel, channelID int, modelName string, now int64) (bool, error) {
	if channel != nil {
		if err := ensureCommandPoolUsageRows(channel, now); err != nil {
			return false, err
		}
	}
	usage, err := getOrCreateCommandChannelUsage(channelID, now)
	if err != nil {
		return false, err
	}
	poolKey, poolLimit := CommandPoolKey(channelID), CommandCombinedPoolLimitMicroUSD
	if channel != nil {
		poolKey, poolLimit = commandPoolForModel(channel, modelName)
	}
	pool, err := getOrCreateCommandPoolUsageByKey(poolKey, now)
	if err != nil {
		return false, err
	}
	resetCommandChannelWindows(usage, now)
	return remainingOpencode(usage.FiveHourUsed, CommandFiveHourLimitMicroUSD) > CommandMinimumRemainingMicroUSD &&
		remainingOpencode(usage.WeekUsed, CommandWeekLimitMicroUSD) > CommandMinimumRemainingMicroUSD &&
		remainingOpencode(usage.MonthUsed, CommandMonthLimitMicroUSD) > CommandMinimumRemainingMicroUSD &&
		remainingOpencode(pool.Used, poolLimit) > CommandMinimumRemainingMicroUSD, nil
}

// CommandChannelHasAnyQuotaCapacity 报告渠道是否还有任一启用模型的额度：
// 任一模型池（flash/4.1/4p）未耗尽即视为有容量，供候选渠道过滤使用。
func CommandChannelHasAnyQuotaCapacity(channel *Channel, now int64) (bool, error) {
	if channel == nil {
		return false, errors.New("command channel is missing")
	}
	for _, target := range []string{CommandFlashModel, CommandV41FlashModel, CommandProModel} {
		if !commandChannelServesModel(channel, target) {
			continue
		}
		available, err := CommandChannelHasQuotaCapacityForModel(channel, target, now)
		if err != nil || available {
			return available, err
		}
	}
	return false, nil
}

func CalculateCommandCostMicroUSD(usage *dto.Usage, now time.Time) (int64, error) {
	return CalculateCommandCostMicroUSDForModel(usage, CommandFlashModel, now)
}

func CalculateCommandCostMicroUSDForModel(usage *dto.Usage, modelName string, now time.Time) (int64, error) {
	if usage == nil {
		return 0, errors.New("command usage is missing")
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
	pricing, ok := commandModelPricingTable[modelName]
	if !ok {
		// 未知模型按 Flash 计价兜底（映射解析结果由上游保证）。
		pricing = commandModelPricingTable[CommandFlashModel]
	}
	rates := pricing.offPeak
	if IsOpencodePeak(now) {
		rates = pricing.peak
	}
	inputPrice := decimal.RequireFromString(rates.input)
	outputPrice := decimal.RequireFromString(rates.output)
	cachePrice := decimal.RequireFromString(rates.cache)
	costMicroUSD := decimal.NewFromInt(uncached).Mul(inputPrice).
		Add(decimal.NewFromInt(completion).Mul(outputPrice)).
		Add(decimal.NewFromInt(cached).Mul(cachePrice))
	if costMicroUSD.IsNegative() || costMicroUSD.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return math.MaxInt64, errors.New("command usage cost exceeded accounting bounds")
	}
	return costMicroUSD.Round(0).IntPart(), nil
}

func SettleCommandUsage(channelID int, usage *dto.Usage, now time.Time) error {
	return SettleCommandUsageForModel(channelID, CommandFlashModel, usage, now)
}

func SettleCommandUsageForModel(channelID int, modelName string, usage *dto.Usage, now time.Time) error {
	if channelID <= 0 || usage == nil {
		return errors.New("invalid command settlement input")
	}
	cost, err := CalculateCommandCostMicroUSDForModel(usage, modelName, now)
	if err != nil {
		return err
	}
	return SettleCommandCostForModel(channelID, modelName, cost, now)
}

func SettleCommandCost(channelID int, costMicroUSD int64, now time.Time) error {
	return SettleCommandCostForModel(channelID, CommandFlashModel, costMicroUSD, now)
}

func SettleCommandCostForModel(channelID int, modelName string, costMicroUSD int64, now time.Time) error {
	if channelID <= 0 || costMicroUSD < 0 {
		return errors.New("invalid command settlement input")
	}
	if costMicroUSD == 0 {
		return nil
	}
	ts := now.Unix()
	return DB.Transaction(func(tx *gorm.DB) error {
		channel, found, err := getCommandChannelTx(tx, channelID)
		if err != nil {
			return err
		}
		if found {
			if err := ensureCommandPoolUsageRowsTx(tx, channel, ts); err != nil {
				return err
			}
		}
		channelUsage, err := getOrCreateCommandChannelUsageTx(tx, channelID, ts)
		if err != nil {
			return err
		}
		poolKey, poolLimit := CommandPoolKey(channelID), CommandCombinedPoolLimitMicroUSD
		if found {
			poolKey, poolLimit = commandPoolForModel(channel, modelName)
		}
		poolUsage, err := getOrCreateCommandPoolUsageByKeyTx(tx, poolKey, ts)
		if err != nil {
			return err
		}
		resetCommandChannelWindows(channelUsage, ts)
		channelUsage.FiveHourUsed = addCapped(channelUsage.FiveHourUsed, costMicroUSD, CommandFiveHourLimitMicroUSD)
		channelUsage.WeekUsed = addCapped(channelUsage.WeekUsed, costMicroUSD, CommandWeekLimitMicroUSD)
		channelUsage.MonthUsed = addCapped(channelUsage.MonthUsed, costMicroUSD, CommandMonthLimitMicroUSD)
		poolUsage.Used = addCapped(poolUsage.Used, costMicroUSD, poolLimit)
		if err := tx.Save(channelUsage).Error; err != nil {
			return err
		}
		return tx.Save(poolUsage).Error
	})
}

// SetCommandUsageOverride replaces the usage counters for one Command
// channel. It intentionally leaves RPM state untouched.
func SetCommandUsageOverride(channelID int, override CodeProviderUsageOverride, now int64) error {
	if channelID <= 0 {
		return errors.New("invalid command channel id")
	}
	channel, found, err := getCommandChannel(channelID)
	if err != nil {
		return err
	}
	if !found || channel.Type != constant.ChannelTypeCommand {
		return errors.New("channel is not command")
	}
	splitPools := commandChannelUsesSplitPools(channel)
	poolLimit := CommandCombinedPoolLimitMicroUSD
	if splitPools {
		poolLimit = CommandFlashPoolLimitMicroUSD + CommandV41PoolLimitMicroUSD + CommandProPoolLimitMicroUSD
	}
	if err := validateCodeProviderUsageOverride(override, CommandFiveHourLimitMicroUSD, CommandWeekLimitMicroUSD, CommandMonthLimitMicroUSD); err != nil {
		return err
	}
	if override.PoolUsedMicroUSD < 0 || override.PoolUsedMicroUSD > poolLimit {
		return errors.New("command pool usage exceeds the limit")
	}
	if splitPools {
		hasFlash := commandChannelServesModel(channel, CommandFlashModel)
		hasV41 := commandChannelServesModel(channel, CommandV41FlashModel)
		hasPro := commandChannelServesModel(channel, CommandProModel)
		// 启用的模型池必须提供用量；未启用的池不允许传值。
		if hasFlash && override.FlashPoolUsedMicroUSD == nil {
			return errors.New("command flash pool usage is required")
		}
		if hasV41 && override.V41PoolUsedMicroUSD == nil {
			return errors.New("command v4.1 pool usage is required")
		}
		if hasPro && override.ProPoolUsedMicroUSD == nil {
			return errors.New("command pro pool usage is required")
		}
		if override.FlashPoolUsedMicroUSD != nil {
			if *override.FlashPoolUsedMicroUSD < 0 || *override.FlashPoolUsedMicroUSD > CommandFlashPoolLimitMicroUSD {
				return errors.New("command flash pool usage exceeds the limit")
			}
		}
		if override.V41PoolUsedMicroUSD != nil {
			if *override.V41PoolUsedMicroUSD < 0 || *override.V41PoolUsedMicroUSD > CommandV41PoolLimitMicroUSD {
				return errors.New("command v4.1 pool usage exceeds the limit")
			}
		}
		if override.ProPoolUsedMicroUSD != nil {
			if *override.ProPoolUsedMicroUSD < 0 || *override.ProPoolUsedMicroUSD > CommandProPoolLimitMicroUSD {
				return errors.New("command pro pool usage exceeds the limit")
			}
		}
		poolSum := int64(0)
		if override.FlashPoolUsedMicroUSD != nil {
			poolSum += *override.FlashPoolUsedMicroUSD
		}
		if override.V41PoolUsedMicroUSD != nil {
			poolSum += *override.V41PoolUsedMicroUSD
		}
		if override.ProPoolUsedMicroUSD != nil {
			poolSum += *override.ProPoolUsedMicroUSD
		}
		if poolSum != override.PoolUsedMicroUSD {
			return errors.New("command model pool usage must equal the total pool usage")
		}
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		channel, found, err := getCommandChannelTx(tx, channelID)
		if err != nil {
			return err
		}
		if !found || channel.Type != constant.ChannelTypeCommand {
			return errors.New("channel is not command")
		}
		if err := ensureCommandPoolUsageRowsTx(tx, channel, now); err != nil {
			return err
		}
		usage, err := getOrCreateCommandChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		usage.FiveHourUsed, usage.WeekUsed, usage.MonthUsed = override.FiveHourUsedMicroUSD, override.WeekUsedMicroUSD, override.MonthUsedMicroUSD
		usage.FiveHourStart, usage.WeekStart, usage.MonthStart = now, now, now
		if err := tx.Save(usage).Error; err != nil {
			return err
		}

		if !splitPools {
			pool, err := getOrCreateCommandPoolUsageByKeyTx(tx, CommandPoolKey(channelID), now)
			if err != nil {
				return err
			}
			pool.Used, pool.ResetTime = override.PoolUsedMicroUSD, now
			return tx.Save(pool).Error
		}

		poolWrites := []struct {
			key  string
			used *int64
		}{
			{CommandFlashPoolKey(channelID), override.FlashPoolUsedMicroUSD},
			{CommandV41PoolKey(channelID), override.V41PoolUsedMicroUSD},
			{CommandProPoolKey(channelID), override.ProPoolUsedMicroUSD},
		}
		for _, write := range poolWrites {
			if write.used == nil {
				continue
			}
			pool, err := getOrCreateCommandPoolUsageByKeyTx(tx, write.key, now)
			if err != nil {
				return err
			}
			pool.Used, pool.ResetTime = *write.used, now
			if err := tx.Save(pool).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func GetCommandUsageSnapshot(channelID int, now int64) (*CommandUsageSnapshot, error) {
	if channelID <= 0 {
		return nil, errors.New("invalid channel id")
	}
	channel, found, err := getCommandChannel(channelID)
	if err != nil {
		return nil, err
	}
	if found {
		if err := ensureCommandPoolUsageRows(channel, now); err != nil {
			return nil, err
		}
	}
	channelUsage, err := getOrCreateCommandChannelUsage(channelID, now)
	if err != nil {
		return nil, err
	}
	poolUsage, err := getOrCreateCommandPoolUsageByKey(CommandPoolKey(channelID), now)
	if err != nil {
		return nil, err
	}
	splitPools := found && commandChannelUsesSplitPools(channel)
	hasFlash := found && commandChannelServesModel(channel, CommandFlashModel)
	hasV41 := found && commandChannelServesModel(channel, CommandV41FlashModel)
	hasPro := found && commandChannelServesModel(channel, CommandProModel)
	flashPool := poolUsage
	v41Pool := &CommandPoolUsage{}
	proPool := &CommandPoolUsage{}
	poolLimit := int64(CommandCombinedPoolLimitMicroUSD)
	if splitPools {
		// 总池额度 = 当前启用的模型池限额之和（未启用池不计入）。
		poolLimit = 0
		if hasFlash {
			poolLimit += CommandFlashPoolLimitMicroUSD
		}
		if hasV41 {
			poolLimit += CommandV41PoolLimitMicroUSD
		}
		if hasPro {
			poolLimit += CommandProPoolLimitMicroUSD
		}
		if poolLimit == 0 {
			poolLimit = CommandFlashPoolLimitMicroUSD + CommandV41PoolLimitMicroUSD + CommandProPoolLimitMicroUSD
		}
		flashPool, err = getOrCreateCommandPoolUsageByKey(CommandFlashPoolKey(channelID), now)
		if err != nil {
			return nil, err
		}
		v41Pool, err = getOrCreateCommandPoolUsageByKey(CommandV41PoolKey(channelID), now)
		if err != nil {
			return nil, err
		}
		proPool, err = getOrCreateCommandPoolUsageByKey(CommandProPoolKey(channelID), now)
		if err != nil {
			return nil, err
		}
	}
	resetCommandChannelWindows(channelUsage, now)
	poolUsed := poolUsage.Used
	poolResetAt := poolUsage.ResetTime
	if splitPools {
		if hasFlash {
			poolUsed = addCapped(poolUsed, flashPool.Used, poolLimit)
		}
		if hasV41 {
			poolUsed = addCapped(poolUsed, v41Pool.Used, poolLimit)
		}
		if hasPro {
			poolUsed = addCapped(poolUsed, proPool.Used, poolLimit)
		}
		poolResetAt = flashPool.ResetTime
		if hasV41 {
			poolResetAt = v41Pool.ResetTime
		}
		if hasPro {
			poolResetAt = proPool.ResetTime
		}
	}
	poolWindow := makeCommandWindow(poolUsed, poolLimit, poolResetAt)
	flashWindow := makeCommandWindow(flashPool.Used, func() int64 {
		if splitPools {
			return CommandFlashPoolLimitMicroUSD
		}
		return CommandCombinedPoolLimitMicroUSD
	}(), flashPool.ResetTime)
	v41Window := OpencodeUsageWindow{}
	if splitPools {
		v41Window = makeCommandWindow(v41Pool.Used, CommandV41PoolLimitMicroUSD, v41Pool.ResetTime)
	}
	proWindow := OpencodeUsageWindow{}
	if splitPools {
		proWindow = makeCommandWindow(proPool.Used, CommandProPoolLimitMicroUSD, proPool.ResetTime)
	}
	return &CommandUsageSnapshot{
		ChannelID:        channelID,
		FiveHour:         makeCommandWindow(channelUsage.FiveHourUsed, CommandFiveHourLimitMicroUSD, channelUsage.FiveHourStart+5*60*60),
		Week:             makeCommandWindow(channelUsage.WeekUsed, CommandWeekLimitMicroUSD, channelUsage.WeekStart+7*24*60*60),
		Month:            makeCommandWindow(channelUsage.MonthUsed, CommandMonthLimitMicroUSD, channelUsage.MonthStart+30*24*60*60),
		Pool:             poolWindow,
		FlashPool:        flashWindow,
		V41Pool:          v41Window,
		ProPool:          proWindow,
		SplitPools:       splitPools,
		FlashPoolActive:  hasFlash,
		V41PoolActive:    hasV41,
		ProPoolActive:    hasPro,
		PricingPeriod:    OpencodePricingPeriodAt(time.Unix(now, 0).UTC()),
		RPMCount:         normalizeCommandRPM(channelUsage.RPMCount),
		RPMLimit:         operation_setting.GetOpencodeConcurrencyLimit(),
		RPMResetAt:       channelUsage.RPMWindowStart + 60,
		ConcurrencyCount: normalizeCommandConcurrency(channelUsage.ConcurrencyCount),
		ConcurrencyLimit: operation_setting.GetOpencodeConcurrencyLimit(),
	}, nil
}

func ResetAllCommandUsage() error {
	now := time.Now().Unix()
	return DB.Transaction(func(tx *gorm.DB) error {
		var channelUsages []CommandChannelUsage
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

		var pools []CommandPoolUsage
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

func EnsureCommandPoolUsageRows() error {
	now := time.Now().Unix()
	var channels []Channel
	if err := DB.Where("type = ?", constant.ChannelTypeCommand).Find(&channels).Error; err != nil {
		return err
	}
	for _, channel := range channels {
		if err := ensureCommandPoolUsageRows(&channel, now); err != nil {
			return err
		}
	}
	return nil
}

func ensureCommandPoolUsageRows(channel *Channel, now int64) error {
	if channel == nil {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		return ensureCommandPoolUsageRowsTx(tx, channel, now)
	})
}

func ensureCommandPoolUsageRowsTx(tx *gorm.DB, channel *Channel, now int64) error {
	if channel == nil {
		return nil
	}
	legacy, legacyFound, err := findCommandPoolUsageTx(tx, CommandPoolKey(channel.Id), now)
	if err != nil {
		return err
	}
	if commandChannelUsesSplitPools(channel) {
		flash, flashFound, err := findCommandPoolUsageTx(tx, CommandFlashPoolKey(channel.Id), now)
		if err != nil {
			return err
		}
		if !flashFound {
			used := int64(0)
			if legacyFound {
				used = minMaxOpencode(legacy.Used, 0, CommandFlashPoolLimitMicroUSD)
			}
			flash = &CommandPoolUsage{PoolKey: CommandFlashPoolKey(channel.Id), Used: used, ResetTime: now}
			if err := tx.Create(flash).Error; err != nil {
				return err
			}
		}
		if legacyFound && legacy.Used != 0 {
			legacy.Used = 0
			if err := tx.Save(legacy).Error; err != nil {
				return err
			}
		}
		_, v41Found, err := findCommandPoolUsageTx(tx, CommandV41PoolKey(channel.Id), now)
		if err != nil {
			return err
		}
		if !v41Found {
			if err := tx.Create(&CommandPoolUsage{PoolKey: CommandV41PoolKey(channel.Id), ResetTime: now}).Error; err != nil {
				return err
			}
		}
		_, proFound, err := findCommandPoolUsageTx(tx, CommandProPoolKey(channel.Id), now)
		if err != nil {
			return err
		}
		if !proFound {
			if err := tx.Create(&CommandPoolUsage{PoolKey: CommandProPoolKey(channel.Id), ResetTime: now}).Error; err != nil {
				return err
			}
		}
		return nil
	}

	if !legacyFound {
		legacy = &CommandPoolUsage{PoolKey: CommandPoolKey(channel.Id), ResetTime: now}
		if err := tx.Create(legacy).Error; err != nil {
			return err
		}
	}
	flash, flashFound, err := findCommandPoolUsageTx(tx, CommandFlashPoolKey(channel.Id), now)
	if err != nil {
		return err
	}
	v41, v41Found, err := findCommandPoolUsageTx(tx, CommandV41PoolKey(channel.Id), now)
	if err != nil {
		return err
	}
	pro, proFound, err := findCommandPoolUsageTx(tx, CommandProPoolKey(channel.Id), now)
	if err != nil {
		return err
	}
	if flashFound || proFound || v41Found {
		if legacy.Used == 0 {
			if flashFound {
				legacy.Used = addCapped(legacy.Used, flash.Used, CommandCombinedPoolLimitMicroUSD)
			}
			if v41Found {
				legacy.Used = addCapped(legacy.Used, v41.Used, CommandCombinedPoolLimitMicroUSD)
			}
			if proFound {
				legacy.Used = addCapped(legacy.Used, pro.Used, CommandCombinedPoolLimitMicroUSD)
			}
		}
		legacy.Used = minMaxOpencode(legacy.Used, 0, CommandCombinedPoolLimitMicroUSD)
		if err := tx.Save(legacy).Error; err != nil {
			return err
		}
		if flashFound {
			flash.Used = 0
			if err := tx.Save(flash).Error; err != nil {
				return err
			}
		}
		if v41Found {
			v41.Used = 0
			if err := tx.Save(v41).Error; err != nil {
				return err
			}
		}
		if proFound {
			pro.Used = 0
			if err := tx.Save(pro).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func ReserveCommandRPM(channelID int, now int64) (bool, int64, error) {
	if channelID <= 0 {
		return false, 0, errors.New("invalid channel id")
	}
	var allowed bool
	var resetAt int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateCommandChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		resetCommandChannelWindows(usage, now)
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

func ReleaseCommandRPM(channelID int, now int64) error {
	if channelID <= 0 {
		return errors.New("invalid channel id")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateCommandChannelUsageTx(tx, channelID, now)
		if err != nil {
			return err
		}
		wasInWindow := now-usage.RPMWindowStart < 60
		resetCommandChannelWindows(usage, now)
		if !wasInWindow || usage.RPMCount <= 0 {
			return nil
		}
		usage.RPMCount--
		return tx.Save(usage).Error
	})
}

func getOrCreateCommandChannelUsage(channelID int, now int64) (*CommandChannelUsage, error) {
	var usage CommandChannelUsage
	err := DB.Where("channel_id = ?", channelID).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = CommandChannelUsage{ChannelID: channelID, FiveHourStart: now, WeekStart: now, MonthStart: now, RPMWindowStart: now}
		err = DB.Create(&usage).Error
	}
	return &usage, err
}

func getOrCreateCommandChannelUsageTx(tx *gorm.DB, channelID int, now int64) (*CommandChannelUsage, error) {
	var usage CommandChannelUsage
	err := lockForUpdate(tx).Where("channel_id = ?", channelID).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = CommandChannelUsage{ChannelID: channelID, FiveHourStart: now, WeekStart: now, MonthStart: now, RPMWindowStart: now}
		err = tx.Create(&usage).Error
	}
	return &usage, err
}

func getOrCreateCommandPoolUsage(channelID int, now int64) (*CommandPoolUsage, error) {
	return getOrCreateCommandPoolUsageByKey(CommandPoolKey(channelID), now)
}

func getOrCreateCommandPoolUsageByKey(poolKey string, now int64) (*CommandPoolUsage, error) {
	var usage CommandPoolUsage
	err := DB.Where("pool_key = ?", poolKey).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = CommandPoolUsage{PoolKey: poolKey, ResetTime: now}
		err = DB.Create(&usage).Error
	}
	return &usage, err
}

func getOrCreateCommandPoolUsageByKeyTx(tx *gorm.DB, poolKey string, now int64) (*CommandPoolUsage, error) {
	var usage CommandPoolUsage
	err := lockForUpdate(tx).Where("pool_key = ?", poolKey).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		usage = CommandPoolUsage{PoolKey: poolKey, ResetTime: now}
		err = tx.Create(&usage).Error
	}
	return &usage, err
}

func findCommandPoolUsageTx(tx *gorm.DB, poolKey string, now int64) (*CommandPoolUsage, bool, error) {
	var usage CommandPoolUsage
	err := lockForUpdate(tx).Where("pool_key = ?", poolKey).First(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &CommandPoolUsage{PoolKey: poolKey, ResetTime: now}, false, nil
	}
	return &usage, true, err
}

func getCommandChannel(channelID int) (*Channel, bool, error) {
	if !DB.Migrator().HasTable("channels") {
		return nil, false, nil
	}
	var channel Channel
	err := DB.Where("id = ?", channelID).First(&channel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	return &channel, err == nil, err
}

func getCommandChannelTx(tx *gorm.DB, channelID int) (*Channel, bool, error) {
	if !tx.Migrator().HasTable("channels") {
		return nil, false, nil
	}
	var channel Channel
	err := lockForUpdate(tx).Where("id = ?", channelID).First(&channel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	return &channel, err == nil, err
}

func resetCommandChannelWindows(usage *CommandChannelUsage, now int64) {
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

func makeCommandWindow(used, limit, resetAt int64) OpencodeUsageWindow {
	return makeOpencodeWindow(used, limit, resetAt)
}

func normalizeCommandRPM(count int) int {
	return int(minMaxOpencode(int64(count), 0, int64(operation_setting.GetOpencodeRPMLimit())))
}

func normalizeCommandConcurrency(count int) int {
	return int(minMaxOpencode(int64(count), 0, int64(operation_setting.GetOpencodeConcurrencyLimit())))
}

func ReserveCommandConcurrency(channelID int) (bool, error) {
	if channelID <= 0 {
		return false, errors.New("invalid channel id")
	}
	var allowed bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateCommandChannelUsageTx(tx, channelID, common.GetTimestamp())
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

func ReleaseCommandConcurrency(channelID int) error {
	if channelID <= 0 {
		return errors.New("invalid channel id")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		usage, err := getOrCreateCommandChannelUsageTx(tx, channelID, common.GetTimestamp())
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
