// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Command Code（commandcode.ai）账号额度查询。
// 上游为未公开文档的 /alpha/* 端点，解析层做了防御性兼容（字段命名
// camelCase/snake_case、时间戳 epoch 毫秒/秒/ISO、信封包裹 data 等），
// 归一化逻辑对齐社区项目 MAXeaglet/commandcode-usage（MIT）。
// 端点（均为 GET + Authorization: Bearer <渠道密钥>）：
//
//	/alpha/whoami                 → 账户身份、orgId
//	/alpha/billing/credits        → 信用余额 + 5h/周滚动窗口 + 限流状态
//	/alpha/billing/subscriptions  → 套餐与账期（?orgId= 取自 whoami）
//	/alpha/usage/summary          → 账期累计聚合

const (
	CommandcodeDefaultOrigin = "https://api.commandcode.ai"
	commandcodeFetchTimeout  = 15 * time.Second
	commandcodeUserAgent     = "new-api-commandcode-quota/1.0"
)

// commandcodePlan 描述一个已知套餐的月度信用额（来自社区对官方 CLI 的逆向，
// 官方未文档化；未知套餐月度额度按 null 处理，界面自动降级为只显示余额）。
type commandcodePlan struct {
	Name           string
	MonthlyCredits float64
}

var commandcodeKnownPlans = map[string]commandcodePlan{
	"individual-go":       {Name: "Go", MonthlyCredits: 10},
	"individual-goat":     {Name: "GOAT", MonthlyCredits: 70},
	"individual-pro":      {Name: "Pro", MonthlyCredits: 30},
	"individual-pro-v1":   {Name: "Pro", MonthlyCredits: 80},
	"individual-provider": {Name: "Provider", MonthlyCredits: 15},
	"individual-max":      {Name: "Max", MonthlyCredits: 150},
	"individual-ultra":    {Name: "Ultra", MonthlyCredits: 300},
	"teams-pro":           {Name: "Teams Pro", MonthlyCredits: 40},
}

// commandcodePlanPrefixes 最长前缀优先：individual-pro-v1 应胜过 individual-pro。
var commandcodePlanPrefixes = func() []string {
	prefixes := make([]string, 0, len(commandcodeKnownPlans))
	for p := range commandcodeKnownPlans {
		prefixes = append(prefixes, p)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		if len(prefixes[i]) != len(prefixes[j]) {
			return len(prefixes[i]) > len(prefixes[j])
		}
		return prefixes[i] < prefixes[j]
	})
	return prefixes
}()

func commandcodePlanInfo(planID string) (commandcodePlan, bool) {
	norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(planID), "_", "-"))
	for _, prefix := range commandcodePlanPrefixes {
		if strings.HasPrefix(norm, prefix) {
			return commandcodeKnownPlans[prefix], true
		}
	}
	return commandcodePlan{}, false
}

// CommandcodeWindow 是一条滚动额度窗口（服务端原始 used/cap）。
type CommandcodeWindow struct {
	Used     float64 `json:"used"`
	Cap      float64 `json:"cap"`
	Exceeded bool    `json:"exceeded"`
	ResetAt  int64   `json:"reset_at"` // epoch 毫秒，0 表示未知
}

// CommandcodeCredits 是 billing/credits 的归一化结果。
type CommandcodeCredits struct {
	MonthlyCredits   *float64           `json:"monthly_credits"`
	PurchasedCredits *float64           `json:"purchased_credits"`
	FreeCredits      *float64           `json:"free_credits"`
	Limited          bool               `json:"limited"`
	Exceeded         string             `json:"exceeded"` // 服务端指出的拦截窗口："fiveHour"/"weekly"/""
	BelowThreshold   bool               `json:"below_threshold"`
	CreditThreshold  *float64           `json:"credit_threshold"`
	FiveHour         *CommandcodeWindow `json:"five_hour,omitempty"`
	Weekly           *CommandcodeWindow `json:"weekly,omitempty"`
}

// CommandcodePlan 是 billing/subscriptions 的归一化结果。
type CommandcodePlan struct {
	PlanID             string          `json:"plan_id"`
	Name               string          `json:"name"`
	Status             string          `json:"status"`
	MonthlyCredits     *float64        `json:"monthly_credits"` // 已知套餐才有，未知套餐为 null
	CurrentPeriodStart int64           `json:"current_period_start"`
	CurrentPeriodEnd   int64           `json:"current_period_end"`
	CancelAtPeriodEnd  bool            `json:"cancel_at_period_end"`
	PendingPhase       json.RawMessage `json:"pending_phase,omitempty"`
}

