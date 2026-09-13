// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package controller

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// GetCommandcodeChannelQuota 查询 Command 渠道对应 commandcode.ai 账号的额度。
// 数据来自上游 /alpha/* 端点（whoami / billing/credits / billing/subscriptions
// / usage/summary），归一化后返回，供管理面板展示三条额度进度条、信用余额、
// 套餐与账期累计。
func GetCommandcodeChannelQuota(c *gin.Context) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return
	}

	ch, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if ch == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel not found"})
		return
	}
	if ch.Type != constant.ChannelTypeCommand {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel type is not Command"})
		return
	}
	if ch.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "multi-key channel is not supported"})
		return
	}

	apiKey := strings.TrimSpace(ch.Key)
	if apiKey == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "command channel: api key is required"})
		return
	}

	client, err := service.GetHttpClientWithProxy(ch.GetSetting().Proxy)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	origin := service.CommandcodeOriginFromBaseURL(ch.GetBaseURL())
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	report, err := service.FetchCommandcodeQuotaReport(ctx, client, origin, apiKey)
	if err != nil {
		common.SysError("failed to fetch commandcode quota: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	// 每次额度查询即一次校准：小时/周/月窗口以上游为准（尽力而为，
	// 校准失败不影响本次查询结果）。
	if _, calibErr := service.CalibrateCommandChannelWithClient(c.Request.Context(), ch, client, time.Now().UTC(), true); calibErr != nil {
		common.SysError("command usage calibration failed: " + calibErr.Error())
	}
	common.ApiSuccess(c, report)
}
