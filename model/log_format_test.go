package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestFormatUserLogsStripsQuotaSaturation verifies the admin-only quota
// saturation marker (nested under other.admin_info) is removed for non-admin
// log views, since formatUserLogs strips the whole admin_info object.
func TestFormatUserLogsStripsQuotaSaturation(t *testing.T) {
	other := common.MapToJsonStr(map[string]interface{}{
		"model_price": 0.004,
		"admin_info": map[string]interface{}{
			"opencode_upstream_cost_usd":       0.000061,
			"opencode_upstream_cost_micro_usd": 61,
			"quota_saturation": map[string]interface{}{
				"op":      "QuotaFromDecimal",
				"kind":    "overflow",
				"clamped": common.MaxQuota,
			},
		},
	})
	logs := []*Log{{Other: other}}

	formatUserLogs(logs, 0)

	parsed, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	_, hasAdminInfo := parsed["admin_info"]
	require.False(t, hasAdminInfo, "admin_info (and nested quota_saturation) must be stripped for non-admin views")
	// Non-admin billing fields remain visible.
	require.Contains(t, parsed, "model_price")
}

func TestGetUserLogsExcludesSessionLifecycle(t *testing.T) {
	previousLogDB := LOG_DB
	logDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, logDB.AutoMigrate(&Log{}))
	LOG_DB = logDB
	t.Cleanup(func() {
		LOG_DB = previousLogDB
	})

	require.NoError(t, LOG_DB.Create(&Log{
		UserId:    42,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeSession,
		Content:   "OpenCode session create",
		ModelName: "deepseek-v4-flash",
		ChannelId: 9,
		Group:     "default",
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:    42,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeConsume,
		Content:   "consume",
		ModelName: "deepseek-v4-flash",
	}).Error)

	logs, total, err := GetUserLogs(42, LogTypeUnknown, 0, 0, "", "", 0, 20, "", "", "")
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, logs, 1)
	require.Equal(t, LogTypeConsume, logs[0].Type)

	sessionLogs, sessionTotal, err := GetUserLogs(42, LogTypeSession, 0, 0, "", "", 0, 20, "", "", "")
	require.NoError(t, err)
	require.Zero(t, sessionTotal)
	require.Empty(t, sessionLogs)
}

func TestGetAllLogsKeepsSessionOnDedicatedType(t *testing.T) {
	previousLogDB := LOG_DB
	logDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, logDB.AutoMigrate(&Log{}))
	LOG_DB = logDB
	t.Cleanup(func() {
		LOG_DB = previousLogDB
	})

	require.NoError(t, LOG_DB.Create(&Log{
		UserId:    7,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeSession,
		Content:   "OpenCode session create",
		ModelName: "deepseek-v4-flash",
		ChannelId: 9,
		Group:     "default",
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:    7,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeConsume,
		Content:   "consume",
		ModelName: "deepseek-v4-flash",
	}).Error)

	allLogs, allTotal, err := GetAllLogs(LogTypeUnknown, 0, 0, "", "", "", 0, 20, 0, "", "", "", "")
	require.NoError(t, err)
	require.Equal(t, int64(1), allTotal)
	require.Len(t, allLogs, 1)
	require.Equal(t, LogTypeConsume, allLogs[0].Type)

	sessionLogs, sessionTotal, err := GetAllLogs(LogTypeSession, 0, 0, "", "", "", 0, 20, 0, "", "", "", "create")
	require.NoError(t, err)
	require.Equal(t, int64(1), sessionTotal)
	require.Len(t, sessionLogs, 1)
	require.Equal(t, LogTypeSession, sessionLogs[0].Type)
	require.Equal(t, "OpenCode session create", sessionLogs[0].Content)
}
