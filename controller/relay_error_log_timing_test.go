package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// auto 分组在选渠后把真实分组写回 relayInfo，错误日志必须与消费日志同组，
// 否则 type=5 行会保留本来要隐藏的真实首字。
func TestErrorLogGroupUsesResolvedRelayInfoGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("group", "auto")

	tests := []struct {
		name     string
		info     *relaycommon.RelayInfo
		expected string
	}{
		{name: "without relay info falls back to context", info: nil, expected: "auto"},
		{name: "resolved auto group wins", info: &relaycommon.RelayInfo{UsingGroup: "vip"}, expected: "vip"},
		{name: "blank relay group falls back to context", info: &relaycommon.RelayInfo{UsingGroup: "  "}, expected: "auto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, errorLogGroup(c, tt.info))
		})
	}
}

func TestRelayInfoFirstResponseMillis(t *testing.T) {
	require.Zero(t, relayInfoFirstResponseMillis(nil))
	// 非流式与尚未收到首字时 FirstResponseTime 不晚于 StartTime。
	require.Zero(t, relayInfoFirstResponseMillis(&relaycommon.RelayInfo{}))

	start := time.Now()
	info := &relaycommon.RelayInfo{StartTime: start, FirstResponseTime: start.Add(9 * time.Second)}
	require.InDelta(t, 9000, relayInfoFirstResponseMillis(info), 2)
}

func TestWriteRelayErrorLogUsesResolvedGroupAndDisplayTiming(t *testing.T) {
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.RedisEnabled = previousRedis
	})

	setting := operation_setting.GetLogTimingOptimizationSetting()
	previousEnabled := setting.Enabled
	previousGroups := append([]string(nil), setting.Groups...)
	setting.Enabled = true
	setting.Groups = []string{"vip"}
	t.Cleanup(func() {
		setting.Enabled = previousEnabled
		setting.Groups = previousGroups
	})

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("group", "auto")
	common.SetContextKey(c, constant.ContextKeyIsStream, true)
	start := time.Now().Add(-30 * time.Second)
	common.SetContextKey(c, constant.ContextKeyRequestStartTime, start)

	relayErr := types.NewOpenAIError(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	writeRelayErrorLog(c, relayErr, &relaycommon.RelayInfo{
		UsingGroup:        "vip",
		StartTime:         start,
		FirstResponseTime: start.Add(20 * time.Second),
	})

	var record model.Log
	require.NoError(t, model.LOG_DB.Last(&record).Error)
	require.Equal(t, model.LogTypeError, record.Type)
	require.Equal(t, "vip", record.Group)

	other, decodeErr := common.StrToMap(record.Other)
	require.NoError(t, decodeErr)
	frt, ok := other["frt"].(float64)
	require.True(t, ok)
	require.GreaterOrEqual(t, frt, 8000.0)
	require.LessOrEqual(t, frt, 15000.0)
	// 真实 30s 耗时扣掉省下的 5-12s，且不短于展示首字。
	require.GreaterOrEqual(t, record.UseTime, 18)
	require.LessOrEqual(t, record.UseTime, 25)
	require.GreaterOrEqual(t, float64(record.UseTime)*1000, frt)
}
