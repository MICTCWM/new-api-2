// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupCommandGoControllerTestDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousCache := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.CommandGoAccount{}, &model.Ability{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled = previousCache
	})
}

func createCommandGoControllerTestChannel(t *testing.T, id int, channelType int) {
	t.Helper()
	channel := &model.Channel{Id: id, Type: channelType, Key: "channel-key", Name: "cg-test", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(channel).Error)
}

func newCommandGoHandlerContext(t *testing.T, method, path string, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	ctx.Request = httptest.NewRequest(method, path, reader)
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = params
	return ctx, recorder
}

func TestMaskCommandGoKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "empty", key: "", want: ""},
		{name: "short standard mask", key: "user_abc", want: "us****bc"},
		{name: "boundary standard mask", key: "1234567890", want: "1234**********7890"},
		{name: "user prefix preserved", key: "user_abcdefghijklmnopqrstuvwxyz", want: "user_****wxyz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, maskCommandGoKey(tt.key))
		})
	}
}

func TestCommandGoAccountToDTOContract(t *testing.T) {
	account := model.CommandGoAccount{
		Id:                  12,
		Name:                "alpha",
		ApiKey:              "user_abcdefghijklmnopqrstuvwxyz",
		Status:              model.CommandGoAccountStatusNoQuota,
		ConsecutiveFailures: 3,
		LastError:           "quota exhausted",
		LastUsedAt:          100,
		LastRefreshedAt:     200,
		PlanId:              "individual-go",
		QuotaAvailable:      false,
		Credits:             `{"monthly":10,"purchased":2,"free":1,"total_remaining":13}`,
		WindowUsage:         `{"five_hour":{"used":1,"cap":10,"percent":10,"reset_at":300},"weekly":{"used":2,"cap":20,"percent":10,"reset_at":400},"monthly":{"used":5,"cap":50,"percent":10,"remaining":45,"total":50,"reset_at":500}}`,
	}

	dto := commandGoAccountToDTO(account)

	assert.Equal(t, uint(12), dto.Id)
	assert.Equal(t, "user_****wxyz", dto.KeyMask)
	assert.Equal(t, model.CommandGoAccountStatusNoQuota, dto.Status)
	assert.Equal(t, 3, dto.ConsecutiveFailures)
	assert.Equal(t, "quota exhausted", dto.LastError)
	assert.Equal(t, "individual-go", dto.PlanId)
	assert.False(t, dto.QuotaAvailable)
	assert.Equal(t, 13.0, dto.Credits.TotalRemaining)
	assert.Equal(t, 2.0, dto.Credits.Purchased)
	assert.Equal(t, 1.0, dto.Windows.FiveHour.Used)
	assert.Equal(t, 10.0, dto.Windows.FiveHour.Cap)
	assert.Equal(t, 20.0, dto.Windows.Weekly.Cap)
	assert.Equal(t, 45.0, dto.Windows.Monthly.Remaining)
	assert.Equal(t, 50.0, dto.Windows.Monthly.Total)
	assert.Equal(t, int64(500), dto.Windows.Monthly.ResetAt)
}

func TestCommandGoAccountToDTOToleratesInvalidSnapshots(t *testing.T) {
	dto := commandGoAccountToDTO(model.CommandGoAccount{Id: 1, Credits: "not-json", WindowUsage: "{"})
	assert.Equal(t, uint(1), dto.Id)
	assert.Zero(t, dto.Credits.TotalRemaining)
	assert.Zero(t, dto.Windows.FiveHour.Cap)
}

