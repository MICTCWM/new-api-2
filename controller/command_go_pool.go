// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/commandgo"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Command Go 渠道账号池管理接口。
//
// 这些接口只负责管理面：账号的导入、额度刷新、手动恢复与删除。请求链路的
// 账号选择、失败归因见 controller/relay.go 与 controller/channel-test.go。

// commandGoAccountDTO 是管理面板使用的账号视图，字段名与前端契约保持一致。
type commandGoAccountDTO struct {
	Id                  uint                    `json:"id"`
	Name                string                  `json:"name"`
	KeyMask             string                  `json:"key_mask"`
	Status              string                  `json:"status"`
	ConsecutiveFailures int                     `json:"consecutive_failures"`
	LastError           string                  `json:"last_error"`
	LastUsedAt          int64                   `json:"last_used_at"`
	LastRefreshedAt     int64                   `json:"last_refreshed_at"`
	PlanId              string                  `json:"plan_id"`
	QuotaAvailable      bool                    `json:"quota_available"`
	Credits             commandGoCreditsDTO     `json:"credits"`
	Windows             commandGoWindowUsageDTO `json:"windows"`
}

type commandGoCreditsDTO struct {
	Monthly        float64 `json:"monthly"`
	Purchased      float64 `json:"purchased"`
	Free           float64 `json:"free"`
	TotalRemaining float64 `json:"total_remaining"`
}

type commandGoWindowUsageDTO struct {
	FiveHour commandGoWindowDTO        `json:"five_hour"`
	Weekly   commandGoWindowDTO        `json:"weekly"`
	Monthly  commandGoMonthlyWindowDTO `json:"monthly"`
}

type commandGoWindowDTO struct {
	Used    float64 `json:"used"`
	Cap     float64 `json:"cap"`
	Percent float64 `json:"percent"`
	ResetAt int64   `json:"reset_at"`
}

type commandGoMonthlyWindowDTO struct {
	Used      float64 `json:"used"`
	Cap       float64 `json:"cap"`
	Percent   float64 `json:"percent"`
	Remaining float64 `json:"remaining"`
	Total     float64 `json:"total"`
	ResetAt   int64   `json:"reset_at"`
}

type commandGoAccountRefDTO struct {
	Id   uint   `json:"id"`
	Name string `json:"name"`
}

// maskCommandGoKey uses the project's standard token-key mask, preserving the
// "user_" prefix so admins can identify the account without exposing the key.
func maskCommandGoKey(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ""
	}
	const prefix = "user_"
	if strings.HasPrefix(trimmed, prefix) && len(trimmed) > len(prefix)+4 {
		return prefix + "****" + trimmed[len(trimmed)-4:]
	}
	return model.MaskTokenKey(trimmed)
}

func commandGoAccountToDTO(account model.CommandGoAccount) commandGoAccountDTO {
	var credits model.CommandGoCreditsSnapshot
	_ = common.UnmarshalJsonStr(account.Credits, &credits)
	var windows model.CommandGoWindowUsageSnapshot
	_ = common.UnmarshalJsonStr(account.WindowUsage, &windows)

	dto := commandGoAccountDTO{
		Id:                  account.Id,
		Name:                account.Name,
		KeyMask:             maskCommandGoKey(account.ApiKey),
		Status:              account.Status,
		ConsecutiveFailures: account.ConsecutiveFailures,
		LastError:           account.LastError,
		LastUsedAt:          account.LastUsedAt,
		LastRefreshedAt:     account.LastRefreshedAt,
		PlanId:              account.PlanId,
		QuotaAvailable:      account.QuotaAvailable,
		Credits: commandGoCreditsDTO{
			Monthly:        credits.Monthly,
			Purchased:      credits.Purchased,
			Free:           credits.Free,
			TotalRemaining: credits.TotalRemaining,
		},
		Windows: commandGoWindowUsageDTO{
			FiveHour: commandGoWindowDTO{
				Used:    windows.FiveHour.Used,
				Cap:     windows.FiveHour.Cap,
				Percent: windows.FiveHour.Percent,
				ResetAt: windows.FiveHour.ResetAt,
			},
			Weekly: commandGoWindowDTO{
				Used:    windows.Weekly.Used,
				Cap:     windows.Weekly.Cap,
				Percent: windows.Weekly.Percent,
				ResetAt: windows.Weekly.ResetAt,
			},
			Monthly: commandGoMonthlyWindowDTO{
				Used:      windows.Monthly.Used,
				Cap:       windows.Monthly.Cap,
				Percent:   windows.Monthly.Percent,
				Remaining: windows.Monthly.Remaining,
				Total:     windows.Monthly.Total,
				ResetAt:   windows.Monthly.ResetAt,
			},
		},
	}
	return dto
}

