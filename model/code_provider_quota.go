package model

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

const codeProviderQuotaDisableReasonPrefix = "rolling quota exhausted;"

// codeProviderQuotaWindowState covers the rolling upstream windows that have
// an authoritative reset time. Local pricing pools are deliberately excluded:
// they do not have an upstream reset timestamp and therefore cannot safely be
// used to schedule an automatic re-enable.
type codeProviderQuotaWindowState struct {
	Available bool
	ResetAt   int64
}

func getCodeProviderQuotaWindowState(channel *Channel, now int64) (codeProviderQuotaWindowState, error) {
	if channel == nil || channel.Id <= 0 {
		return codeProviderQuotaWindowState{}, errors.New("code provider channel is missing")
	}
	if now <= 0 {
		now = time.Now().Unix()
	}

	state := codeProviderQuotaWindowState{Available: true}
	markExhausted := func(used, limit, resetAt int64) {
		if remainingOpencode(used, limit) > codeProviderMinimumRemaining(channel.Type) {
			return
		}
		state.Available = false
		if resetAt > state.ResetAt {
			state.ResetAt = resetAt
		}
	}

	switch channel.Type {
	case constant.ChannelTypeOpencode:
		usage, err := getOrCreateOpencodeChannelUsage(channel.Id, now)
		if err != nil {
			return state, err
		}
		before := *usage
		resetOpencodeChannelWindows(usage, now)
		if *usage != before {
			if err := DB.Save(usage).Error; err != nil {
				return state, err
			}
		}
		markExhausted(usage.FiveHourUsed, OpencodeFiveHourLimitMicroUSD, usage.FiveHourStart+int64((5*time.Hour)/time.Second))
		markExhausted(usage.WeekUsed, OpencodeWeekLimitMicroUSD, usage.WeekStart+int64((7*24*time.Hour)/time.Second))
		markExhausted(usage.MonthUsed, OpencodeMonthLimitMicroUSD, usage.MonthStart+int64((30*24*time.Hour)/time.Second))
	case constant.ChannelTypeCommand:
		usage, err := getOrCreateCommandChannelUsage(channel.Id, now)
		if err != nil {
			return state, err
		}
		before := *usage
		resetCommandChannelWindows(usage, now)
		if *usage != before {
			if err := DB.Save(usage).Error; err != nil {
				return state, err
			}
		}
		markExhausted(usage.FiveHourUsed, CommandFiveHourLimitMicroUSD, usage.FiveHourStart+int64((5*time.Hour)/time.Second))
		markExhausted(usage.WeekUsed, CommandWeekLimitMicroUSD, usage.WeekStart+int64((7*24*time.Hour)/time.Second))
		markExhausted(usage.MonthUsed, CommandMonthLimitMicroUSD, usage.MonthStart+int64((30*24*time.Hour)/time.Second))
	default:
		return codeProviderQuotaWindowState{}, fmt.Errorf("unsupported code provider channel type: %d", channel.Type)
	}
	return state, nil
}

func codeProviderMinimumRemaining(channelType int) int64 {
	if channelType == constant.ChannelTypeCommand {
		return CommandMinimumRemainingMicroUSD
	}
	return OpencodeMinimumRemainingMicroUSD
}

func GetCodeProviderQuotaDisabledUntil(channelID int) (int64, error) {
	if channelID <= 0 || DB == nil {
		return 0, errors.New("invalid code provider channel id")
	}
	channel, err := GetChannelById(channelID, false)
	if err != nil {
		return 0, err
	}
	switch channel.Type {
	case constant.ChannelTypeOpencode:
		var usage OpencodeChannelUsage
		err = DB.Where("channel_id = ?", channelID).First(&usage).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return usage.QuotaDisabledUntil, err
	case constant.ChannelTypeCommand:
		var usage CommandChannelUsage
		err = DB.Where("channel_id = ?", channelID).First(&usage).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return usage.QuotaDisabledUntil, err
	default:
		return 0, nil
	}
}

func setCodeProviderQuotaDisabledUntil(channelID int, until int64) error {
	if channelID <= 0 || until <= 0 {
		return errors.New("invalid code provider quota reset time")
	}
	channel, err := GetChannelById(channelID, true)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	return DB.Transaction(func(tx *gorm.DB) error {
		switch channel.Type {
		case constant.ChannelTypeOpencode:
			usage, err := getOrCreateOpencodeChannelUsageTx(tx, channelID, now)
			if err != nil {
				return err
			}
			if until > usage.QuotaDisabledUntil {
				usage.QuotaDisabledUntil = until
			}
			return tx.Save(usage).Error
		case constant.ChannelTypeCommand:
			usage, err := getOrCreateCommandChannelUsageTx(tx, channelID, now)
			if err != nil {
				return err
			}
			if until > usage.QuotaDisabledUntil {
				usage.QuotaDisabledUntil = until
			}
			return tx.Save(usage).Error
		default:
			return nil
		}
	})
}

