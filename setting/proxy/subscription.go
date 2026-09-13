// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ParseSubscription 按 v2rayN 订阅规则解析内容（base64 分享链接 / Clash / sing-box|v2ray JSON）。
// raw 为订阅响应体；url 仅用于 error 消息与日志（sanitize 后不含 query）。
func ParseSubscription(raw []byte, rawURL string) ([]Node, error) {
	content := strings.TrimSpace(string(raw))
	if content == "" {
		return nil, errors.New("empty subscription")
	}

	if nodes := parseV2rayNSubscription(raw); len(nodes) > 0 {
		return nodes, nil
	}

	return nil, fmt.Errorf("failed to parse v2rayN subscription: %s", sanitizeURL(rawURL))
}

// finalizeNode 统一填充 Bindable、ProxyURL、ShareURL、Relay。
func finalizeNode(n *Node, shareURL string) *Node {
	n.ShareURL = strings.TrimSpace(shareURL)
	n.Bindable = CanBindNode(*n)
	n.Relay = n.Bindable && IsXrayRelayType(n.Type)
	if n.Bindable && IsDirectBindableType(n.Type) {
		if u, err := NormalizeNodeToProxyURL(*n); err == nil {
			n.ProxyURL = u
		} else {
			n.Bindable = false
			n.Relay = false
		}
	}
	return n
}

// nodeKey 用于去重的键。
func nodeKey(n Node) string {
	return fmt.Sprintf("%s|%d|%s|%s", n.Server, n.Port, n.Type, n.Name)
}

// sanitizeURL 返回 URL 的 scheme://host+path（去掉 query/token 等敏感参数）。
// 解析失败时退化为去掉问号后内容。
func sanitizeURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		s := u.Scheme + "://" + u.Host + u.Path
		return s
	}
	if idx := strings.IndexByte(rawURL, '?'); idx >= 0 {
		return rawURL[:idx]
	}
	return rawURL
}