func TestClassifyCommandGoFailure(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		message     string
		code        string
		wantNoQuota bool
	}{
		{name: "402 always no_quota", statusCode: http.StatusPaymentRequired, message: "anything", wantNoQuota: true},
		{name: "429 with quota wording", statusCode: http.StatusTooManyRequests, message: "Quota exceeded for this plan", wantNoQuota: true},
		{name: "429 rate limited code", statusCode: http.StatusTooManyRequests, code: "RATE_LIMITED", wantNoQuota: true},
		{name: "400 insufficient credits", statusCode: http.StatusBadRequest, message: "Insufficient credits", wantNoQuota: true},
		{name: "400 usage exceeded code", statusCode: http.StatusBadRequest, code: "USAGE_EXCEEDED", wantNoQuota: true},
		{name: "400 quota exceeded code", statusCode: http.StatusBadRequest, code: "QUOTA_EXCEEDED", wantNoQuota: true},
		{name: "400 insufficient quota code", statusCode: http.StatusBadRequest, code: "INSUFFICIENT_QUOTA", wantNoQuota: true},
		{name: "400 plain validation", statusCode: http.StatusBadRequest, message: "invalid model", wantNoQuota: false},
		{name: "401 auth is ordinary failure", statusCode: http.StatusUnauthorized, message: "invalid key", wantNoQuota: false},
		{name: "401 ignores quota code", statusCode: http.StatusUnauthorized, code: "USAGE_EXCEEDED", wantNoQuota: false},
		{name: "403 auth is ordinary failure", statusCode: http.StatusForbidden, message: "forbidden", wantNoQuota: false},
		{name: "500 is ordinary failure", statusCode: http.StatusInternalServerError, message: "boom", wantNoQuota: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantNoQuota, classifyCommandGoFailure(tt.statusCode, tt.message, tt.code))
		})
	}
}

func TestCommandGoAttemptAccountNilSafe(t *testing.T) {
	// Non-Command Go relay attempts carry no pool account; the attribution
	// helpers must be no-ops instead of panicking.
	var account *commandGoAttemptAccount
	require.NotPanics(t, func() {
		account.recordSuccess()
		account.recordFailure(nil, 0)
	})
}

func TestValidateCommandGoRequestParams(t *testing.T) {
	assert.Error(t, validateCommandGoRecoverRequest(nil))
	assert.NoError(t, validateCommandGoRecoverRequest([]uint{1}))

	assert.Error(t, validateCommandGoDeleteRequest(nil, ""))
	assert.Error(t, validateCommandGoDeleteRequest(nil, "active"))
	assert.NoError(t, validateCommandGoDeleteRequest(nil, model.CommandGoAccountStatusError))
	assert.NoError(t, validateCommandGoDeleteRequest(nil, model.CommandGoAccountStatusNoQuota))
	assert.NoError(t, validateCommandGoDeleteRequest([]uint{7}, ""))
}

func TestResolveCommandGoChannelValidatesType(t *testing.T) {
	setupCommandGoControllerTestDB(t)
	createCommandGoControllerTestChannel(t, 7101, constant.ChannelTypeCommandGo)
	createCommandGoControllerTestChannel(t, 7102, constant.ChannelTypeOpenAI)

	ctx, recorder := newCommandGoHandlerContext(t, http.MethodGet, "/channel/7101/commandgo/accounts", "", gin.Params{{Key: "id", Value: "7101"}})
	channel, ok := resolveCommandGoChannel(ctx)
	require.True(t, ok)
	require.NotNil(t, channel)
	assert.Equal(t, 7101, channel.Id)

	ctx, recorder = newCommandGoHandlerContext(t, http.MethodGet, "/channel/7102/commandgo/accounts", "", gin.Params{{Key: "id", Value: "7102"}})
	channel, ok = resolveCommandGoChannel(ctx)
	assert.False(t, ok)
	assert.Nil(t, channel)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "not a Command Go channel")

	ctx, recorder = newCommandGoHandlerContext(t, http.MethodGet, "/channel/9999/commandgo/accounts", "", gin.Params{{Key: "id", Value: "9999"}})
	_, ok = resolveCommandGoChannel(ctx)
	assert.False(t, ok)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestCommandGoDeleteHandlerRejectsEmptySelection(t *testing.T) {
	setupCommandGoControllerTestDB(t)
	createCommandGoControllerTestChannel(t, 7201, constant.ChannelTypeCommandGo)

	ctx, recorder := newCommandGoHandlerContext(t, http.MethodPost, "/channel/7201/commandgo/accounts/delete", `{}`, gin.Params{{Key: "id", Value: "7201"}})
	DeleteCommandGoAccounts(ctx)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "ids is required")
}

