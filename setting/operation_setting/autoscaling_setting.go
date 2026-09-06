package operation_setting

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	AutoScalingEnabledOptionKey         = "AutoScalingEnabled"
	AutoScalingRPMLimitOptionKey        = "AutoScalingRPMLimit"
	AutoScalingCooldownMinutesOptionKey = "AutoScalingCooldownMinutes"
	AutoScalingTokenOptionKey           = "AutoScalingToken"
	AutoScalingChannelIDsOptionKey      = "AutoScalingChannelIDs"
	AutoScalingInstanceIDsOptionKey     = "AutoScalingInstanceIDs"
	// AutoScalingStateOptionKey 持久化自动伸缩状态机（JSON 字符串），由状态机读写。
	AutoScalingStateOptionKey = "AutoScalingState"

	DefaultAutoScalingEnabled         = false
	DefaultAutoScalingRPMLimit        = 50
	DefaultAutoScalingCooldownMinutes = 30
	MinAutoScalingRPMLimit            = 1
	MinAutoScalingCooldownMinutes     = 1
	MaxAutoScalingCooldownMinutes     = 1440 // 1 天，防止冷却窗过大导致自动伸缩长期失效
)

var (
	autoScalingEnabled         atomic.Bool
	autoScalingRPMLimit        atomic.Int64
	autoScalingCooldownMinutes atomic.Int64
	autoScalingChannelIDs      atomic.Value
	autoScalingInstanceIDs     atomic.Value
	autoScalingToken           atomic.Value
)

func init() {
	autoScalingEnabled.Store(DefaultAutoScalingEnabled)
	autoScalingRPMLimit.Store(DefaultAutoScalingRPMLimit)
	autoScalingCooldownMinutes.Store(DefaultAutoScalingCooldownMinutes)
	autoScalingChannelIDs.Store([]int(nil))
	autoScalingInstanceIDs.Store([]string(nil))
	autoScalingToken.Store("")
}

// AutoScalingConfig 汇总自动伸缩的全部运行时配置。
type AutoScalingConfig struct {
	Enabled         bool
	RPMLimit        int
	CooldownMinutes int
	Token           string
	ChannelIDs      []int
	InstanceIDs     []string
}

// GetAutoScalingConfig 返回当前自动伸缩配置。
func GetAutoScalingConfig() AutoScalingConfig {
	token, _ := autoScalingToken.Load().(string)
	channelIDs, _ := autoScalingChannelIDs.Load().([]int)
	instanceIDs, _ := autoScalingInstanceIDs.Load().([]string)
	return AutoScalingConfig{
		Enabled:         autoScalingEnabled.Load(),
		RPMLimit:        int(autoScalingRPMLimit.Load()),
		CooldownMinutes: int(autoScalingCooldownMinutes.Load()),
		Token:           token,
		ChannelIDs:      channelIDs,
		InstanceIDs:     instanceIDs,
	}
}

func SetAutoScalingEnabled(value string) error {
	autoScalingEnabled.Store(strings.TrimSpace(value) == "true")
	return nil
}

func ValidateAutoScalingRPMLimit(value string) error {
	v, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || v < MinAutoScalingRPMLimit {
		return fmt.Errorf("AutoScaling RPM limit must be an integer >= %d", MinAutoScalingRPMLimit)
	}
	return nil
}

func SetAutoScalingRPMLimit(value string) error {
	if err := ValidateAutoScalingRPMLimit(value); err != nil {
		return err
	}
	v, _ := strconv.Atoi(strings.TrimSpace(value))
	autoScalingRPMLimit.Store(int64(v))
	return nil
}

func ValidateAutoScalingCooldownMinutes(value string) error {
	v, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || v < MinAutoScalingCooldownMinutes || v > MaxAutoScalingCooldownMinutes {
		return fmt.Errorf("AutoScaling cooldown minutes must be an integer between %d and %d", MinAutoScalingCooldownMinutes, MaxAutoScalingCooldownMinutes)
	}
	return nil
}

func SetAutoScalingCooldownMinutes(value string) error {
	if err := ValidateAutoScalingCooldownMinutes(value); err != nil {
		return err
	}
	v, _ := strconv.Atoi(strings.TrimSpace(value))
	autoScalingCooldownMinutes.Store(int64(v))
	return nil
}

func SetAutoScalingToken(value string) error {
	autoScalingToken.Store(value)
	return nil
}

// SetAutoScalingChannelIDs 解析逗号分隔的渠道 id 列表（如 "1,3,5"）并存储；空串视为未选择（nil）。
// 过滤掉 id<=0 的无效段并去重（忽略无效/重复段，仅存有效 id）；全部无效时存储 nil。
// 任一段非数字则返回错误。
func SetAutoScalingChannelIDs(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		autoScalingChannelIDs.Store([]int(nil))
		return nil
	}
	seen := make(map[int]struct{})
	var ids []int
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("AutoScaling channel ids must be comma-separated integers")
		}
		if v <= 0 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		ids = append(ids, v)
	}
	if len(ids) == 0 {
		autoScalingChannelIDs.Store([]int(nil))
		return nil
	}
	autoScalingChannelIDs.Store(ids)
	return nil
}

// SetAutoScalingInstanceIDs 解析逗号分隔的 AutoDL 实例 uuid 列表（如 "uu1,uu2,uu3"）并存储；
// 空串视为未选择（nil）。过滤空段并按首次出现去重；全部为空时存储 nil。
// uuid 是字符串（非数字），因此该函数不做整型校验，也应当做 trim 处理与空段跳过。
func SetAutoScalingInstanceIDs(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		autoScalingInstanceIDs.Store([]string(nil))
		return nil
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		ids = append(ids, part)
	}
	if len(ids) == 0 {
		autoScalingInstanceIDs.Store([]string(nil))
		return nil
	}
	autoScalingInstanceIDs.Store(ids)
	return nil
}
