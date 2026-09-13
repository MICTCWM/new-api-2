// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

// decodeBase64 宽松解码 base64：依次尝试标准/Padding 缺失/URL-safe 变体，
// 均失败返回 error。
func decodeBase64(s string) ([]byte, error) {
	variants := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range variants {
		if data, err := enc.DecodeString(strings.TrimSpace(s)); err == nil {
			return data, nil
		}
	}
	return nil, errors.New("invalid base64")
}

// tryDecodeUTF8 尝试 base64 解码，成功返回解码字符串，失败原样返回原文。
func tryDecodeUTF8(s string) string {
	data, err := decodeBase64(s)
	if err != nil {
		return s
	}
	// 解码结果需为合法 UTF-8 文本才采用，避免把明文误当 base64。
	if !isValidUTF8(data) {
		return s
	}
	return string(data)
}

func isValidUTF8(b []byte) bool {
	return utf8.Valid(b)
}

// parseVmess 解析 vmess://，整行 base64 解码为 JSON（{ps, add, port}）。
func parseVmess(line string) *Node {
	rest := strings.TrimPrefix(line, "vmess://")
	data, err := decodeBase64(rest)
	if err != nil {
		return nil
	}
	var obj map[string]interface{}
	if err := common.Unmarshal(data, &obj); err != nil {
		return nil
	}
	add, _ := obj["add"].(string)
	ps, _ := obj["ps"].(string)
	port, ok := anyToPort(obj["port"])
	if !ok || strings.TrimSpace(add) == "" {
		return nil
	}
	return &Node{Name: ps, Type: "vmess", Server: add, Port: port}
}

// parseSS 解析 ss://，可能为 base64(userinfo@host:port) 或明文形式的
// method:password@host:port，后接 #fragment。
func parseSS(line string) *Node {
	rest := strings.TrimPrefix(line, "ss://")
	fragment := ""
	if idx := strings.IndexByte(rest, '#'); idx >= 0 {
		fragment = rest[idx+1:]
		rest = rest[:idx]
	}

	var hostport string
	if atIdx := strings.LastIndexByte(rest, '@'); atIdx >= 0 {
		hostport = rest[atIdx+1:]
	} else {
		// 不带 @，尝试 base64 解码整段得到 userinfo@host:port。
		decoded, err := decodeBase64(rest)
		if err != nil {
			return nil
		}
		plain := string(decoded)
		atIdx := strings.LastIndexByte(plain, '@')
		if atIdx < 0 {
			return nil
		}
		hostport = plain[atIdx+1:]
	}

	server, port, ok := splitHostPort(hostport)
	if !ok {
		return nil
	}

	// fragment 可能是明文或 base64 编码的节点名。
	name := ""
	if fragment != "" {
		name = tryDecodeUTF8(fragment)
	}

	return &Node{Name: name, Type: "ss", Server: server, Port: port}
}

// parseURLNode 解析 trojan/vless/http/https/socks/socks5 等标准 scheme:// 形式。
func parseURLNode(line, ptype string) *Node {
	u, err := url.Parse(line)
	if err != nil {
		return nil
	}
	server := u.Hostname()
	portStr := u.Port()
	if portStr == "" {
		// 无端口时对常见协议使用默认端口。
		switch strings.ToLower(u.Scheme) {
		case "http":
			portStr = "80"
		case "https":
			portStr = "443"
		case "socks", "socks5":
			portStr = "1080"
		}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 || strings.TrimSpace(server) == "" {
		return nil
	}

	name := u.Fragment
	if strings.TrimSpace(name) != "" {
		// fragment 可能是明文或 base64 编码。
		name = tryDecodeUTF8(name)
	}

	t := ptype
	if strings.EqualFold(t, "socks") {
		t = "socks5"
	}
	return &Node{Name: name, Type: t, Server: server, Port: port}
}

// splitHostPort 解析 host:port，要求 port 为合法 1-65535 端口。
func splitHostPort(hostport string) (string, int, bool) {
	if strings.TrimSpace(hostport) == "" {
		return "", 0, false
	}
	// 兼容 IPv6 的 [::1]:port。
	if strings.HasPrefix(hostport, "[") {
		if idx := strings.LastIndexByte(hostport, ']'); idx >= 0 && idx+1 < len(hostport) && hostport[idx+1] == ':' {
			host := hostport[1:idx]
			portStr := hostport[idx+2:]
			port, err := strconv.Atoi(portStr)
			if err != nil || port <= 0 || port > 65535 || host == "" {
				return "", 0, false
			}
			return host, port, true
		}
		return "", 0, false
	}
	idx := strings.LastIndexByte(hostport, ':')
	if idx < 0 {
		return "", 0, false
	}
	host := hostport[:idx]
	portStr := hostport[idx+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 || strings.TrimSpace(host) == "" {
		return "", 0, false
	}
	return host, port, true
}

// anyToPort 将 JSON 中的 port 字段（数字或字符串）转为端口；无效返回 !ok。
func anyToPort(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		p := int(n)
		return p, p > 0 && p <= 65535
	case string:
		p, err := strconv.Atoi(strings.TrimSpace(n))
		return p, err == nil && p > 0 && p <= 65535
	default:
		return 0, false
	}
}