// resolveCommandGoChannel parses :id, loads the channel and enforces that it is
// a Command Go (type 63) channel. It writes the error response and returns
// ok=false when validation fails.
func resolveCommandGoChannel(c *gin.Context) (*model.Channel, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		commandGoBadRequest(c, fmt.Sprintf("invalid channel id: %s", c.Param("id")))
		return nil, false
	}
	channel, err := model.CacheGetChannel(id)
	if err != nil || channel == nil {
		commandGoBadRequest(c, fmt.Sprintf("channel #%d not found", id))
		return nil, false
	}
	if channel.Type != constant.ChannelTypeCommandGo {
		commandGoBadRequest(c, fmt.Sprintf("channel #%d is not a Command Go channel", id))
		return nil, false
	}
	return channel, true
}

func commandGoBadRequest(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, gin.H{
		"success": false,
		"message": message,
	})
}

// ListCommandGoAccounts GET /channel/:id/commandgo/accounts
func ListCommandGoAccounts(c *gin.Context) {
	channel, ok := resolveCommandGoChannel(c)
	if !ok {
		return
	}
	accounts, summary, err := service.ListCommandGoAccounts(channel.Id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	dtos := make([]commandGoAccountDTO, 0, len(accounts))
	for _, account := range accounts {
		dtos = append(dtos, commandGoAccountToDTO(account))
	}
	common.ApiSuccess(c, gin.H{
		"accounts": dtos,
		"summary":  summary,
	})
}

// ImportCommandGoAccounts POST /channel/:id/commandgo/accounts/import
func ImportCommandGoAccounts(c *gin.Context) {
	channel, ok := resolveCommandGoChannel(c)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		commandGoBadRequest(c, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		commandGoBadRequest(c, "text is required")
		return
	}

	created, existing, invalid, err := service.ImportCommandGoAccounts(channel.Id, req.Text)
	createdRefs := make([]commandGoAccountRefDTO, 0, len(created))
	for _, account := range created {
		createdRefs = append(createdRefs, commandGoAccountRefDTO{Id: account.Id, Name: account.Name})
	}
	existingRefs := make([]commandGoAccountRefDTO, 0, len(existing))
	for _, account := range existing {
		existingRefs = append(existingRefs, commandGoAccountRefDTO{Id: account.Id, Name: account.Name})
	}
	// Hard failures with no survivable results keep the standard error response.
	if err != nil && len(created) == 0 && len(existing) == 0 {
		common.ApiError(c, err)
		return
	}
	// Partial failures still return the successfully created/existing results so
	// the frontend can render them; the message explains what failed.
	message := ""
	if err != nil {
		message = "部分账号导入失败: " + err.Error()
		common.SysError("command go import partially failed: channel_id=" + strconv.Itoa(channel.Id) + " err=" + err.Error())
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": message,
		"data": gin.H{
			"created":  createdRefs,
			"existing": existingRefs,
			"invalid":  invalid,
		},
	})
}

// RefreshAllCommandGoAccounts POST /channel/:id/commandgo/accounts/refresh
func RefreshAllCommandGoAccounts(c *gin.Context) {
	channel, ok := resolveCommandGoChannel(c)
	if !ok {
		return
	}
	refreshed, failures := service.RefreshAllCommandGoAccounts(c.Request.Context(), channel.Id)
	if failures == nil {
		failures = []service.CommandGoRefreshFailure{}
	}
	common.ApiSuccess(c, gin.H{
		"refreshed": refreshed,
		"failed":    failures,
	})
}

// RefreshCommandGoAccount POST /channel/:id/commandgo/accounts/:accountId/refresh
func RefreshCommandGoAccount(c *gin.Context) {
	channel, ok := resolveCommandGoChannel(c)
	if !ok {
		return
	}
	accountID, err := strconv.ParseUint(c.Param("accountId"), 10, 64)
	if err != nil || accountID == 0 {
		commandGoBadRequest(c, "invalid account id")
		return
	}
	account, err := service.RefreshCommandGoAccount(c.Request.Context(), channel.Id, uint(accountID))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, commandGoAccountToDTO(account))
}

// RecoverCommandGoAccounts POST /channel/:id/commandgo/accounts/recover
func RecoverCommandGoAccounts(c *gin.Context) {
	channel, ok := resolveCommandGoChannel(c)
	if !ok {
		return
	}
	var req struct {
		Ids []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		commandGoBadRequest(c, "invalid request body")
		return
	}
	if err := validateCommandGoRecoverRequest(req.Ids); err != nil {
		commandGoBadRequest(c, err.Error())
		return
	}
	updated, err := service.RecoverCommandGoAccounts(channel.Id, req.Ids)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"updated": updated})
}

// DeleteCommandGoAccounts POST /channel/:id/commandgo/accounts/delete
func DeleteCommandGoAccounts(c *gin.Context) {
	channel, ok := resolveCommandGoChannel(c)
	if !ok {
		return
	}
	var req struct {
		Ids    []uint `json:"ids"`
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		commandGoBadRequest(c, "invalid request body")
		return
	}
	status := strings.TrimSpace(req.Status)
	if err := validateCommandGoDeleteRequest(req.Ids, status); err != nil {
		commandGoBadRequest(c, err.Error())
		return
	}
	deleted, err := service.DeleteCommandGoAccounts(channel.Id, req.Ids, status)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}