// CommandcodeUsageSummary 是 usage/summary 的归一化结果（账期累计聚合）。
type CommandcodeUsageSummary struct {
	TotalCount     int64    `json:"total_count"`
	TotalCost      float64  `json:"total_cost"`
	AverageCost    *float64 `json:"average_cost"`
	SuccessRate    float64  `json:"success_rate"`
	CompletedCount int64    `json:"completed_count"`
	FailedCount    int64    `json:"failed_count"`
	TotalTokensIn  float64  `json:"total_tokens_in"`
	TotalTokensOut float64  `json:"total_tokens_out"`
	TotalCredits   float64  `json:"total_credits"`
	PeriodBasis    string   `json:"period_basis"`
}

// CommandcodeQuotaReport 是一次额度查询的聚合结果。
type CommandcodeQuotaReport struct {
	Account  *CommandcodeAccount      `json:"account,omitempty"`
	Credits  *CommandcodeCredits      `json:"credits,omitempty"`
	Plan     *CommandcodePlan         `json:"plan,omitempty"`
	Usage    *CommandcodeUsageSummary `json:"usage,omitempty"`
	Failures []string                 `json:"failures,omitempty"`
}

// CommandcodeAccount 是 whoami 的归一化结果。
type CommandcodeAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	UserName string `json:"user_name"`
}

type commandcodeHTTPIface interface {
	Do(req *http.Request) (*http.Response, error)
}

