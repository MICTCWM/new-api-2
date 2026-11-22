// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupCommandGoTestDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.CommandGoAccount{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
}

func withCommandGoFetch(t *testing.T, fetcher commandGoQuotaFetcher) {
	t.Helper()
	previous := commandGoFetchQuotaReport
	commandGoFetchQuotaReport = fetcher
	t.Cleanup(func() { commandGoFetchQuotaReport = previous })
}

func createCommandGoTestChannel(t *testing.T, channelID int) {
	t.Helper()
	baseURL := "https://api.commandcode.ai/provider/v1"
	channel := &model.Channel{Id: channelID, Key: "channel-key", BaseURL: &baseURL, Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(channel).Error)
}

func floatPtr(value float64) *float64 { return &value }

func commandGoTestReport(name string, monthlyCredits float64, fiveHourExceeded, weeklyExceeded bool) *CommandcodeQuotaReport {
	return &CommandcodeQuotaReport{
		Account: &CommandcodeAccount{UserName: name},
		Credits: &CommandcodeCredits{
			MonthlyCredits:   floatPtr(monthlyCredits),
			PurchasedCredits: floatPtr(0),
			FreeCredits:      floatPtr(0),
			FiveHour: &CommandcodeWindow{
				Used:     1,
				Cap:      10,
				Exceeded: fiveHourExceeded,
			},
			Weekly: &CommandcodeWindow{
				Used:     2,
				Cap:      20,
				Exceeded: weeklyExceeded,
			},
		},
		Plan: &CommandcodePlan{
			PlanID:           "individual-go",
			CurrentPeriodEnd: 1_900_000_000_000,
		},
	}
}

func TestExtractCommandGoKeysDedupesAndCountsInvalid(t *testing.T) {
	text := `user_aaaaaaaa user_bbbbbbbb user_aaaaaaaa user_short user_ okay`
	keys, invalid := ExtractCommandGoKeys(text)
	require.Equal(t, []string{"user_aaaaaaaa", "user_bbbbbbbb"}, keys)
	assert.Equal(t, 2, invalid)
}

