// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package proxy 提供 v2rayN 兼容的订阅解析能力：将 base64 分享链接、Clash YAML、
// sing-box/v2ray JSON 等归一化为 Node 列表。http/https/socks5 可直接绑定；vmess/vless
// 等通过内置 Xray 中继转为本地 socks5 后再绑定渠道。
package proxy

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// Node 是归一化后的代理节点。
type Node struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // ss/trojan/vmess/vless/http/socks5/hysteria2/...
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Bindable bool   `json:"bindable"`              // 是否可用于创建代理规则
	ProxyURL string `json:"proxy_url,omitempty"`   // 直连类型时的代理地址
	ShareURL string `json:"share_url,omitempty"`   // 原始分享链接，供 Xray 中继使用
	Relay    bool   `json:"relay,omitempty"`       // true 表示通过本地 Xray 中继绑定
}

// IsDirectBindableType 是否可直接作为 HTTP 代理地址。
func IsDirectBindableType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "http", "https", "socks", "socks5":
		return true
	default:
		return false
	}
}

// IsXrayRelayType 是否可通过 Xray 中继绑定。
func IsXrayRelayType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "vmess", "vless", "trojan", "ss", "shadowsocks":
		return true
	default:
		return false
	}
}

// IsInfoNode 过滤订阅里的说明/流量行（常见 127.0.0.1 占位）。
func IsInfoNode(n Node) bool {
	host := strings.TrimSpace(strings.ToLower(n.Server))
	if host == "" {
		return true
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	// 畸形 IPv6（如 2401:f460:...:::443）不可 relay。
	if strings.Count(host, ":") >= 2 && strings.Contains(host, ":::") {
		return true
	}
	return false
}

// CanBindNode 判断节点能否用于创建规则。
func CanBindNode(n Node) bool {
	if IsInfoNode(n) {
		return false
	}
	if IsDirectBindableType(n.Type) {
		return n.Server != "" && n.Port > 0 && n.Port <= 65535
	}
	if IsXrayRelayType(n.Type) {
		return strings.TrimSpace(n.ShareURL) != "" && n.Server != "" && n.Port > 0 && n.Port <= 65535
	}
	return false
}

// NormalizeNodeToProxyURL 将直连节点转为网关可直接使用的代理 URL。
func NormalizeNodeToProxyURL(node Node) (string, error) {
	t := strings.ToLower(strings.TrimSpace(node.Type))
	switch t {
	case "http", "https", "socks", "socks5":
		if t == "socks" {
			t = "socks5"
		}
		server := strings.TrimSpace(node.Server)
		if server == "" || node.Port <= 0 || node.Port > 65535 {
			return "", errors.New("proxy node missing valid server:port")
		}
		return fmt.Sprintf("%s://%s:%d", t, server, node.Port), nil
	default:
		return "", fmt.Errorf("unsupported direct proxy node type: %s", node.Type)
	}
}
