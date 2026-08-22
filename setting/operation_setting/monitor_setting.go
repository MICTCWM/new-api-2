package operation_setting

import (
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/QuantumNous/new-api/setting/config"
)

type MonitorSetting struct {
	AutoTestChannelEnabled              bool    `json:"auto_test_channel_enabled"`
	AutoTestChannelMinutes              float64 `json:"auto_test_channel_minutes"`
	ChannelTestMode                     string  `json:"channel_test_mode"`
	ChannelTestConcurrency              int     `json:"channel_test_concurrency"`
	SpeedProtectionProbeIntervalMinutes float64 `json:"speed_protection_probe_interval_minutes"`
}

const (
	ChannelTestModeScheduledAll    = "scheduled_all"
	ChannelTestModeAutoBanOnly     = "auto_ban_only"
	ChannelTestModePassiveRecovery = "passive_recovery"

	ChannelTestConcurrencyOptionKey            = "monitor_setting.channel_test_concurrency"
	DefaultChannelTestConcurrency              = 1
	MaxChannelTestConcurrency                  = 32
	SpeedProtectionProbeIntervalOptionKey      = "monitor_setting.speed_protection_probe_interval_minutes"
	DefaultSpeedProtectionProbeIntervalMinutes = 30
	MinSpeedProtectionProbeIntervalMinutes     = 1
	MaxSpeedProtectionProbeIntervalMinutes     = 1440
)

// 默认配置
var monitorSetting = MonitorSetting{
	AutoTestChannelEnabled:              false,
	AutoTestChannelMinutes:              10,
	ChannelTestMode:                     ChannelTestModeScheduledAll,
	ChannelTestConcurrency:              DefaultChannelTestConcurrency,
	SpeedProtectionProbeIntervalMinutes: DefaultSpeedProtectionProbeIntervalMinutes,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("monitor_setting", &monitorSetting)
}

func GetMonitorSetting() *MonitorSetting {
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err == nil && frequency > 0 {
			monitorSetting.AutoTestChannelEnabled = true
			monitorSetting.AutoTestChannelMinutes = float64(frequency)
			monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
		}
	}
	if enabled, ok := os.LookupEnv("CHANNEL_TEST_ENABLED"); ok {
		parsed, err := strconv.ParseBool(enabled)
		if err == nil {
			monitorSetting.AutoTestChannelEnabled = parsed
		}
	}
	switch monitorSetting.ChannelTestMode {
	case ChannelTestModeAutoBanOnly, ChannelTestModePassiveRecovery:
	default:
		monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
	}
	monitorSetting.ChannelTestConcurrency = NormalizeChannelTestConcurrency(monitorSetting.ChannelTestConcurrency)
	monitorSetting.SpeedProtectionProbeIntervalMinutes = NormalizeSpeedProtectionProbeInterval(monitorSetting.SpeedProtectionProbeIntervalMinutes)
	return &monitorSetting
}

func NormalizeSpeedProtectionProbeInterval(minutes float64) float64 {
	if math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes < MinSpeedProtectionProbeIntervalMinutes {
		return DefaultSpeedProtectionProbeIntervalMinutes
	}
	if minutes > MaxSpeedProtectionProbeIntervalMinutes {
		return MaxSpeedProtectionProbeIntervalMinutes
	}
	return minutes
}

func NormalizeChannelTestConcurrency(concurrency int) int {
	if concurrency < 1 {
		return DefaultChannelTestConcurrency
	}
	if concurrency > MaxChannelTestConcurrency {
		return MaxChannelTestConcurrency
	}
	return concurrency
}

func ValidateChannelTestConcurrency(value string) error {
	concurrency, err := strconv.Atoi(value)
	if err != nil || concurrency < 1 || concurrency > MaxChannelTestConcurrency {
		return fmt.Errorf("channel test concurrency must be between 1 and %d", MaxChannelTestConcurrency)
	}
	return nil
}

func ValidateSpeedProtectionProbeInterval(value string) error {
	minutes, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(minutes) || math.IsInf(minutes, 0) || math.Trunc(minutes) != minutes || minutes < MinSpeedProtectionProbeIntervalMinutes || minutes > MaxSpeedProtectionProbeIntervalMinutes {
		return fmt.Errorf("speed protection probe interval must be between %d and %d minutes", MinSpeedProtectionProbeIntervalMinutes, MaxSpeedProtectionProbeIntervalMinutes)
	}
	return nil
}
