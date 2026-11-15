package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

const (
	SpeedProtectionProbeDefaultInterval = 30 * time.Minute
	SpeedProtectionFailureWindow        = 20 * time.Minute
	SpeedProtectionEnterThreshold       = 5 * time.Second
	SpeedProtectionFailureThreshold     = 15 * time.Second
	SpeedProtectionFailureLimit         = 2
	SpeedProtectionRecoveryWindow       = 10 * time.Minute
	SpeedProtectionRecoveryRequestLimit = 20
)

const (
	speedProtectionEnteredAction = "speed_protection.entered"
	speedProtectionKickedAction  = "speed_protection.kicked"
)

type speedProtectionEvent struct {
	Action          string
	Event           string
	ChannelID       int
	Group           string
	FirstResponseMs int64
	FailureCount    int
	Trigger         string
	Timestamp       int64
}

// recordSpeedProtectionEvent writes only state transitions. Probe results and
// normal request observations that leave the state unchanged are intentionally
// not recorded, otherwise the usage log would be flooded by the scheduler.
func recordSpeedProtectionEvent(event *speedProtectionEvent) {
	if event == nil || LOG_DB == nil {
		return
	}

	content := fmt.Sprintf(
		"Speed protection %s for channel %d in group %s",
		event.Event,
		event.ChannelID,
		event.Group,
	)
	if event.Event == "kicked" {
		content = fmt.Sprintf(
			"Speed protection kicked channel %d out of group %s after %d slow responses",
			event.ChannelID,
			event.Group,
			event.FailureCount,
		)
	}

	other := map[string]interface{}{
		"op": buildOpField(event.Action, map[string]interface{}{
			"channel_id":        event.ChannelID,
			"group":             event.Group,
			"first_response_ms": event.FirstResponseMs,
			"failure_count":     event.FailureCount,
			"trigger":           event.Trigger,
		}),
		"speed_protection": map[string]interface{}{
			"event":             event.Event,
			"channel_id":        event.ChannelID,
			"group":             event.Group,
			"first_response_ms": event.FirstResponseMs,
			"failure_count":     event.FailureCount,
			"trigger":           event.Trigger,
		},
	}
	log := &Log{
		CreatedAt: event.Timestamp,
		Type:      LogTypeSystem,
		Content:   content,
		ChannelId: event.ChannelID,
		Group:     event.Group,
		Other:     common.MapToJsonStr(other),
	}
	if err := createLog(log); err != nil {
		common.SysLog("failed to record speed protection log: " + err.Error())
	}
}

// SpeedProtectionState is intentionally separate from ChannelOtherSettings.
// Configuration changes must not overwrite live probe counters.
type SpeedProtectionState struct {
	ID                  int    `json:"id"`
	ChannelID           int    `json:"channel_id" gorm:"uniqueIndex:idx_speed_protection_channel_group"`
	Group               string `json:"group" gorm:"size:128;uniqueIndex:idx_speed_protection_channel_group"`
	Active              bool   `json:"active"`
	Kicked              bool   `json:"kicked"`
	FailureCount        int    `json:"failure_count"`
	FailureWindowStart  int64  `json:"failure_window_start"`
	LastProbeAt         int64  `json:"last_probe_at"`
	LastFirstResponseMs int64  `json:"last_first_response_ms"`
	LastProbeSucceeded  bool   `json:"last_probe_succeeded"`
	RecoveryWindowStart int64  `json:"recovery_window_start"`
	RecoveryFastCount   int    `json:"recovery_fast_count"`
}

type SpeedProtectionStatus struct {
	Enabled      bool     `json:"enabled"`
	Groups       []string `json:"groups"`
	ActiveGroups []string `json:"active_groups"`
}

func normalizeSpeedProtectionGroups(groups []string) []string {
	seen := make(map[string]struct{}, len(groups))
	result := make([]string, 0, len(groups))
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		result = append(result, group)
	}
	sort.Strings(result)
	return result
}

func channelGroups(channel *Channel) map[string]struct{} {
	result := make(map[string]struct{})
	if channel == nil {
		return result
	}
	for _, group := range strings.Split(channel.Group, ",") {
		group = strings.TrimSpace(group)
		if group != "" {
			result[group] = struct{}{}
		}
	}
	return result
}

func ValidateSpeedProtectionSettings(channel *Channel, settings dto.ChannelOtherSettings) error {
	groups := normalizeSpeedProtectionGroups(settings.SpeedProtectionGroups)
	if !settings.SpeedProtectionEnabled {
		return nil
	}
	if channel == nil {
		return errors.New("speed protection requires a channel")
	}
	if !constant.IsStreamSupportedChannel(channel.Type) {
		return errors.New("speed protection requires a stream-capable channel")
	}
	if len(groups) == 0 {
		return errors.New("speed protection requires at least one group")
	}
	channelGroups := channelGroups(channel)
	for _, group := range groups {
		if _, ok := channelGroups[group]; !ok {
			return errors.New("speed protection groups must belong to the channel")
		}
	}
	return nil
}