// validateCommandGoRecoverRequest requires a non-empty account id list.
func validateCommandGoRecoverRequest(ids []uint) error {
	if len(ids) == 0 {
		return fmt.Errorf("ids is required")
	}
	return nil
}

// validateCommandGoDeleteRequest requires either explicit ids or a status that
// restricts the bulk delete to error/no_quota accounts.
func validateCommandGoDeleteRequest(ids []uint, status string) error {
	if len(ids) > 0 {
		return nil
	}
	if status != model.CommandGoAccountStatusError && status != model.CommandGoAccountStatusNoQuota {
		return fmt.Errorf("ids is required, or status must be error/no_quota")
	}
	return nil
}

// commandGoFailurePhrases are upstream bodies that mean the account is out of
// quota even when the status code alone is ambiguous (400/429). Matched
// case-insensitively against the raw error message.
var commandGoFailurePhrases = []string{
	"insufficient credits",
	"no credits",
	"usage limit",
	"quota",
	"payment required",
	"usage_exceeded",
	"rate_limited",
	"quota_exceeded",
	"insufficient_quota",
}

// classifyCommandGoFailure reports whether an upstream failure means the pool
// account has no usable quota (grey no_quota marker) rather than a transient
// or auth failure (red error marker after the consecutive-failure threshold).
//
// 402 always means quota exhaustion. 400/429 only count as quota exhaustion
// when the body carries an explicit quota phrase; other 4xx/5xx (including
// 401/403) are ordinary failures.
func classifyCommandGoFailure(statusCode int, message string, upstreamCode string) bool {
	if statusCode == http.StatusPaymentRequired {
		return true
	}
	if statusCode != http.StatusTooManyRequests && statusCode != http.StatusBadRequest {
		return false
	}
	if isCommandGoQuotaCode(upstreamCode) {
		return true
	}
	lower := strings.ToLower(message)
	for _, phrase := range commandGoFailurePhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// commandGoQuotaCodes are upstream error codes that unambiguously mean the
// account's quota/rate budget is exhausted. They are checked before falling
// back to message phrase matching.
var commandGoQuotaCodes = map[string]struct{}{
	"USAGE_EXCEEDED":     {},
	"RATE_LIMITED":       {},
	"QUOTA_EXCEEDED":     {},
	"INSUFFICIENT_QUOTA": {},
}

func isCommandGoQuotaCode(code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return false
	}
	_, ok := commandGoQuotaCodes[code]
	return ok
}

// commandGoAttemptAccount captures the pool account used by one upstream
// attempt so its outcome can be attributed after the relay returns.
type commandGoAttemptAccount struct {
	id  uint
	key string
}

func newCommandGoAttemptAccount(account *model.CommandGoAccount) *commandGoAttemptAccount {
	if account == nil {
		return nil
	}
	return &commandGoAttemptAccount{id: account.Id, key: account.ApiKey}
}

// recordSuccess clears the failure counters for the account.
func (a *commandGoAttemptAccount) recordSuccess() {
	if a == nil {
		return
	}
	if err := service.RecordCommandGoSuccess(a.id); err != nil {
		common.SysError(fmt.Sprintf("failed to record Command Go account success: account_id=%d err=%v", a.id, err))
	}
}

// recordFailure invalidates the account's current upstream session and routes
// the failure to the no_quota or consecutive-failure path. sourceStatusCode
// overrides the status carried by upstreamErr when the caller recorded the raw
// upstream status separately (e.g. the channel-test path wraps every failure in
// a 500). Pass 0 to derive it from upstreamErr.
func (a *commandGoAttemptAccount) recordFailure(upstreamErr *types.NewAPIError, sourceStatusCode int) {
	if a == nil {
		return
	}
	commandgo.ResetSession(a.key)

	statusCode := sourceStatusCode
	message := ""
	upstreamCode := ""
	if upstreamErr != nil {
		if statusCode == 0 {
			statusCode = upstreamErr.StatusCode
			if upstreamErr.SourceStatusCode != 0 {
				statusCode = upstreamErr.SourceStatusCode
			}
		}
		message = upstreamErr.Error()
		upstreamCode = string(upstreamErr.GetErrorCode())
	}
	if classifyCommandGoFailure(statusCode, message, upstreamCode) {
		if err := service.MarkCommandGoNoQuota(a.id, message); err != nil {
			common.SysError(fmt.Sprintf("failed to mark Command Go account no_quota: account_id=%d err=%v", a.id, err))
		}
		return
	}
	if err := service.RecordCommandGoFailure(a.id, message); err != nil {
		common.SysError(fmt.Sprintf("failed to record Command Go account failure: account_id=%d err=%v", a.id, err))
	}
}
