// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strings"
	"unicode"
)

// v2rayN 订阅解析策略参考 ServiceLib.Handler.ConfigHandler.AddBatchServers：
// 1. 整段 base64 → 分享链接行
// 2. 明文分享链接行
// 3. 再次 base64 尝试
// 4. Clash YAML proxies
// 5. sing-box / v2ray JSON outbounds（仅提取可识别节点）

const (
	// V2rayNSubscriptionUserAgent 与 v2rayN 订阅拉取行为对齐，部分面板会校验 UA。
	V2rayNSubscriptionUserAgent = "v2rayN"
)

// isProbablyBase64 判断内容是否像 v2rayN 常见的整段 base64 订阅。
func isProbablyBase64(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" || strings.Contains(content, "://") {
		return false
	}
	for _, r := range content {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '+', r == '/', r == '=', r == '-', r == '_':
		default:
			return false
		}
	}
	_, err := decodeBase64(content)
	return err == nil
}

func decodeShareLines(content string) []Node {
	if decoded, err := decodeBase64(content); err == nil {
		if nodes := parseShareLines(string(decoded)); len(nodes) > 0 {
			return nodes
		}
	}
	return parseShareLines(content)
}

// parseV2rayNSubscription 按 v2rayN 顺序解析订阅内容。
func parseV2rayNSubscription(raw []byte) []Node {
	content := strings.TrimSpace(string(raw))
	if content == "" {
		return nil
	}

	if isProbablyBase64(content) {
		if nodes := decodeShareLines(content); len(nodes) > 0 {
			return nodes
		}
	}
	if nodes := parseShareLines(content); len(nodes) > 0 {
		return nodes
	}
	if isProbablyBase64(content) {
		if nodes := decodeShareLines(content); len(nodes) > 0 {
			return nodes
		}
	}
	if nodes := parseClashYAML(raw); len(nodes) > 0 {
		return nodes
	}
	if nodes := parseSingboxOrV2rayJSON(raw); len(nodes) > 0 {
		return nodes
	}
	return nil
}

// parseShareLines 解析 v2rayN 分享链接行（vmess/ss/trojan/vless/hy2/...）。
func parseShareLines(content string) []Node {
	var out []Node
	seen := make(map[string]bool)
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		n := parseShareLine(line)
		if n == nil {
			continue
		}
		key := nodeKey(*n)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, *n)
	}
	return out
}

// parseShareLine 识别 v2rayN FmtHandler.ResolveConfig 支持的分享链接前缀。
func parseShareLine(line string) *Node {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	var n *Node
	switch {
	case strings.HasPrefix(line, "vmess://"):
		n = parseVmess(line)
	case strings.HasPrefix(line, "ss://"):
		n = parseSS(line)
	case strings.HasPrefix(line, "trojan://"):
		n = parseURLNode(line, "trojan")
	case strings.HasPrefix(line, "vless://"):
		n = parseURLNode(line, "vless")
	case strings.HasPrefix(line, "http://"):
		n = parseURLNode(line, "http")
	case strings.HasPrefix(line, "https://"):
		n = parseURLNode(line, "https")
	case strings.HasPrefix(line, "socks5://"):
		n = parseURLNode(line, "socks5")
	case strings.HasPrefix(line, "socks://"):
		n = parseURLNode(line, "socks")
	case strings.HasPrefix(line, "socks4://"):
		n = parseURLNode(strings.Replace(line, "socks4://", "socks5://", 1), "socks5")
	case strings.HasPrefix(line, "hy2://"), strings.HasPrefix(line, "hysteria2://"):
		n = parseURLNode(line, "hysteria2")
	case strings.HasPrefix(line, "hysteria2+realm://"), strings.HasPrefix(line, "hysteria2+realm+http://"):
		n = parseCompoundSchemeURL(line, "hysteria2")
	case strings.HasPrefix(line, "tuic://"):
		n = parseURLNode(line, "tuic")
	case strings.HasPrefix(line, "wireguard://"):
		n = parseURLNode(line, "wireguard")
	case strings.HasPrefix(line, "anytls://"):
		n = parseURLNode(line, "anytls")
	case strings.HasPrefix(line, "naive+https://"), strings.HasPrefix(line, "naive+quic://"):
		n = parseCompoundSchemeURL(line, "naive")
	case strings.HasPrefix(line, "naive://"):
		n = parseURLNode(line, "naive")
	default:
		return nil
	}
	if n == nil {
		return nil
	}
	return finalizeNode(n, line)
}

// parseCompoundSchemeURL 将 hysteria2+realm://、naive+https:// 等复合 scheme 归一化为 url.Parse 可识别形式。
func parseCompoundSchemeURL(line, nodeType string) *Node {
	idx := strings.Index(line, "://")
	if idx < 0 {
		return nil
	}
	normalized := "https://" + line[idx+3:]
	return parseURLNode(normalized, nodeType)
}

// isShareLine 判断一行是否像 v2rayN 分享链接（用于 JSON 提取等场景）。
func isShareLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
		// 订阅里的 http(s) 行应为分享链接而非订阅 URL；要求带 # 或 @ 视为节点。
		return strings.Contains(line, "@") || strings.Contains(line, "#")
	}
	for _, prefix := range []string{
		"vmess://", "ss://", "trojan://", "vless://", "socks5://", "socks://", "socks4://",
		"hy2://", "hysteria2://", "hysteria2+realm://", "hysteria2+realm+http://",
		"tuic://", "wireguard://", "anytls://", "naive://", "naive+https://", "naive+quic://",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// trimJSONNoise 去掉 JSON 文本两侧空白与 BOM。
func trimJSONNoise(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	s = strings.TrimPrefix(s, "\uFEFF")
	return s
}

func looksLikeJSON(content string) bool {
	if content == "" {
		return false
	}
	r := rune(content[0])
	return r == '{' || r == '[' || unicode.IsSpace(r) && strings.Contains(content, "{")
}