func GetSpeedProtectionStatus(channel *Channel) (*SpeedProtectionStatus, error) {
	if channel == nil {
		return nil, errors.New("channel is missing")
	}
	settings := channel.GetOtherSettings()
	status := &SpeedProtectionStatus{
		Enabled:      settings.SpeedProtectionEnabled,
		Groups:       normalizeSpeedProtectionGroups(settings.SpeedProtectionGroups),
		ActiveGroups: []string{},
	}
	if len(status.Groups) == 0 || DB == nil {
		return status, nil
	}
	var states []SpeedProtectionState
	if err := DB.Where("channel_id = ? AND active = ?", channel.Id, true).Find(&states).Error; err != nil {
		return nil, err
	}
	configured := make(map[string]struct{}, len(status.Groups))
	for _, group := range status.Groups {
		configured[group] = struct{}{}
	}
	for _, state := range states {
		if _, ok := configured[state.Group]; ok {
			status.ActiveGroups = append(status.ActiveGroups, state.Group)
		}
	}
	sort.Strings(status.ActiveGroups)
	return status, nil
}

func EnrichSpeedProtectionStatuses(channels []*Channel) error {
	channelIDs := make([]int, 0, len(channels))
	for _, channel := range channels {
		if channel != nil && channel.Id > 0 {
			channelIDs = append(channelIDs, channel.Id)
		}
	}
	activeGroupsByChannel := make(map[int][]string, len(channelIDs))
	if DB != nil && len(channelIDs) > 0 {
		var states []SpeedProtectionState
		if err := DB.Where("channel_id IN ? AND active = ?", channelIDs, true).Find(&states).Error; err != nil {
			return err
		}
		for _, state := range states {
			activeGroupsByChannel[state.ChannelID] = append(activeGroupsByChannel[state.ChannelID], state.Group)
		}
	}
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		settings := channel.GetOtherSettings()
		configuredGroups := normalizeSpeedProtectionGroups(settings.SpeedProtectionGroups)
		configured := make(map[string]struct{}, len(configuredGroups))
		for _, group := range configuredGroups {
			configured[group] = struct{}{}
		}
		activeGroups := make([]string, 0, len(activeGroupsByChannel[channel.Id]))
		for _, group := range activeGroupsByChannel[channel.Id] {
			if _, ok := configured[group]; ok {
				activeGroups = append(activeGroups, group)
			}
		}
		sort.Strings(activeGroups)
		channel.SpeedProtection = SpeedProtectionStatus{
			Enabled:      settings.SpeedProtectionEnabled,
			Groups:       configuredGroups,
			ActiveGroups: activeGroups,
		}
	}
	return nil
}

func GetSpeedProtectionCandidates(group, modelName string, attempted map[int]struct{}) ([]*Channel, error) {
	if DB == nil || strings.TrimSpace(group) == "" || strings.TrimSpace(modelName) == "" {
		return nil, nil
	}
	var channels []*Channel
	query := DB.Table("channels").
		Select("channels.*").
		Joins("JOIN abilities ON abilities.channel_id = channels.id").
		Joins("JOIN speed_protection_states ON speed_protection_states.channel_id = channels.id AND speed_protection_states."+commonGroupCol+" = ?", group).
		Where("channels.status = ? AND abilities."+commonGroupCol+" = ? AND abilities.model = ? AND abilities.enabled = ? AND speed_protection_states.active = ?", common.ChannelStatusEnabled, group, modelName, true, true)
	if err := query.Find(&channels).Error; err != nil {
		return nil, err
	}
	result := make([]*Channel, 0, len(channels))
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		if IsFallbackChannel(channel.Id) {
			continue
		}
		if _, ok := attempted[channel.Id]; ok {
			continue
		}
		settings := channel.GetOtherSettings()
		if !settings.SpeedProtectionEnabled || !containsString(settings.SpeedProtectionGroups, group) {
			continue
		}
		if _, ok := channelGroups(channel)[group]; !ok {
			continue
		}
		result = append(result, channel)
	}
	return result, nil
}

