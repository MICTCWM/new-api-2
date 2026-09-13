// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package operation_setting

import "github.com/QuantumNous/new-api/setting/proxy"

// 代理订阅与规则管理功能的 option key 与持久化数据模型。
// 存储模式与项目其它 operation_setting 一致：key-value 存于 options 表，运行时读写 common.OptionMap。
const (
	// ProxySubscriptionURLOptionKey 保存订阅地址。
	ProxySubscriptionURLOptionKey = "ProxySubscriptionURL"
	// ProxySubscriptionDataOptionKey 保存订阅解析后的节点缓存（JSON 形态 SubscriptionData）。
	ProxySubscriptionDataOptionKey = "ProxySubscriptionData"
	// ProxyRulesOptionKey 保存规则列表（JSON 形态 []Rule）。
	ProxyRulesOptionKey = "ProxyRules"
)

// SubscriptionData 是订阅缓存（节点列表+抓取时间），以 JSON 字符串存入 option。
type SubscriptionData struct {
	FetchedAt int64         `json:"fetched_at"`
	Nodes     []proxy.Node  `json:"nodes"`
}

// Rule 是一条代理规则：把选定渠道渠道的 ChannelSettings.Proxy 统一写入一个节点的 ProxyURL。
// 以 JSON 字符串存入 option（[]Rule）。
type Rule struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	ChannelIDs []int      `json:"channel_ids"`
	Node       proxy.Node `json:"node"`
	ProxyURL   string     `json:"proxy_url"`
	CreatedAt  int64      `json:"created_at"`
}