func TestImportCommandGoAccountsCreatesRefreshesAndDedupes(t *testing.T) {
	setupCommandGoTestDB(t)
	createCommandGoTestChannel(t, 1)
	withCommandGoFetch(t, func(_ context.Context, _ commandcodeHTTPIface, _, apiKey string) (*CommandcodeQuotaReport, error) {
		names := map[string]string{
			"user_aaaaaaaa": "alice",
			"user_bbbbbbbb": "bob",
		}
		return commandGoTestReport(names[apiKey], 5, false, false), nil
	})

	text := `user_aaaaaaaa user_bbbbbbbb user_aaaaaaaa user_short`
	created, existing, invalid, err := ImportCommandGoAccounts(1, text)
	require.NoError(t, err)
	assert.Equal(t, 2, len(created))
	assert.Equal(t, 0, len(existing))
	assert.Equal(t, 1, invalid)
	require.Equal(t, "user_aaaaaaaa", created[0].ApiKey)
	require.Equal(t, "user_bbbbbbbb", created[1].ApiKey)

	// 刷新在后台执行：等待后台完成后重读行，确认 whoami 改名与额度快照。
	commandGoImportRefreshWG.Wait()
	first, err := model.GetCommandGoAccount(1, created[0].Id)
	require.NoError(t, err)
	assert.Equal(t, "alice", first.Name)
	assert.True(t, first.QuotaAvailable)

	// 再次导入：全部归入 existing，不应新建。
	created, existing, _, err = ImportCommandGoAccounts(1, text)
	require.NoError(t, err)
	assert.Empty(t, created)
	assert.Equal(t, 2, len(existing))

	count, err := CountCommandGoAccountsByChannel(1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
}

func TestImportCommandGoAccountsRejectsMoreThanLimit(t *testing.T) {
	setupCommandGoTestDB(t)
	keys := make([]string, 0, commandGoImportLimit+1)
	for i := 0; i <= commandGoImportLimit; i++ {
		keys = append(keys, fmt.Sprintf("user_%08d", i))
	}
	text := ""
	for _, key := range keys {
		text += key + "\n"
	}

	created, existing, _, err := ImportCommandGoAccounts(1, text)
	require.Error(t, err)
	assert.Empty(t, created)
	assert.Empty(t, existing)
}

func TestRecordCommandGoFailureMarksErrorAtThreshold(t *testing.T) {
	setupCommandGoTestDB(t)
	account := &model.CommandGoAccount{
		ChannelId:      1,
		Name:           "acct",
		ApiKey:         "user_failuretest",
		Status:         model.CommandGoAccountStatusActive,
		QuotaAvailable: true,
	}
	require.NoError(t, model.DB.Create(account).Error)

	for i := 0; i < model.CommandGoAccountFailureThreshold-1; i++ {
		require.NoError(t, RecordCommandGoFailure(account.Id, "boom"))
	}
	reloaded, err := model.GetCommandGoAccount(1, account.Id)
	require.NoError(t, err)
	assert.Equal(t, model.CommandGoAccountStatusActive, reloaded.Status)
	assert.Equal(t, model.CommandGoAccountFailureThreshold-1, reloaded.ConsecutiveFailures)

	require.NoError(t, RecordCommandGoFailure(account.Id, "boom again"))
	reloaded, err = model.GetCommandGoAccount(1, account.Id)
	require.NoError(t, err)
	assert.Equal(t, model.CommandGoAccountStatusError, reloaded.Status)
	assert.Equal(t, model.CommandGoAccountFailureThreshold, reloaded.ConsecutiveFailures)
	assert.Equal(t, "boom again", reloaded.LastError)

	require.NoError(t, RecordCommandGoSuccess(account.Id))
	reloaded, err = model.GetCommandGoAccount(1, account.Id)
	require.NoError(t, err)
	assert.Equal(t, 0, reloaded.ConsecutiveFailures)
	assert.Empty(t, reloaded.LastError)
	// 成功一次不清除红标，只能手动恢复。
	assert.Equal(t, model.CommandGoAccountStatusError, reloaded.Status)
}

func TestRefreshCommandGoAccountMonthlyQuotaBoundary(t *testing.T) {
	setupCommandGoTestDB(t)
	createCommandGoTestChannel(t, 1)
	withCommandGoFetch(t, func(_ context.Context, _ commandcodeHTTPIface, _, apiKey string) (*CommandcodeQuotaReport, error) {
		remaining := map[string]float64{
			"user_boundary01": 0.1,
			"user_boundary02": 0.11,
			"user_boundary03": 0.09,
		}
		return commandGoTestReport("", remaining[apiKey], false, false), nil
	})

	cases := []struct {
		apiKey        string
		wantAvailable bool
		wantStatus    string
	}{
		{"user_boundary01", false, model.CommandGoAccountStatusNoQuota},
		{"user_boundary02", true, model.CommandGoAccountStatusActive},
		{"user_boundary03", false, model.CommandGoAccountStatusNoQuota},
	}
	for _, tc := range cases {
		account := &model.CommandGoAccount{
			ChannelId:      1,
			Name:           "acct",
			ApiKey:         tc.apiKey,
			Status:         model.CommandGoAccountStatusActive,
			QuotaAvailable: true,
		}
		require.NoError(t, model.DB.Create(account).Error)

		refreshed, err := RefreshCommandGoAccount(context.Background(), 1, account.Id)
		require.NoError(t, err)
		assert.Equal(t, tc.wantAvailable, refreshed.QuotaAvailable, tc.apiKey)
		assert.Equal(t, tc.wantStatus, refreshed.Status, tc.apiKey)
	}
}

func TestRefreshCommandGoAccountKeepsQuotaWhenCreditsMissing(t *testing.T) {
	setupCommandGoTestDB(t)
	createCommandGoTestChannel(t, 1)
	withCommandGoFetch(t, func(_ context.Context, _ commandcodeHTTPIface, _, _ string) (*CommandcodeQuotaReport, error) {
		// Upstream answered but omitted the credits block: this is not a
		// verified "still available" result.
		return &CommandcodeQuotaReport{Account: &CommandcodeAccount{UserName: "acct"}}, nil
	})

	account := &model.CommandGoAccount{
		ChannelId:      1,
		Name:           "acct",
		ApiKey:         "user_nocredits",
		Status:         model.CommandGoAccountStatusActive,
		QuotaAvailable: false,
	}
	require.NoError(t, model.DB.Create(account).Error)

	refreshed, err := RefreshCommandGoAccount(context.Background(), 1, account.Id)
	require.NoError(t, err)
	assert.False(t, refreshed.QuotaAvailable, "missing credits must not mark the account available")
	assert.Equal(t, model.CommandGoAccountStatusActive, refreshed.Status)
	assert.Contains(t, refreshed.LastRefreshError, "missing credits")
}

func TestRefreshCommandGoAccountKeepsPlanWhenReportOmitsIt(t *testing.T) {
	setupCommandGoTestDB(t)
	createCommandGoTestChannel(t, 1)
	withCommandGoFetch(t, func(_ context.Context, _ commandcodeHTTPIface, _, _ string) (*CommandcodeQuotaReport, error) {
		// Credits present, but no plan block: must not clear the stored plan.
		return &CommandcodeQuotaReport{
			Credits: &CommandcodeCredits{
				MonthlyCredits:   floatPtr(5),
				PurchasedCredits: floatPtr(0),
				FreeCredits:      floatPtr(0),
			},
		}, nil
	})

	account := &model.CommandGoAccount{
		ChannelId:      1,
		Name:           "acct",
		ApiKey:         "user_plankeep01",
		Status:         model.CommandGoAccountStatusActive,
		QuotaAvailable: true,
		PlanId:         "individual-go",
	}
	require.NoError(t, model.DB.Create(account).Error)

	refreshed, err := RefreshCommandGoAccount(context.Background(), 1, account.Id)
	require.NoError(t, err)
	assert.Equal(t, "individual-go", refreshed.PlanId)
	assert.True(t, refreshed.QuotaAvailable)
}

func TestRefreshCommandGoAccountKeepsManualBan(t *testing.T) {
	setupCommandGoTestDB(t)
	createCommandGoTestChannel(t, 1)
	withCommandGoFetch(t, func(_ context.Context, _ commandcodeHTTPIface, _, _ string) (*CommandcodeQuotaReport, error) {
		return commandGoTestReport("", 5, false, false), nil
	})

	for _, status := range []string{model.CommandGoAccountStatusError, model.CommandGoAccountStatusNoQuota} {
		account := &model.CommandGoAccount{
			ChannelId:      1,
			Name:           "acct",
			ApiKey:         "user_manualban_" + status,
			Status:         status,
			QuotaAvailable: false,
		}
		require.NoError(t, model.DB.Create(account).Error)

		refreshed, err := RefreshCommandGoAccount(context.Background(), 1, account.Id)
		require.NoError(t, err)
		assert.True(t, refreshed.QuotaAvailable)
		assert.Equal(t, status, refreshed.Status)
	}
}

func TestRefreshCommandGoAccountMarksInvalidKeyAsError(t *testing.T) {
	setupCommandGoTestDB(t)
	createCommandGoTestChannel(t, 1)
	withCommandGoFetch(t, func(_ context.Context, _ commandcodeHTTPIface, _, _ string) (*CommandcodeQuotaReport, error) {
		return nil, fmt.Errorf("密钥被拒（HTTP 401），请确认渠道密钥为有效的 commandcode.ai 密钥")
	})
	account := &model.CommandGoAccount{
		ChannelId: 1,
		Name:      "acct",
		ApiKey:    "user_invalidkey",
		Status:    model.CommandGoAccountStatusActive,
	}
	require.NoError(t, model.DB.Create(account).Error)

	refreshed, err := RefreshCommandGoAccount(context.Background(), 1, account.Id)
	require.Error(t, err)
	assert.Equal(t, model.CommandGoAccountStatusError, refreshed.Status)
	assert.Contains(t, refreshed.LastError, "密钥被拒")
	assert.Contains(t, refreshed.LastRefreshError, "密钥被拒")
}

func TestSelectCommandGoAccountRotatesAndExcludes(t *testing.T) {
	setupCommandGoTestDB(t)

	accounts := []*model.CommandGoAccount{
		{ChannelId: 1, Name: "a1", ApiKey: "user_select0001", Status: model.CommandGoAccountStatusActive, QuotaAvailable: true, LastUsedAt: 100},
		{ChannelId: 1, Name: "a2", ApiKey: "user_select0002", Status: model.CommandGoAccountStatusActive, QuotaAvailable: true, LastUsedAt: 50},
		{ChannelId: 1, Name: "a3", ApiKey: "user_select0003", Status: model.CommandGoAccountStatusError, QuotaAvailable: true, LastUsedAt: 0},
		{ChannelId: 1, Name: "a4", ApiKey: "user_select0004", Status: model.CommandGoAccountStatusActive, QuotaAvailable: false, LastUsedAt: 0, LastRefreshedAt: 500},
		{ChannelId: 1, Name: "a5", ApiKey: "user_select0005", Status: model.CommandGoAccountStatusActive, QuotaAvailable: false, LastUsedAt: 0, LastRefreshedAt: 0},
	}
	for _, account := range accounts {
		require.NoError(t, model.DB.Create(account).Error)
	}

	selected, err := SelectCommandGoAccount(1)
	require.NoError(t, err)
	require.NotNil(t, selected)
	// a5 尚未刷新（last_refreshed_at=0）仍可尝试，且最久未用；a4 已刷新且额度不可用被排除。
	assert.Equal(t, "a5", selected.Name)
	assert.Greater(t, selected.LastUsedAt, int64(100))

	// 刚刚使用的 a5 不再是最久未用，轮到 a2。
	selected, err = SelectCommandGoAccount(1)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "a2", selected.Name)
}