func TestCommandGoRecoverHandlerRejectsEmptyIds(t *testing.T) {
	setupCommandGoControllerTestDB(t)
	createCommandGoControllerTestChannel(t, 7202, constant.ChannelTypeCommandGo)

	ctx, recorder := newCommandGoHandlerContext(t, http.MethodPost, "/channel/7202/commandgo/accounts/recover", `{"ids":[]}`, gin.Params{{Key: "id", Value: "7202"}})
	RecoverCommandGoAccounts(ctx)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "ids is required")
}

func TestCommandGoImportHandlerRejectsBlankText(t *testing.T) {
	setupCommandGoControllerTestDB(t)
	createCommandGoControllerTestChannel(t, 7203, constant.ChannelTypeCommandGo)

	ctx, recorder := newCommandGoHandlerContext(t, http.MethodPost, "/channel/7203/commandgo/accounts/import", `{"text":"   "}`, gin.Params{{Key: "id", Value: "7203"}})
	ImportCommandGoAccounts(ctx)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "text is required")
}

func TestAddChannelBatchRejectsCommandGo(t *testing.T) {
	setupCommandGoControllerTestDB(t)

	ctx, recorder := newCommandGoHandlerContext(t, http.MethodPost, "/channel/", `{"mode":"batch","channel":{"type":63,"key":"user_batch01","name":"batch"}}`, nil)
	AddChannel(ctx)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "账号池")
	assert.Contains(t, recorder.Body.String(), `"success":false`)
}

func TestDeleteChannelCascadesCommandGoAccounts(t *testing.T) {
	setupCommandGoControllerTestDB(t)
	createCommandGoControllerTestChannel(t, 7301, constant.ChannelTypeCommandGo)
	account := &model.CommandGoAccount{ChannelId: 7301, Name: "a", ApiKey: "user_cascade01", Status: model.CommandGoAccountStatusActive}
	require.NoError(t, model.DB.Create(account).Error)

	channel := model.Channel{Id: 7301}
	require.NoError(t, channel.Delete())

	var count int64
	require.NoError(t, model.DB.Model(&model.CommandGoAccount{}).Where("channel_id = ?", 7301).Count(&count).Error)
	assert.Zero(t, count)
}

func TestBatchDeleteChannelsCascadesCommandGoAccounts(t *testing.T) {
	setupCommandGoControllerTestDB(t)
	createCommandGoControllerTestChannel(t, 7302, constant.ChannelTypeCommandGo)
	createCommandGoControllerTestChannel(t, 7303, constant.ChannelTypeCommandGo)
	for _, account := range []*model.CommandGoAccount{
		{ChannelId: 7302, Name: "a", ApiKey: "user_cascade02"},
		{ChannelId: 7303, Name: "b", ApiKey: "user_cascade03"},
	} {
		require.NoError(t, model.DB.Create(account).Error)
	}

	deleted, err := model.BatchDeleteChannels([]int{7302, 7303})
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted)

	var count int64
	require.NoError(t, model.DB.Model(&model.CommandGoAccount{}).Where("channel_id IN ?", []int{7302, 7303}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestCopyCommandGoAccounts(t *testing.T) {
	setupCommandGoControllerTestDB(t)
	createCommandGoControllerTestChannel(t, 7304, constant.ChannelTypeCommandGo)
	require.NoError(t, model.DB.Create(&model.CommandGoAccount{
		ChannelId: 7304, Name: "source", ApiKey: "user_copytest01", Status: model.CommandGoAccountStatusActive, QuotaAvailable: true, LastUsedAt: 123,
	}).Error)

	require.NoError(t, model.CopyCommandGoAccounts(7304, 7305))
	accounts, err := model.ListCommandGoAccountsByChannel(7305)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, "source", accounts[0].Name)
	assert.Equal(t, "user_copytest01", accounts[0].ApiKey)
	assert.Zero(t, accounts[0].LastUsedAt)
	assert.True(t, accounts[0].QuotaAvailable)
}