func clearCodeProviderQuotaDisabledUntil(channelID int) error {
	if channelID <= 0 || DB == nil {
		return errors.New("invalid code provider channel id")
	}
	channel, err := GetChannelById(channelID, false)
	if err != nil {
		return err
	}
	switch channel.Type {
	case constant.ChannelTypeOpencode:
		return DB.Model(&OpencodeChannelUsage{}).Where("channel_id = ?", channelID).Update("quota_disabled_until", 0).Error
	case constant.ChannelTypeCommand:
		return DB.Model(&CommandChannelUsage{}).Where("channel_id = ?", channelID).Update("quota_disabled_until", 0).Error
	default:
		return nil
	}
}

// ReconcileCodeProviderQuotaChannel disables an enabled code-provider channel
// when a rolling quota is exhausted, and re-enables only a channel that this
// same mechanism disabled after its latest known reset time. Other automatic
// error disables and manual disables are left untouched.
func ReconcileCodeProviderQuotaChannel(channelID int, now int64) error {
	if channelID <= 0 || DB == nil {
		return errors.New("invalid code provider channel id")
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	channel, err := GetChannelById(channelID, true)
	if err != nil {
		return err
	}
	// Command Go (63) is fully excluded from channel-level rolling quota. It must
	// never be auto-disabled by this path; pool-account quota is handled
	// separately by the command-go account pool.
	if channel.Type == constant.ChannelTypeCommandGo {
		return nil
	}
	if channel.Type != constant.ChannelTypeOpencode && channel.Type != constant.ChannelTypeCommand {
		return nil
	}

	state, err := getCodeProviderQuotaWindowState(channel, now)
	if err != nil {
		return err
	}
	marker, err := GetCodeProviderQuotaDisabledUntil(channelID)
	if err != nil {
		return err
	}

	if channel.Status == common.ChannelStatusEnabled {
		if state.Available || state.ResetAt <= now {
			return nil
		}
		if err := setCodeProviderQuotaDisabledUntil(channelID, state.ResetAt); err != nil {
			return err
		}
		reason := fmt.Sprintf("%s automatic re-enable after %s", codeProviderQuotaDisableReasonPrefix, time.Unix(state.ResetAt, 0).UTC().Format(time.RFC3339))
		if !UpdateChannelStatus(channelID, "", common.ChannelStatusAutoDisabled, reason) {
			return fmt.Errorf("failed to disable code provider channel %d for quota", channelID)
		}
		common.SysLog(fmt.Sprintf("code provider channel #%d disabled until %s because its rolling quota is exhausted", channelID, time.Unix(state.ResetAt, 0).UTC().Format(time.RFC3339)))
		return nil
	}

	if channel.Status != common.ChannelStatusAutoDisabled || marker <= 0 {
		return nil
	}
	if now < marker {
		return nil
	}
	if !state.Available {
		if state.ResetAt > marker {
			return setCodeProviderQuotaDisabledUntil(channelID, state.ResetAt)
		}
		return nil
	}
	if err := clearCodeProviderQuotaDisabledUntil(channelID); err != nil {
		return err
	}
	if !UpdateChannelStatus(channelID, "", common.ChannelStatusEnabled, "rolling quota reset") {
		return fmt.Errorf("failed to re-enable code provider channel %d after quota reset", channelID)
	}
	common.SysLog(fmt.Sprintf("code provider channel #%d re-enabled after rolling quota reset", channelID))
	return nil
}

// ReconcileCodeProviderQuotaChannels is called by the periodic usage worker
// and also by request selection. This gives quota exhaustion immediate effect
// while still recovering channels that receive no traffic.
func ReconcileCodeProviderQuotaChannels(now int64) (int, int, error) {
	if DB == nil {
		return 0, 0, errors.New("database is not initialized")
	}
	var channels []Channel
	// Command Go (63) 以池账号额度为准，不参与渠道级配额对账。
	if err := DB.Where("type IN ? AND status IN ?", []int{constant.ChannelTypeOpencode, constant.ChannelTypeCommand}, []int{common.ChannelStatusEnabled, common.ChannelStatusAutoDisabled}).Find(&channels).Error; err != nil {
		return 0, 0, err
	}
	disabled, enabled := 0, 0
	for index := range channels {
		before := channels[index].Status
		if err := ReconcileCodeProviderQuotaChannel(channels[index].Id, now); err != nil {
			return disabled, enabled, err
		}
		updated, err := GetChannelById(channels[index].Id, false)
		if err != nil {
			return disabled, enabled, err
		}
		if before == common.ChannelStatusEnabled && updated.Status == common.ChannelStatusAutoDisabled {
			disabled++
		} else if before == common.ChannelStatusAutoDisabled && updated.Status == common.ChannelStatusEnabled {
			enabled++
		}
	}
	return disabled, enabled, nil
}