// FetchCommandcodeQuotaReport 查询 Command Code 账号额度。
// origin 是上游站点根（如 https://api.commandcode.ai），由控制器从渠道
// BaseURL 推导；apiKey 使用渠道密钥。任一端点失败不中断整体，仅在
// Failures 中记录；全部失败才返回错误。
func FetchCommandcodeQuotaReport(ctx context.Context, client commandcodeHTTPIface, origin, apiKey string) (*CommandcodeQuotaReport, error) {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return nil, fmt.Errorf("commandcode origin is required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("commandcode api key is required")
	}
	if client == nil {
		client = http.DefaultClient
	}

	report := &CommandcodeQuotaReport{}
	failures := make([]string, 0, 4)
	fail := func(section, format string, args ...interface{}) {
		failures = append(failures, section+": "+fmt.Sprintf(format, args...))
	}

	// 1) whoami：账户身份 + orgId。401/403 在此短路 = 密钥无效。
	orgID := ""
	body, status, err := commandcodeGet(ctx, client, origin, "/alpha/whoami", apiKey)
	if err != nil {
		fail("whoami", "%v", err)
	} else if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, fmt.Errorf("密钥被拒（HTTP %d），请确认渠道密钥为有效的 commandcode.ai 密钥", status)
	} else if status >= 300 {
		fail("whoami", "HTTP %d", status)
	} else {
		var payload struct {
			User map[string]interface{} `json:"user"`
			Org  map[string]interface{} `json:"org"`
			Data struct {
				User map[string]interface{} `json:"user"`
				Org  map[string]interface{} `json:"org"`
			} `json:"data"`
		}
		if err := common.Unmarshal(body, &payload); err == nil {
			user := payload.User
			org := payload.Org
			if user == nil {
				user = payload.Data.User
				org = payload.Data.Org
			}
			if user != nil {
				report.Account = &CommandcodeAccount{
					ID:       commandcodeStr(user["id"]),
					Name:     commandcodeStr(user["name"]),
					UserName: commandcodeFirstStr(user["userName"], user["username"]),
				}
			}
			if org != nil {
				orgID = commandcodeStr(org["id"])
			}
		} else {
			fail("whoami", "%v", err)
		}
	}

	// 2) billing/credits：余额 + 5h/周窗口（核心数据）。
	planIDFallback := ""
	body, status, err = commandcodeGet(ctx, client, origin, "/alpha/billing/credits", apiKey)
	if err != nil {
		fail("billing/credits", "%v", err)
	} else if status >= 300 {
		fail("billing/credits", "HTTP %d", status)
	} else {
		var payload map[string]interface{}
		if err := common.Unmarshal(body, &payload); err == nil {
			credits, _ := payload["credits"].(map[string]interface{})
			wl, _ := payload["windowLimits"].(map[string]interface{})
			if credits == nil || wl == nil {
				if data, ok := payload["data"].(map[string]interface{}); ok {
					if credits == nil {
						credits, _ = data["credits"].(map[string]interface{})
					}
					if wl == nil {
						wl, _ = data["windowLimits"].(map[string]interface{})
					}
				}
			}
			if credits != nil || wl != nil {
				creditsReport := &CommandcodeCredits{
					MonthlyCredits:   commandcodeNum(credits["monthlyCredits"], credits["monthly_credits"]),
					PurchasedCredits: commandcodeNum(credits["purchasedCredits"], credits["purchased_credits"]),
					FreeCredits:      commandcodeNum(credits["freeCredits"], credits["free_credits"]),
					Limited:          commandcodeBool(wl["limited"]),
					Exceeded:         commandcodeStr(wl["exceeded"]),
					BelowThreshold:   commandcodeBool(credits["belowThreshold"]),
					CreditThreshold:  commandcodeNum(credits["creditThreshold"]),
				}
				creditsReport.FiveHour = commandcodeNormalizeWindow(commandcodePickWindow(wl, "fiveHour", "five_hour", "rolling5h", "5h"))
				creditsReport.Weekly = commandcodeNormalizeWindow(commandcodePickWindow(wl, "weekly", "week"))
				report.Credits = creditsReport
				planIDFallback = commandcodeFirstStr(credits["planId"], credits["plan_id"])
			}
		} else {
			fail("billing/credits", "%v", err)
		}
	}

	// 3) billing/subscriptions：套餐与账期（失败时用 credits 里的 planId 兜底）。
	subPath := "/alpha/billing/subscriptions"
	if orgID != "" {
		subPath += "?orgId=" + url.QueryEscape(orgID)
	}
	body, status, err = commandcodeGet(ctx, client, origin, subPath, apiKey)
	subscriptionOK := false
	var subData map[string]interface{}
	if err != nil {
		fail("billing/subscriptions", "%v", err)
	} else if status >= 300 {
		fail("billing/subscriptions", "HTTP %d", status)
	} else {
		var payload struct {
			Data         map[string]interface{} `json:"data"`
			Subscription map[string]interface{} `json:"subscription"`
		}
		if err := common.Unmarshal(body, &payload); err == nil {
			subData = payload.Data
			if subData == nil {
				subData = payload.Subscription
			}
			subscriptionOK = true
		} else {
			fail("billing/subscriptions", "%v", err)
		}
	}
	planID := ""
	if subscriptionOK {
		planID = commandcodeFirstStr(subData["planId"], subData["plan_id"])
	}
	if planID == "" {
		planID = planIDFallback
	}
	if subData != nil || planID != "" {
		info, known := commandcodePlanInfo(planID)
		plan := &CommandcodePlan{
			PlanID:             planID,
			Name:               planID,
			Status:             commandcodeStr(subData["status"]),
			CurrentPeriodStart: commandcodeEpochMs(subData["currentPeriodStart"], subData["current_period_start"]),
			CurrentPeriodEnd:   commandcodeEpochMs(subData["currentPeriodEnd"], subData["current_period_end"]),
			CancelAtPeriodEnd:  commandcodeBool(subData["cancelAtPeriodEnd"]),
		}
		if raw, hasPending := subData["pendingPhase"]; hasPending && raw != nil {
			if rawJSON, err := common.Marshal(raw); err == nil {
				plan.PendingPhase = rawJSON
			}
		}
		if known {
			plan.Name = info.Name
			monthly := info.MonthlyCredits
			plan.MonthlyCredits = &monthly
		}
		if subData == nil && planID == "" {
			plan = nil
		}
		if plan != nil {
			report.Plan = plan
		}
	}

	// 4) usage/summary：账期累计聚合。
	body, status, err = commandcodeGet(ctx, client, origin, "/alpha/usage/summary", apiKey)
	if err != nil {
		fail("usage/summary", "%v", err)
	} else if status >= 300 {
		fail("usage/summary", "HTTP %d", status)
	} else {
		var payload struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := common.Unmarshal(body, &payload); err == nil {
			u := payload.Data
			if u == nil {
				var direct map[string]interface{}
				_ = common.Unmarshal(body, &direct)
				u = direct
			}
			if u != nil {
				report.Usage = &CommandcodeUsageSummary{
					TotalCount:     int64(commandcodeFloat(u["totalCount"])),
					TotalCost:      commandcodeFloat(u["totalCost"]),
					AverageCost:    commandcodeNum(u["averageCost"]),
					SuccessRate:    commandcodeFloat(u["successRate"]),
					CompletedCount: int64(commandcodeFloat(u["completedCount"])),
					FailedCount:    int64(commandcodeFloat(u["failedCount"])),
					TotalTokensIn:  commandcodeFloat(u["totalTokensIn"]),
					TotalTokensOut: commandcodeFloat(u["totalTokensOut"]),
					TotalCredits:   commandcodeFloat(u["totalCredits"]),
					PeriodBasis:    commandcodeStr(u["periodBasis"]),
				}
			}
		} else {
			fail("usage/summary", "%v", err)
		}
	}

	if len(failures) > 0 {
		report.Failures = failures
	}
	if report.Account == nil && report.Credits == nil && report.Plan == nil {
		return nil, fmt.Errorf("所有 commandcode.ai 端点均无法访问：%s", strings.Join(failures, "; "))
	}
	return report, nil
}

