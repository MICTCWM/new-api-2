package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useSpeedProtectionDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &SpeedProtectionState{}))
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	initCol()
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		initCol()
	})
}

func getSpeedProtectionStateForTest(t *testing.T, channelID int, group string) SpeedProtectionState {
	t.Helper()
	var state SpeedProtectionState
	require.NoError(t, DB.Where(map[string]any{"channel_id": channelID, "group": group}).First(&state).Error)
	return state
}

func TestSpeedProtectionEntersAndLeavesAfterTwoSlowProbes(t *testing.T) {
	useSpeedProtectionDB(t)

	require.NoError(t, UpdateSpeedProtectionProbe(11, "default", 5_000, true, 1_000))
	state := getSpeedProtectionStateForTest(t, 11, "default")
	require.True(t, state.Active)
	require.Equal(t, 0, state.FailureCount)

	require.NoError(t, UpdateSpeedProtectionProbe(11, "default", 15_001, true, 1_001))
	require.NoError(t, UpdateSpeedProtectionProbe(11, "default", 15_001, true, 1_002))
	state = getSpeedProtectionStateForTest(t, 11, "default")
	require.False(t, state.Active)
	require.True(t, state.Kicked)
	require.Equal(t, 0, state.FailureCount)
	require.Zero(t, state.FailureWindowStart)

	require.NoError(t, UpdateSpeedProtectionProbe(11, "default", 4_000, true, 1_004))
	state = getSpeedProtectionStateForTest(t, 11, "default")
	require.True(t, state.Active)
	require.False(t, state.Kicked)
}

func TestSpeedProtectionStateTransitionsWriteUsageLogs(t *testing.T) {
	useSpeedProtectionDB(t)

	previousLogDB := LOG_DB
	logDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, logDB.AutoMigrate(&Log{}))
	LOG_DB = logDB
	t.Cleanup(func() {
		LOG_DB = previousLogDB
	})

	require.NoError(t, UpdateSpeedProtectionProbe(21, "default", 4_000, true, 3_000))
	require.NoError(t, UpdateSpeedProtectionProbe(21, "default", 16_000, true, 3_001))
	require.NoError(t, UpdateSpeedProtectionProbe(21, "default", 16_000, true, 3_002))

	var logs []Log
	require.NoError(t, LOG_DB.Order("created_at asc, id asc").Find(&logs).Error)
	require.Len(t, logs, 2)
	require.Equal(t, LogTypeSystem, logs[0].Type)
	require.Equal(t, LogTypeSystem, logs[1].Type)
	require.Equal(t, "default", logs[0].Group)
	require.Equal(t, "default", logs[1].Group)
	require.Contains(t, logs[0].Content, "Speed protection entered")
	require.Contains(t, logs[1].Content, "kicked")

	entered, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	enteredEvent, ok := entered["speed_protection"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "entered", enteredEvent["event"])

	kicked, err := common.StrToMap(logs[1].Other)
	require.NoError(t, err)
	kickedEvent, ok := kicked["speed_protection"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "kicked", kickedEvent["event"])
}

func TestSpeedProtectionClearsExpiredFailureWindowWithoutRemovingProtection(t *testing.T) {
	useSpeedProtectionDB(t)

	require.NoError(t, UpdateSpeedProtectionProbe(12, "default", 4_000, true, 2_000))
	require.NoError(t, UpdateSpeedProtectionProbe(12, "default", 16_000, true, 2_001))
	state := getSpeedProtectionStateForTest(t, 12, "default")
	require.Equal(t, 1, state.FailureCount)

	require.NoError(t, UpdateSpeedProtectionProbe(12, "default", 4_000, true, 2_000+int64(SpeedProtectionFailureWindow.Seconds())+1))
	state = getSpeedProtectionStateForTest(t, 12, "default")
	require.True(t, state.Active)
	require.Equal(t, 0, state.FailureCount)
	require.Zero(t, state.FailureWindowStart)
}

func TestKickedSpeedProtectionRecoversAfterTwentyFastStreamingRequests(t *testing.T) {
	useSpeedProtectionDB(t)

	require.NoError(t, DB.Create(&SpeedProtectionState{
		ChannelID:           14,
		Group:               "default",
		Kicked:              true,
		RecoveryWindowStart: 4_000,
		RecoveryFastCount:   19,
	}).Error)

	require.NoError(t, observeSpeedProtectionRequestAt(14, "default", 5_000, 4_001))
	state := getSpeedProtectionStateForTest(t, 14, "default")
	require.True(t, state.Active)
	require.False(t, state.Kicked)
	require.Zero(t, state.RecoveryWindowStart)
	require.Zero(t, state.RecoveryFastCount)
}