// IsSpeedProtectionChannelKicked reports whether a channel was removed from
// speed protection for this configured group. Kicked state is scoped to the
// channel/group pair; the same channel remains eligible in other groups.
func IsSpeedProtectionChannelKicked(channel *Channel, group string) (bool, error) {
	if channel == nil || DB == nil || strings.TrimSpace(group) == "" {
		return false, nil
	}
	settings := channel.GetOtherSettings()
	if !settings.SpeedProtectionEnabled || !containsString(settings.SpeedProtectionGroups, group) {
		return false, nil
	}
	if _, ok := channelGroups(channel)[group]; !ok {
		return false, nil
	}
	var state SpeedProtectionState
	err := DB.Where("channel_id = ? AND "+commonGroupCol+" = ?", channel.Id, group).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return state.Kicked && !state.Active, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func UpdateSpeedProtectionProbe(channelID int, group string, firstResponseMs int64, succeeded bool, now int64) error {
	if channelID <= 0 || strings.TrimSpace(group) == "" || DB == nil {
		return errors.New("invalid speed protection probe input")
	}
	var event *speedProtectionEvent
	err := DB.Transaction(func(tx *gorm.DB) error {
		var state SpeedProtectionState
		err := lockForUpdate(tx).Where(map[string]any{"channel_id": channelID, "group": group}).First(&state).Error
		isNew := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !isNew {
			return err
		}
		if isNew {
			state = SpeedProtectionState{ChannelID: channelID, Group: group}
		}
		wasActive := state.Active
		state.LastProbeAt = now
		state.LastFirstResponseMs = max(firstResponseMs, 0)
		state.LastProbeSucceeded = succeeded
		if state.FailureWindowStart != 0 && now-state.FailureWindowStart >= int64(SpeedProtectionFailureWindow.Seconds()) {
			state.FailureCount = 0
			state.FailureWindowStart = 0
		}
		if succeeded && firstResponseMs <= SpeedProtectionEnterThreshold.Milliseconds() {
			state.Active = true
			if state.Kicked {
				state.Kicked = false
				state.FailureCount = 0
				state.FailureWindowStart = 0
				state.RecoveryWindowStart = 0
				state.RecoveryFastCount = 0
			}
		} else if !succeeded || firstResponseMs > SpeedProtectionFailureThreshold.Milliseconds() {
			if state.FailureWindowStart == 0 {
				state.FailureWindowStart = now
				state.FailureCount = 0
			}
			state.FailureCount++
			if state.FailureCount >= SpeedProtectionFailureLimit {
				state.Active = false
				state.Kicked = true
				state.FailureCount = 0
				state.FailureWindowStart = 0
				state.RecoveryWindowStart = now
				state.RecoveryFastCount = 0
			}
		}
		if !wasActive && state.Active {
			event = &speedProtectionEvent{
				Action:          speedProtectionEnteredAction,
				Event:           "entered",
				ChannelID:       channelID,
				Group:           group,
				FirstResponseMs: max(firstResponseMs, 0),
				Trigger:         "probe",
				Timestamp:       now,
			}
		} else if wasActive && !state.Active && state.Kicked {
			event = &speedProtectionEvent{
				Action:          speedProtectionKickedAction,
				Event:           "kicked",
				ChannelID:       channelID,
				Group:           group,
				FirstResponseMs: max(firstResponseMs, 0),
				FailureCount:    SpeedProtectionFailureLimit,
				Trigger:         "probe",
				Timestamp:       now,
			}
		}
		if isNew {
			return tx.Create(&state).Error
		}
		return tx.Save(&state).Error
	})
	if err != nil {
		return err
	}
	recordSpeedProtectionEvent(event)
	return nil
}

func ObserveSpeedProtectionRequest(channelID int, group string, firstResponseMs int64) error {
	return observeSpeedProtectionRequestAt(channelID, group, firstResponseMs, time.Now().Unix())
}

// observeSpeedProtectionRequestAt applies traffic-based state changes for a
// deterministic timestamp. It is called only from the streaming first-token
// path, so non-streaming requests never affect protection state.
func observeSpeedProtectionRequestAt(channelID int, group string, firstResponseMs int64, now int64) error {
	group = strings.TrimSpace(group)
	if channelID <= 0 || group == "" || firstResponseMs < 0 || now <= 0 || DB == nil {
		return nil
	}
	if firstResponseMs > SpeedProtectionFailureThreshold.Milliseconds() {
		var state SpeedProtectionState
		if err := DB.Where(map[string]any{"channel_id": channelID, "group": group}).First(&state).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if !state.Active {
			return nil
		}
		return UpdateSpeedProtectionProbe(channelID, group, firstResponseMs, false, now)
	}
	if firstResponseMs > SpeedProtectionEnterThreshold.Milliseconds() {
		return nil
	}

	var event *speedProtectionEvent
	err := DB.Transaction(func(tx *gorm.DB) error {
		var state SpeedProtectionState
		err := lockForUpdate(tx).Where(map[string]any{"channel_id": channelID, "group": group}).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if state.Active || !state.Kicked {
			return nil
		}

		windowSeconds := int64(SpeedProtectionRecoveryWindow / time.Second)
		if state.RecoveryWindowStart == 0 || now-state.RecoveryWindowStart >= windowSeconds {
			state.RecoveryWindowStart = now
			state.RecoveryFastCount = 0
		}
		state.RecoveryFastCount++
		if state.RecoveryFastCount >= SpeedProtectionRecoveryRequestLimit {
			state.Active = true
			state.Kicked = false
			state.RecoveryWindowStart = 0
			state.RecoveryFastCount = 0
			event = &speedProtectionEvent{
				Action:          speedProtectionEnteredAction,
				Event:           "entered",
				ChannelID:       channelID,
				Group:           group,
				FirstResponseMs: firstResponseMs,
				Trigger:         "passive_recovery",
				Timestamp:       now,
			}
		}
		return tx.Save(&state).Error
	})
	if err != nil {
		return err
	}
	recordSpeedProtectionEvent(event)
	return nil
}
