// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	proxypkg "github.com/QuantumNous/new-api/setting/proxy"

	"github.com/gin-gonic/gin"
)

type proxySubscriptionRequest struct {
	URL string `json:"url"`
}

type proxyRuleRequest struct {
	Name       string     `json:"name"`
	ChannelIDs []int      `json:"channel_ids"`
	Node       proxypkg.Node `json:"node"`
}

// GetProxyStatus 返回订阅信息、节点缓存与规则列表。
func GetProxyStatus(c *gin.Context) {
	data := service.LoadProxySubscriptionData()
	common.ApiSuccess(c, gin.H{
		"subscription_url":        service.GetProxySubscriptionURL(),
		"subscription_fetched_at": data.FetchedAt,
		"nodes":                   data.Nodes,
		"rules":                   service.LoadProxyRules(),
	})
}

// UpdateProxySubscription 保存订阅地址并拉取、解析、缓存节点。
func UpdateProxySubscription(c *gin.Context) {
	var req proxySubscriptionRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "invalid request body")
		return
	}
	nodes, err := service.SaveProxySubscription(c.Request.Context(), req.URL)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	bindable := countBindableNodes(nodes)
	common.ApiSuccess(c, gin.H{
		"node_count":      len(nodes),
		"bindable_count":  bindable,
		"nodes":           nodes,
	})
}

// RefreshProxySubscription 用已保存的订阅地址重新拉取并覆盖节点缓存（规则保持不变）。
func RefreshProxySubscription(c *gin.Context) {
	nodes, err := service.RefreshProxySubscription(c.Request.Context())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	bindable := countBindableNodes(nodes)
	common.ApiSuccess(c, gin.H{
		"node_count":     len(nodes),
		"bindable_count": bindable,
		"nodes":          nodes,
	})
}

// ClearProxySubscription 清空订阅地址与节点缓存（规则保留）。
func ClearProxySubscription(c *gin.Context) {
	if err := service.ClearProxySubscription(); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

// CreateProxyRule 创建规则并把节点代理批量写入各渠道。
func CreateProxyRule(c *gin.Context) {
	var req proxyRuleRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "invalid request body")
		return
	}
	rule, err := service.CreateProxyRule(c.Request.Context(), req.Name, req.ChannelIDs, req.Node)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"rule": rule})
}

// DeleteProxyRule 删除规则并清空匹配渠道的代理。
func DeleteProxyRule(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if err := service.DeleteProxyRule(c.Request.Context(), id); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func countBindableNodes(nodes []proxypkg.Node) int {
	n := 0
	for _, node := range nodes {
		if node.Bindable {
			n++
		}
	}
	return n
}