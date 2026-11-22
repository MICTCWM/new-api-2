// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Command Go 渠道账号池服务层。
//
// 一个 Command Go 渠道下挂多个 commandcode.ai 的 Go 账号（每账号一把 user_*
// 密钥），请求由池统一分配。本文件负责导入、额度刷新、轮转选择以及账号状态
// 维护；不涉及 HTTP 路由与 relay 适配。

const (
	commandGoImportLimit        = 50
	commandGoImportConcurrency  = 3
	commandGoRefreshConcurrency = 3
	commandGoErrorMaxLen        = 500
	// commandGoImportRefreshTimeout bounds the background refresh triggered by
	// an import so a stuck upstream cannot leave goroutines alive forever.
	commandGoImportRefreshTimeout = 10 * time.Minute
	// commandGoMonthlyRemainingFloor 是月度信用余额“可继续使用”的下限；
	// 剩余 <= 0.1 即视为额度耗尽（灰标），与约定阈值一致。
	commandGoMonthlyRemainingFloor = 0.1
)

// commandGoCandidatePattern 匹配所有形似 user_* 的片段，用于统计非法片段。
var commandGoCandidatePattern = regexp.MustCompile(`user_[a-zA-Z0-9_-]*`)

// commandGoKeyPattern 对齐原型 src/cli-auth.mjs 的 USER_KEY_RE。
var commandGoKeyPattern = regexp.MustCompile(`^user_[a-zA-Z0-9_-]{8,}$`)

// commandGoQuotaFetcher 抽象刷新时对上游额度查询的调用，便于测试注入。
type commandGoQuotaFetcher func(ctx context.Context, client commandcodeHTTPIface, origin, apiKey string) (*CommandcodeQuotaReport, error)

// commandGoFetchQuotaReport 是可在同包测试中替换的额度查询函数。
var commandGoFetchQuotaReport commandGoQuotaFetcher = FetchCommandcodeQuotaReport

// commandGoImportRefreshWG lets tests wait for import-triggered background
// refreshes to finish before tearing down the database.
var commandGoImportRefreshWG sync.WaitGroup

// WaitCommandGoImportRefreshes blocks until all import-triggered background
// refreshes have finished. Graceful shutdown calls it after cancelling
// common.ProcessContext and before closing the database, so no refresh can
// write after CloseDB.
func WaitCommandGoImportRefreshes() {
	commandGoImportRefreshWG.Wait()
}

