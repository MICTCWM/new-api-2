// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// parseSingboxOrV2rayJSON 从 sing-box / v2ray 完整配置 JSON 中提取 outbounds 节点。
// 对齐 v2rayN AddBatchServers4Custom 的常见订阅形态（仅提取 server/port/type/name）。
func parseSingboxOrV2rayJSON(raw []byte) []Node {
	content := trimJSONNoise(raw)
	if !looksLikeJSON(content) {
		return nil
	}

	var doc map[string]interface{}
	if err := common.UnmarshalJsonStr(content, &doc); err != nil {
		return nil
	}

	outbounds, ok := doc["outbounds"].([]interface{})
	if !ok || len(outbounds) == 0 {
		return nil
	}

	var out []Node
	seen := make(map[string]bool)
	for _, item := range outbounds {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		n := jsonOutboundToNode(m)
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

func jsonOutboundToNode(m map[string]interface{}) *Node {
	rawType := strings.ToLower(strings.TrimSpace(str(m["type"])))
	if rawType == "" {
		rawType = strings.ToLower(strings.TrimSpace(str(m["protocol"])))
	}
	if rawType == "" || rawType == "direct" || rawType == "block" || rawType == "dns" {
		return nil
	}

	nodeType := rawType
	switch rawType {
	case "shadowsocks":
		nodeType = "ss"
	case "socks":
		nodeType = "socks5"
	}

	server := strings.TrimSpace(str(m["server"]))
	if server == "" {
		server = strings.TrimSpace(str(m["address"]))
	}
	port, ok := jsonPort(m["server_port"])
	if !ok {
		port, ok = jsonPort(m["port"])
	}
	if !ok || server == "" {
		return nil
	}

	name := str(m["tag"])
	if name == "" {
		name = str(m["name"])
	}
	return finalizeNode(&Node{Name: name, Type: nodeType, Server: server, Port: port}, "")
}

func jsonPort(v interface{}) (int, bool) {
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