func TestImportCommandGoAccountsDoesNotWaitForRefresh(t *testing.T) {
	setupCommandGoTestDB(t)
	createCommandGoTestChannel(t, 1)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	withCommandGoFetch(t, func(ctx context.Context, _ commandcodeHTTPIface, _, _ string) (*CommandcodeQuotaReport, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return commandGoTestReport("alice", 5, false, false), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		created, _, _, err := ImportCommandGoAccounts(1, "user_waitimport1")
		assert.NoError(t, err)
		assert.Len(t, created, 1)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("import blocked on background refresh")
	}
	// 后台刷新确实已启动。
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background refresh did not start")
	}
	close(release)
	commandGoImportRefreshWG.Wait()
}

func TestRecoverAndDeleteCommandGoAccounts(t *testing.T) {
	setupCommandGoTestDB(t)

	errorAccount := &model.CommandGoAccount{ChannelId: 1, Name: "err", ApiKey: "user_recover001", Status: model.CommandGoAccountStatusError, ConsecutiveFailures: 20}
	noQuotaAccount := &model.CommandGoAccount{ChannelId: 1, Name: "nq", ApiKey: "user_recover002", Status: model.CommandGoAccountStatusNoQuota}
	activeAccount := &model.CommandGoAccount{ChannelId: 1, Name: "ok", ApiKey: "user_recover003", Status: model.CommandGoAccountStatusActive}
	otherChannel := &model.CommandGoAccount{ChannelId: 2, Name: "other", ApiKey: "user_recover004", Status: model.CommandGoAccountStatusError, ConsecutiveFailures: 7}
	for _, account := range []*model.CommandGoAccount{errorAccount, noQuotaAccount, activeAccount, otherChannel} {
		require.NoError(t, model.DB.Create(account).Error)
	}

	_, err := RecoverCommandGoAccounts(1, []uint{errorAccount.Id})
	require.NoError(t, err)
	recovered, err := model.GetCommandGoAccount(1, errorAccount.Id)
	require.NoError(t, err)
	assert.Equal(t, model.CommandGoAccountStatusActive, recovered.Status)
	assert.Equal(t, 0, recovered.ConsecutiveFailures)
	assert.True(t, recovered.QuotaAvailable)

	// 跨渠道恢复必须被拒绝。
	updated, recoverErr := RecoverCommandGoAccounts(1, []uint{otherChannel.Id})
	require.NoError(t, recoverErr)
	assert.EqualValues(t, 0, updated)
	untouched, err := model.GetCommandGoAccount(2, otherChannel.Id)
	require.NoError(t, err)
	assert.Equal(t, model.CommandGoAccountStatusError, untouched.Status)
	assert.Equal(t, 7, untouched.ConsecutiveFailures)

	deleted, err := DeleteCommandGoAccounts(1, []uint{activeAccount.Id}, "")
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	deleted, err = DeleteCommandGoAccounts(1, nil, model.CommandGoAccountStatusNoQuota)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	_, err = DeleteCommandGoAccounts(1, nil, "")
	require.Error(t, err)
	_, err = DeleteCommandGoAccounts(1, nil, model.CommandGoAccountStatusActive)
	require.Error(t, err)

	remaining, summary, err := ListCommandGoAccounts(1)
	require.NoError(t, err)
	assert.Equal(t, 1, len(remaining))
	assert.Equal(t, 1, summary.Total)
	assert.Equal(t, 1, summary.Active)
	assert.Equal(t, 0, summary.Error)
	assert.Equal(t, 0, summary.NoQuota)
}

func TestPickCommandGoAccountForTestDoesNotUpdateCursor(t *testing.T) {
	setupCommandGoTestDB(t)
	account := &model.CommandGoAccount{
		ChannelId: 1, Name: "pick", ApiKey: "user_picktest1", Status: model.CommandGoAccountStatusActive,
		QuotaAvailable: true, LastUsedAt: 42,
	}
	require.NoError(t, model.DB.Create(account).Error)

	picked, err := PickCommandGoAccountForTest(1)
	require.NoError(t, err)
	require.NotNil(t, picked)
	assert.Equal(t, account.Id, picked.Id)

	reloaded, err := model.GetCommandGoAccount(1, account.Id)
	require.NoError(t, err)
	assert.Equal(t, int64(42), reloaded.LastUsedAt)
}