// CommandGoRefreshFailure 描述一次批量刷新中的单个失败。
type CommandGoRefreshFailure struct {
	Id    uint   `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

// ExtractCommandGoKeys 从任意文本提取全部 user_* 密钥（按出现顺序去重），
// 并统计形似但格式非法的片段数量。
func ExtractCommandGoKeys(text string) (keys []string, invalid int) {
	seen := make(map[string]struct{})
	for _, candidate := range commandGoCandidatePattern.FindAllString(text, -1) {
		if !commandGoKeyPattern.MatchString(candidate) {
			invalid++
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		keys = append(keys, candidate)
	}
	return keys, invalid
}

// ImportCommandGoAccounts 从文本批量导入账号。
// 去重后最多 50 个；已存在的密钥归入 existing。新账号创建后立即返回，额度刷新
// 在后台执行，避免导入请求被上游刷新阻塞数分钟。
func ImportCommandGoAccounts(channelID int, text string) (created []model.CommandGoAccount, existing []model.CommandGoAccount, invalid int, err error) {
	if channelID <= 0 {
		return nil, nil, 0, fmt.Errorf("invalid channel id: %d", channelID)
	}
	// The process is shutting down: still allow the import to be recorded, but
	// do not start background refreshes that would race CloseDB. The launch path
	// re-checks below in case shutdown begins mid-import.
	refreshAllowed := common.ProcessContext().Err() == nil
	keys, invalid := ExtractCommandGoKeys(text)
	if len(keys) > commandGoImportLimit {
		return nil, nil, invalid, fmt.Errorf("单次最多导入 %d 个 Command Go 账号，实际 %d 个", commandGoImportLimit, len(keys))
	}
	if len(keys) == 0 {
		return nil, nil, invalid, nil
	}

	existingRows, err := model.FindCommandGoAccountsByApiKeys(channelID, keys)
	if err != nil {
		return nil, nil, invalid, err
	}
	existingByKey := make(map[string]model.CommandGoAccount, len(existingRows))
	for _, row := range existingRows {
		existingByKey[row.ApiKey] = row
	}

	var firstErr error
	createdIDs := make([]uint, 0, len(keys))
	for _, key := range keys {
		if row, ok := existingByKey[key]; ok {
			existing = append(existing, row)
			continue
		}
		account, isExisting, createErr := createCommandGoAccount(channelID, key)
		if createErr != nil {
			if firstErr == nil {
				firstErr = createErr
			}
			continue
		}
		if isExisting {
			existing = append(existing, *account)
			continue
		}
		created = append(created, *account)
		createdIDs = append(createdIDs, account.Id)
	}

	if len(createdIDs) > 0 && refreshAllowed {
		launchCommandGoImportRefresh(channelID, createdIDs)
	}
	return created, existing, invalid, firstErr
}

// commandGoBackgroundShutdownNoise reports whether a background refresh error
// is just shutdown fallout (cancelled context or a closed database) and should
// be silenced. These are never surfaced to users.
func commandGoBackgroundShutdownNoise(ctx context.Context, err error) bool {
	if err == nil {
		return true
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is closed") ||
		strings.Contains(message, "sql: database is closed") ||
		strings.Contains(message, "context canceled")
}

// launchCommandGoImportRefresh refreshes imported accounts in the background.
// It is bounded, cancellable, and recovers panics so a failed refresh never
// affects the import response.
func launchCommandGoImportRefresh(channelID int, accountIDs []uint) {
	if common.ProcessContext().Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(common.ProcessContext(), commandGoImportRefreshTimeout)
	commandGoImportRefreshWG.Add(1)
	go func() {
		defer commandGoImportRefreshWG.Done()
		defer cancel()
		sem := make(chan struct{}, commandGoImportConcurrency)
		var wg sync.WaitGroup
		for _, accountID := range accountIDs {
			wg.Add(1)
			go func(id uint) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil && ctx.Err() == nil {
						common.SysError(fmt.Sprintf("command go import refresh panicked: channel_id=%d account_id=%d err=%v", channelID, id, r))
					}
				}()
				select {
				case <-ctx.Done():
					return
				case sem <- struct{}{}:
				}
				defer func() { <-sem }()
				if _, refreshErr := RefreshCommandGoAccount(ctx, channelID, id); refreshErr != nil && !commandGoBackgroundShutdownNoise(ctx, refreshErr) {
					common.SysLog(fmt.Sprintf("command go import refresh failed: channel_id=%d account_id=%d err=%v", channelID, id, refreshErr))
				}
			}(accountID)
		}
		wg.Wait()
	}()
}

func createCommandGoAccount(channelID int, apiKey string) (*model.CommandGoAccount, bool, error) {
	account := &model.CommandGoAccount{
		ChannelId: channelID,
		Name:      model.CommandGoAccountDefaultName,
		ApiKey:    apiKey,
		Status:    model.CommandGoAccountStatusActive,
	}
	if err := model.CreateCommandGoAccount(account); err != nil {
		// 并发导入可能撞唯一约束；命中的行归入 existing。
		var duplicate model.CommandGoAccount
		if queryErr := model.DB.Where("channel_id = ? AND api_key = ?", channelID, apiKey).First(&duplicate).Error; queryErr == nil {
			return &duplicate, true, nil
		}
		return nil, false, err
	}
	return account, false, nil
}

// RefreshCommandGoAccount 通过渠道代理查询单个账号额度并写回快照。
func RefreshCommandGoAccount(ctx context.Context, channelID int, accountID uint) (model.CommandGoAccount, error) {
	account, err := model.GetCommandGoAccount(channelID, accountID)
	if err != nil {
		return model.CommandGoAccount{}, err
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		return model.CommandGoAccount{}, err
	}
	setting := channel.GetSetting()
	client, err := GetHttpClientWithProxySettings(setting.Proxy, setting)
	if err != nil {
		return model.CommandGoAccount{}, err
	}

	report, fetchErr := commandGoFetchQuotaReport(
		ctx,
		client,
		CommandcodeOriginFromBaseURL(channel.GetBaseURL()),
		strings.TrimSpace(account.ApiKey),
	)
	now := time.Now().Unix()

	if fetchErr != nil {
		message := commandGoTruncate(fetchErr.Error(), commandGoErrorMaxLen)
		updates := map[string]interface{}{
			"last_refreshed_at":  now,
			"last_refresh_error": message,
		}
		if isCommandGoInvalidKeyError(fetchErr) {
			updates["status"] = model.CommandGoAccountStatusError
			updates["last_error"] = message
		}
		if updateErr := model.UpdateCommandGoAccountSnapshot(channelID, accountID, updates); updateErr != nil {
			return model.CommandGoAccount{}, updateErr
		}
		// Re-read so callers never receive a stale pre-refresh snapshot.
		refreshed, reloadErr := model.GetCommandGoAccount(channelID, accountID)
		if reloadErr != nil {
			return model.CommandGoAccount{}, reloadErr
		}
		return *refreshed, fetchErr
	}

	credits, windows, available, planID := buildCommandGoSnapshots(report)
	// A report without credits is not a verified "still available" result.
	// Preserve the previous quota snapshot/availability and surface the gap in
	// last_refresh_error instead of marking the account back to available.
	creditsPresent := report != nil && report.Credits != nil

	updates := map[string]interface{}{
		"last_refreshed_at": now,
	}
	// Only overwrite plan_id when the report actually carries a plan. A credits
	// response without a plan block must not wipe the previously known plan.
	if report != nil && report.Plan != nil && planID != "" {
		updates["plan_id"] = planID
	}
	if creditsPresent {
		creditsJSON, marshalErr := common.Marshal(credits)
		if marshalErr != nil {
			return model.CommandGoAccount{}, marshalErr
		}
		windowsJSON, marshalErr := common.Marshal(windows)
		if marshalErr != nil {
			return model.CommandGoAccount{}, marshalErr
		}
		updates["last_refresh_error"] = ""
		updates["credits"] = string(creditsJSON)
		updates["window_usage"] = string(windowsJSON)
		updates["quota_available"] = available
	} else {
		updates["last_refresh_error"] = "quota report missing credits; previous quota state kept"
	}
	// 仅当账号仍使用默认名时，按 whoami 账户名重命名；管理员自定义名称不动。
	if displayName := commandGoReportDisplayName(report); displayName != "" {
		if account.Name == "" || account.Name == model.CommandGoAccountDefaultName {
			updates["name"] = displayName
		}
	}
	// 额度耗尽只把 active 降为 no_quota（灰标）；已 error/no_quota 的账号
	// 只能手动恢复，刷新成功不会自动解除封禁。
	if creditsPresent && !available && account.Status == model.CommandGoAccountStatusActive {
		updates["status"] = model.CommandGoAccountStatusNoQuota
	}
	if updateErr := model.UpdateCommandGoAccountSnapshot(channelID, accountID, updates); updateErr != nil {
		return model.CommandGoAccount{}, updateErr
	}
	refreshed, err := model.GetCommandGoAccount(channelID, accountID)
	if err != nil {
		return model.CommandGoAccount{}, err
	}
	return *refreshed, nil
}

func buildCommandGoSnapshots(report *CommandcodeQuotaReport) (
	credits model.CommandGoCreditsSnapshot,
	windows model.CommandGoWindowUsageSnapshot,
	available bool,
	planID string,
) {
	available = true
	if report == nil {
		return credits, windows, available, ""
	}
	if report.Plan != nil {
		planID = strings.TrimSpace(report.Plan.PlanID)
	}

	monthlyRemaining := 0.0
	if report.Credits != nil {
		c := report.Credits
		if c.MonthlyCredits != nil {
			credits.Monthly = *c.MonthlyCredits
		}
		if c.PurchasedCredits != nil {
			credits.Purchased = *c.PurchasedCredits
		}
		if c.FreeCredits != nil {
			credits.Free = *c.FreeCredits
		}
		monthlyRemaining = credits.Monthly
		if c.FiveHour != nil {
			windows.FiveHour = makeCommandGoWindowSnapshot(c.FiveHour)
		}
		if c.Weekly != nil {
			windows.Weekly = makeCommandGoWindowSnapshot(c.Weekly)
		}
		if c.FiveHour != nil && c.FiveHour.Exceeded {
			available = false
		}
		if c.Weekly != nil && c.Weekly.Exceeded {
			available = false
		}
		if c.MonthlyCredits != nil && *c.MonthlyCredits <= commandGoMonthlyRemainingFloor {
			available = false
		}
	}
	credits.TotalRemaining = credits.Monthly + credits.Purchased + credits.Free

	// 月度窗口：总额取自套餐额度表（同 commandcode_quota.go 的前缀匹配），
	// 已用 = clamp(总额 - 剩余, 0, 总额)；未知套餐时总额/已用留空。
	monthlyTotal := 0.0
	if info, ok := commandcodePlanInfo(planID); ok {
		monthlyTotal = info.MonthlyCredits
	} else if report.Plan != nil && report.Plan.MonthlyCredits != nil {
		monthlyTotal = *report.Plan.MonthlyCredits
	}
	monthly := model.CommandGoMonthlyWindowSnapshot{Cap: monthlyTotal, Remaining: monthlyRemaining}
	if monthlyTotal > 0 {
		used := monthlyTotal - monthlyRemaining
		if used < 0 {
			used = 0
		}
		if used > monthlyTotal {
			used = monthlyTotal
		}
		monthly.Used = used
		monthly.Percent = used * 100 / monthlyTotal
	}
	if report.Plan != nil {
		monthly.ResetAt = report.Plan.CurrentPeriodEnd
	}
	windows.Monthly = monthly
	return credits, windows, available, planID
}

func makeCommandGoWindowSnapshot(window *CommandcodeWindow) model.CommandGoWindowSnapshot {
	snapshot := model.CommandGoWindowSnapshot{
		Used:    window.Used,
		Cap:     window.Cap,
		ResetAt: window.ResetAt,
	}
	if window.Cap > 0 {
		snapshot.Percent = window.Used * 100 / window.Cap
	}
	return snapshot
}

func commandGoReportDisplayName(report *CommandcodeQuotaReport) string {
	if report == nil || report.Account == nil {
		return ""
	}
	if name := strings.TrimSpace(report.Account.UserName); name != "" {
		return name
	}
	return strings.TrimSpace(report.Account.Name)
}

func isCommandGoInvalidKeyError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "密钥被拒") ||
		strings.Contains(message, "HTTP 401") ||
		strings.Contains(message, "HTTP 403")
}

func commandGoTruncate(message string, max int) string {
	if max <= 0 {
		return message
	}
	runes := []rune(message)
	if len(runes) <= max {
		return message
	}
	return string(runes[:max])
}

// RefreshAllCommandGoAccounts 并发（3）刷新一个渠道下的所有账号。
func RefreshAllCommandGoAccounts(ctx context.Context, channelID int) (int, []CommandGoRefreshFailure) {
	accounts, err := model.ListCommandGoAccountsByChannel(channelID)
	if err != nil {
		return 0, []CommandGoRefreshFailure{{Error: err.Error()}}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, commandGoRefreshConcurrency)
	results := make([]*CommandGoRefreshFailure, len(accounts))
	var refreshed int
	var mu sync.Mutex

	for i, account := range accounts {
		wg.Add(1)
		go func(index int, current model.CommandGoAccount) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if _, refreshErr := RefreshCommandGoAccount(ctx, channelID, current.Id); refreshErr != nil {
				results[index] = &CommandGoRefreshFailure{
					Id:    current.Id,
					Name:  current.Name,
					Error: commandGoTruncate(refreshErr.Error(), commandGoErrorMaxLen),
				}
				return
			}
			mu.Lock()
			refreshed++
			mu.Unlock()
		}(i, account)
	}
	wg.Wait()

	failures := make([]CommandGoRefreshFailure, 0, len(results))
	for _, failure := range results {
		if failure != nil {
			failures = append(failures, *failure)
		}
	}
	return refreshed, failures
}

// SelectCommandGoAccount 选取 status=active 且额度可用的最久未用账号，
// 并在事务内更新 last_used_at。无可用账号时返回 (nil, nil)。
func SelectCommandGoAccount(channelID int) (*model.CommandGoAccount, error) {
	return model.SelectCommandGoAccount(channelID)
}

// PickCommandGoAccountForTest selects an account without updating the
// round-robin cursor, for channel tests.
func PickCommandGoAccountForTest(channelID int) (*model.CommandGoAccount, error) {
	return model.PickCommandGoAccountForTest(channelID)
}

// RecordCommandGoSuccess 记录一次成功：清空连续失败与 LastError，状态不变。
func RecordCommandGoSuccess(accountID uint) error {
	return model.RecordCommandGoAccountSuccess(accountID)
}

// RecordCommandGoFailure 记录一次失败：累加连续失败，达到 20 次置 error（红标）。
func RecordCommandGoFailure(accountID uint, errMsg string) error {
	return model.RecordCommandGoAccountFailure(accountID, commandGoTruncate(errMsg, commandGoErrorMaxLen))
}

// MarkCommandGoNoQuota 将账号置为 no_quota（灰标）。
func MarkCommandGoNoQuota(accountID uint, reason string) error {
	return model.MarkCommandGoAccountNoQuota(accountID, commandGoTruncate(reason, commandGoErrorMaxLen))
}

// RecoverCommandGoAccounts 手动恢复本渠道账号：status→active、连续失败清零，
// 并清除旧的额度不可用判定（管理员覆盖）。
func RecoverCommandGoAccounts(channelID int, ids []uint) (int64, error) {
	return model.RecoverCommandGoAccounts(channelID, ids)
}

// DeleteCommandGoAccounts 按 id 删除；ids 为空时按 status（error/no_quota）批量删除。
func DeleteCommandGoAccounts(channelID int, ids []uint, status string) (int64, error) {
	return model.DeleteCommandGoAccounts(channelID, ids, status)
}

// ListCommandGoAccounts 列出渠道账号并附带状态汇总。
func ListCommandGoAccounts(channelID int) ([]model.CommandGoAccount, model.CommandGoAccountSummary, error) {
	accounts, err := model.ListCommandGoAccountsByChannel(channelID)
	if err != nil {
		return nil, model.CommandGoAccountSummary{}, err
	}
	summary := model.CommandGoAccountSummary{Total: len(accounts)}
	for _, account := range accounts {
		switch account.Status {
		case model.CommandGoAccountStatusActive:
			summary.Active++
		case model.CommandGoAccountStatusError:
			summary.Error++
		case model.CommandGoAccountStatusNoQuota:
			summary.NoQuota++
		}
	}
	return accounts, summary, nil
}

// CountCommandGoAccountsByChannel 返回渠道账号总数。
func CountCommandGoAccountsByChannel(channelID int) (int64, error) {
	return model.CountCommandGoAccountsByChannel(channelID)
}