// commandcodeGet 发起一次上游 GET；返回响应体与状态码。
func commandcodeGet(ctx context.Context, client commandcodeHTTPIface, origin, path, apiKey string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, commandcodeFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", commandcodeUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func commandcodePickWindow(wl map[string]interface{}, names ...string) map[string]interface{} {
	for _, name := range names {
		if w, ok := wl[name].(map[string]interface{}); ok {
			return w
		}
	}
	return nil
}

// commandcodeNormalizeWindow 防御性归一窗口对象：兼容 used/usage/usedCredits、
// cap/limit/capCredits、exceeded 缺省时用 used>=cap 推导。
func commandcodeNormalizeWindow(raw map[string]interface{}) *CommandcodeWindow {
	if raw == nil {
		return nil
	}
	used := commandcodeFirstFloat(raw["used"], raw["usage"], raw["usedCredits"], raw["used_credits"])
	cap := commandcodeFirstFloat(raw["cap"], raw["limit"], raw["capCredits"])
	exceeded := commandcodeBool(raw["exceeded"])
	if !exceeded && cap > 0 && used >= cap {
		exceeded = true
	}
	return &CommandcodeWindow{
		Used:     used,
		Cap:      cap,
		Exceeded: exceeded,
		ResetAt:  commandcodeEpochMs(raw["resetAt"], raw["reset_at"], raw["resetsAt"]),
	}
}

var commandcodeNumberPattern = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)

func commandcodeFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		trimmed := strings.TrimSpace(n)
		if commandcodeNumberPattern.MatchString(trimmed) {
			if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
				return f
			}
		}
	}
	return 0
}

func commandcodeNum(values ...interface{}) *float64 {
	for _, v := range values {
		switch n := v.(type) {
		case float64:
			if !isNaN(n) {
				out := n
				return &out
			}
		}
	}
	return nil
}

func isNaN(f float64) bool { return f != f }

func commandcodeFirstFloat(values ...interface{}) float64 {
	for _, v := range values {
		switch n := v.(type) {
		case float64:
			return n
		}
	}
	return 0
}

func commandcodeStr(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func commandcodeFirstStr(values ...interface{}) string {
	for _, v := range values {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func commandcodeBool(v interface{}) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true")
	}
	return false
}

// commandcodeEpochMs 兼容 epoch 毫秒/秒与 ISO 字符串，返回 epoch 毫秒。
func commandcodeEpochMs(values ...interface{}) int64 {
	for _, v := range values {
		switch t := v.(type) {
		case float64:
			if t <= 0 {
				continue
			}
			if t < 1e12 {
				return int64(t * 1000)
			}
			return int64(t)
		case string:
			if trimmed := strings.TrimSpace(t); trimmed != "" {
				if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
					return parsed.UnixMilli()
				}
			}
		}
	}
	return 0
}

// CommandcodeOriginFromBaseURL 从渠道 BaseURL 推导上游站点根。
// 渠道默认 BaseURL 为 https://api.commandcode.ai/provider/v1，而额度端点
// 挂在站点根的 /alpha/* 下，因此只保留 scheme://host；解析失败时回退默认。
func CommandcodeOriginFromBaseURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return CommandcodeDefaultOrigin
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return CommandcodeDefaultOrigin
	}
	return parsed.Scheme + "://" + parsed.Host
}
