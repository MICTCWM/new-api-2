package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelSupportsRequestPath_OpenCodeChannel(t *testing.T) {
	// nil channel must never pass the path check.
	if channelSupportsRequestPath(nil, "/v1/responses", "deepseek-v4-flash") {
		t.Fatal("expected nil channel to be rejected")
	}

	// Opencode (type 61, not Advanced Custom type 58) must pass the path check
	// so that channel affinity can select an opencode channel.
	opencode := &model.Channel{Type: constant.ChannelTypeOpencode}
	if !channelSupportsRequestPath(opencode, "/v1/responses", "deepseek-v4-flash") {
		t.Fatal("expected opencode channel to pass the request path check")
	}

	// A regular OpenAI channel passes for any request path.
	openai := &model.Channel{Type: constant.ChannelTypeOpenAI}
	if !channelSupportsRequestPath(openai, "/v1/chat/completions", "gpt-4o") {
		t.Fatal("expected openai channel to pass the request path check")
	}
}

func TestSetupContextAppliesModelSplitMappingOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	common.SetContextKey(c, constant.ContextKeyModelSplitTarget, "split-target")
	mapping := `{"nn":"v4.1","other":"keep"}`
	channel := &model.Channel{
		Id:           1,
		Type:         constant.ChannelTypeOpenAI,
		Key:          "sk-test",
		ModelMapping: &mapping,
	}
	require.Nil(t, SetupContextForSelectedChannel(c, channel, "nn"))
	require.Equal(t, "nn", c.GetString("original_model"))
	mapped := common.GetContextKeyString(c, constant.ContextKeyChannelModelMapping)
	var parsed map[string]string
	require.NoError(t, common.Unmarshal([]byte(mapped), &parsed))
	require.Equal(t, "split-target", parsed["nn"])
	require.Equal(t, "keep", parsed["other"])
}

func withModelSplitSetting(t *testing.T, enabled bool, rules []operation_setting.ModelSplitRule) {
	t.Helper()
	setting := operation_setting.GetModelSplitSetting()
	originalEnabled := setting.Enabled
	originalRules := setting.Rules
	setting.Enabled = enabled
	setting.Rules = rules
	t.Cleanup(func() {
		setting.Enabled = originalEnabled
		setting.Rules = originalRules
	})
}

func useModelSplitDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ModelSplitUsage{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func modelSplitGinContext(t *testing.T, method, path string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, nil)
	common.SetContextKey(c, constant.ContextKeyUsingGroup, "vip")
	return c
}

func TestGetModelRequestVideoFetchDoesNotSelectChannel(t *testing.T) {
	c := modelSplitGinContext(t, http.MethodGet, "/v1/videos/task-1")
	modelRequest, shouldSelectChannel, err := getModelRequest(c)
	require.NoError(t, err)
	require.NotNil(t, modelRequest)
	require.False(t, shouldSelectChannel)
}

func TestApplyModelSplitRoutingSkippedOnFetchWithoutSpecifiedChannel(t *testing.T) {
	useModelSplitDB(t)
	withModelSplitSetting(t, true, []operation_setting.ModelSplitRule{{
		ID:            "r1",
		Enabled:       true,
		Groups:        []string{"vip"},
		OriginalModel: "nn",
		Model1:        "model-a",
		Percent1:      57,
		Model2:        "model-b",
		Percent2:      43,
	}})

	c := modelSplitGinContext(t, http.MethodGet, "/v1/videos/task-1")
	reservation, err := applyModelSplitRoutingIfNeeded(c, "nn", false, false)
	require.NoError(t, err)
	require.Nil(t, reservation)
	usage, err := model.GetModelSplitUsage("r1", "vip")
	require.NoError(t, err)
	require.Nil(t, usage)

	generate := modelSplitGinContext(t, http.MethodPost, "/v1/chat/completions")
	reserved, err := applyModelSplitRoutingIfNeeded(generate, "nn", true, false)
	require.NoError(t, err)
	require.NotNil(t, reserved)
	require.Equal(t, "model-a", reserved.TargetModel)

	pinnedFetch := modelSplitGinContext(t, http.MethodGet, "/v1/videos/task-1")
	pinned, err := applyModelSplitRoutingIfNeeded(pinnedFetch, "nn", false, true)
	require.NoError(t, err)
	require.NotNil(t, pinned)
}