func TestKickedSpeedProtectionRecoveryWindowExpiresBeforeCountingAgain(t *testing.T) {
	useSpeedProtectionDB(t)

	require.NoError(t, DB.Create(&SpeedProtectionState{
		ChannelID:           15,
		Group:               "default",
		Kicked:              true,
		RecoveryWindowStart: 5_000,
		RecoveryFastCount:   19,
	}).Error)

	require.NoError(t, observeSpeedProtectionRequestAt(15, "default", 5_000, 5_000+int64(SpeedProtectionRecoveryWindow.Seconds())))
	state := getSpeedProtectionStateForTest(t, 15, "default")
	require.False(t, state.Active)
	require.True(t, state.Kicked)
	require.Equal(t, 1, state.RecoveryFastCount)
	require.Equal(t, 5_000+int64(SpeedProtectionRecoveryWindow.Seconds()), state.RecoveryWindowStart)
}

func TestSpeedProtectionFailedProbeCountsEvenWithoutFirstResponse(t *testing.T) {
	useSpeedProtectionDB(t)

	require.NoError(t, UpdateSpeedProtectionProbe(13, "default", 0, false, 3_000))
	state := getSpeedProtectionStateForTest(t, 13, "default")
	require.False(t, state.Active)
	require.Equal(t, 1, state.FailureCount)
}

func TestSpeedProtectionCandidatesRequireConfiguredActiveGroupAndModel(t *testing.T) {
	useSpeedProtectionDB(t)

	settings := `{"speed_protection_enabled":true,"speed_protection_groups":["default"]}`
	protected := &Channel{
		Type:          constant.ChannelTypeOpenAI,
		Status:        common.ChannelStatusEnabled,
		Key:           "protected-key",
		Models:        "gpt-4o",
		Group:         "default",
		OtherSettings: settings,
	}
	require.NoError(t, DB.Create(protected).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "gpt-4o", ChannelId: protected.Id, Enabled: true}).Error)
	require.NoError(t, DB.Create(&SpeedProtectionState{ChannelID: protected.Id, Group: "default", Active: true}).Error)

	candidates, err := GetSpeedProtectionCandidates("default", "gpt-4o", nil)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, protected.Id, candidates[0].Id)

	candidates, err = GetSpeedProtectionCandidates("other", "gpt-4o", nil)
	require.NoError(t, err)
	require.Empty(t, candidates)

	candidates, err = GetSpeedProtectionCandidates("default", "gpt-4o-mini", nil)
	require.NoError(t, err)
	require.Empty(t, candidates)
}

func TestSpeedProtectionKickedStateIsScopedToConfiguredGroup(t *testing.T) {
	useSpeedProtectionDB(t)

	channel := &Channel{
		Type:          constant.ChannelTypeOpenAI,
		Status:        common.ChannelStatusEnabled,
		Key:           "key",
		Models:        "gpt-4o",
		Group:         "default,other",
		OtherSettings: `{"speed_protection_enabled":true,"speed_protection_groups":["default"]}`,
	}
	require.NoError(t, DB.Create(channel).Error)
	require.NoError(t, DB.Create(&SpeedProtectionState{
		ChannelID: channel.Id,
		Group:     "default",
		Kicked:    true,
	}).Error)

	kicked, err := IsSpeedProtectionChannelKicked(channel, "default")
	require.NoError(t, err)
	require.True(t, kicked)

	kicked, err = IsSpeedProtectionChannelKicked(channel, "other")
	require.NoError(t, err)
	require.False(t, kicked)
}

func TestKickedSpeedProtectionChannelIsExcludedFromOnlyThatGroup(t *testing.T) {
	useSpeedProtectionDB(t)

	channel := &Channel{
		Type:          constant.ChannelTypeOpenAI,
		Status:        common.ChannelStatusEnabled,
		Key:           "key",
		Models:        "gpt-4o",
		Group:         "default,other",
		OtherSettings: `{"speed_protection_enabled":true,"speed_protection_groups":["default"]}`,
	}
	require.NoError(t, DB.Create(channel).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "gpt-4o", ChannelId: channel.Id, Enabled: true}).Error)
	require.NoError(t, DB.Create(&Ability{Group: "other", Model: "gpt-4o", ChannelId: channel.Id, Enabled: true}).Error)
	require.NoError(t, DB.Create(&SpeedProtectionState{ChannelID: channel.Id, Group: "default", Kicked: true}).Error)

	selected, err := GetChannel("default", "gpt-4o", 0, "")
	require.NoError(t, err)
	require.Nil(t, selected)

	selected, err = GetChannel("other", "gpt-4o", 0, "")
	require.NoError(t, err)
	require.NotNil(t, selected)
}

func TestValidateSpeedProtectionSettingsRejectsNonStreamingChannel(t *testing.T) {
	channel := &Channel{
		Type:  constant.ChannelTypeMidjourney,
		Group: "default",
	}
	settings := dto.ChannelOtherSettings{
		SpeedProtectionEnabled: true,
		SpeedProtectionGroups:  []string{"default"},
	}

	err := ValidateSpeedProtectionSettings(channel, settings)
	require.Error(t, err)
}
