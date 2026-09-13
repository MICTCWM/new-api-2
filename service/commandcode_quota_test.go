// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commandcodeMockUpstream 按参考项目 mock_server.py 的形状模拟上游 /alpha/* 端点。
func commandcodeMockUpstream(t *testing.T, creditsShape string) *httptest.Server {
	t.Helper()
	const nowMS = int64(1726000000000)
	mux := http.NewServeMux()
	mux.HandleFunc("/alpha/whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer sk-badkey" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"user":{"id":"u_123","name":"Max","userName":"maxeagle"},"org":{"id":"org_9"}}`))
	})
	mux.HandleFunc("/alpha/billing/credits", func(w http.ResponseWriter, r *http.Request) {
		if creditsShape == "garbage" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>503</html>"))
			return
		}
		if creditsShape == "partial" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		five := fmt.Sprintf(`{"used":1.23,"cap":5.0,"exceeded":false,"resetAt":%d}`, nowMS+3600*1000)
		exceeded := ""
		limited := ""
		if creditsShape == "exhausted" {
			five = fmt.Sprintf(`{"used":5.0,"cap":5.0,"exceeded":true,"resetAt":%d}`, nowMS+42*60*1000)
			exceeded = `"exceeded":"fiveHour",`
			limited = `"limited":true,`
		}
		if creditsShape == "snake" {
			five = fmt.Sprintf(`{"used":1.2,"cap":5.0,"exceeded":false,"reset_at":%d}`, nowMS+3600*1000)
		}
		weekly := fmt.Sprintf(`{"used":12.5,"cap":40.0,"exceeded":false,"resetAt":%d}`, nowMS+3*86400*1000)
		body := fmt.Sprintf(`{"credits":{"monthlyCredits":18.5,"purchasedCredits":0,"freeCredits":2.0,`+
			`"planId":"individual-goat","belowThreshold":false,"creditThreshold":1.0},`+
			`"windowLimits":{%s%s"fiveHour":%s,"weekly":%s}}`, limited, exceeded, five, weekly)
		_, _ = w.Write([]byte(body))
		if creditsShape == "snake" {
			_ = nowMS
		}
	})
	mux.HandleFunc("/alpha/billing/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		if creditsShape == "partial" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		assert.Equal(t, "org_9", r.URL.Query().Get("orgId"), "订阅端点应带 whoami 的 orgId")
		body := fmt.Sprintf(`{"success":true,"data":{"planId":"individual-goat","status":"active",`+
			`"currentPeriodStart":%d,"currentPeriodEnd":%d,"cancelAtPeriodEnd":false,"pendingPhase":null}}`,
			nowMS-18*86400*1000, nowMS+12*86400*1000)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/alpha/usage/summary", func(w http.ResponseWriter, r *http.Request) {
		if creditsShape == "partial" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{"totalCount":1883,"totalCost":6.001,"averageCost":0.00318,` +
			`"successRate":100,"completedCount":1883,"failedCount":0,` +
			`"totalTokensIn":487592979,"totalTokensOut":1210434,` +
			`"totalCredits":6.001,"periodBasis":"billing-period"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestFetchCommandcodeQuotaReport_NormalizesUpstream(t *testing.T) {
	server := commandcodeMockUpstream(t, "ok")
	report, err := FetchCommandcodeQuotaReport(context.Background(), server.Client(), server.URL, "sk-test")
	require.NoError(t, err)

	require.NotNil(t, report.Account)
	assert.Equal(t, "u_123", report.Account.ID)
	assert.Equal(t, "Max", report.Account.Name)
	assert.Equal(t, "maxeagle", report.Account.UserName)

	require.NotNil(t, report.Credits)
	require.NotNil(t, report.Credits.MonthlyCredits)
	assert.InDelta(t, 18.5, *report.Credits.MonthlyCredits, 1e-9)
	require.NotNil(t, report.Credits.FiveHour)
	assert.InDelta(t, 1.23, report.Credits.FiveHour.Used, 1e-9)
	assert.InDelta(t, 5.0, report.Credits.FiveHour.Cap, 1e-9)
	assert.False(t, report.Credits.FiveHour.Exceeded)
	require.NotNil(t, report.Credits.Weekly)
	assert.InDelta(t, 40.0, report.Credits.Weekly.Cap, 1e-9)
	assert.Empty(t, report.Credits.Exceeded)

	require.NotNil(t, report.Plan)
	assert.Equal(t, "individual-goat", report.Plan.PlanID)
	assert.Equal(t, "GOAT", report.Plan.Name)
	require.NotNil(t, report.Plan.MonthlyCredits)
	assert.InDelta(t, 70, *report.Plan.MonthlyCredits, 1e-9)
	assert.Equal(t, "active", report.Plan.Status)
	assert.NotZero(t, report.Plan.CurrentPeriodEnd)

	require.NotNil(t, report.Usage)
	assert.Equal(t, int64(1883), report.Usage.TotalCount)
	assert.Equal(t, "billing-period", report.Usage.PeriodBasis)
	assert.Empty(t, report.Failures)
}

// 蛇形命名的窗口字段与 reset_at 也要能解析（防御性兼容）。
func TestFetchCommandcodeQuotaReport_SnakeCaseWindow(t *testing.T) {
	server := commandcodeMockUpstream(t, "snake")
	report, err := FetchCommandcodeQuotaReport(context.Background(), server.Client(), server.URL, "sk-test")
	require.NoError(t, err)
	require.NotNil(t, report.Credits.FiveHour)
	assert.InDelta(t, 1.2, report.Credits.FiveHour.Used, 1e-9)
	assert.NotZero(t, report.Credits.FiveHour.ResetAt, "snake_case reset_at 应可解析")
}

// 非核心端点失败时整体仍返回可用数据，并在 Failures 记录。
func TestFetchCommandcodeQuotaReport_PartialFailureResilient(t *testing.T) {
	server := commandcodeMockUpstream(t, "partial")
	report, err := FetchCommandcodeQuotaReport(context.Background(), server.Client(), server.URL, "sk-test")
	require.NoError(t, err)
	assert.Nil(t, report.Credits, "credits 失败时不应有该区块")
	require.NotNil(t, report.Account, "whoami 成功的数据要保留")
	require.NotEmpty(t, report.Failures)
}

// whoami 返回 401 = 密钥无效，短路返回错误。
func TestFetchCommandcodeQuotaReport_RejectsInvalidKey(t *testing.T) {
	server := commandcodeMockUpstream(t, "ok")
	_, err := FetchCommandcodeQuotaReport(context.Background(), server.Client(), server.URL, "sk-badkey")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "密钥被拒")
}

func TestCommandcodePlanInfo(t *testing.T) {
	info, ok := commandcodePlanInfo("individual-goat")
	require.True(t, ok)
	assert.Equal(t, "GOAT", info.Name)
	assert.InDelta(t, 70, info.MonthlyCredits, 1e-9)

	// 最长前缀优先：individual-pro-v1 不能命中 individual-pro。
	info, ok = commandcodePlanInfo("individual-pro-v1")
	require.True(t, ok)
	assert.InDelta(t, 80, info.MonthlyCredits, 1e-9)

	info, ok = commandcodePlanInfo("teams-pro-annual")
	require.True(t, ok)
	assert.Equal(t, "Teams Pro", info.Name)

	_, ok = commandcodePlanInfo("unknown-plan")
	assert.False(t, ok)
}

func TestCommandcodeOriginFromBaseURL(t *testing.T) {
	assert.Equal(t, "https://api.commandcode.ai", CommandcodeOriginFromBaseURL("https://api.commandcode.ai/provider/v1"))
	assert.Equal(t, "https://api.commandcode.ai", CommandcodeOriginFromBaseURL(""))
	assert.Equal(t, "https://relay.example.com", CommandcodeOriginFromBaseURL("https://relay.example.com/provider/v1/"))
	assert.Equal(t, CommandcodeDefaultOrigin, CommandcodeOriginFromBaseURL("::::not-a-url"))
}
