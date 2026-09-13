// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// parseClashYAML 解析 clash yaml 订阅，取顶层 proxies 列表（list of map）。
func parseClashYAML(raw []byte) []Node {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	rawList, ok := doc["proxies"]
	if !ok {
		return nil
	}
	list, ok := rawList.([]interface{})
	if !ok {
		return nil
	}

	var out []Node
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		n := clashNodeToNode(m)
		if n != nil {
			out = append(out, *n)
		}
	}
	return out
}

// clashNodeToNode 将单个 clash proxy item 的 map 转为 Node。
func clashNodeToNode(m map[string]interface{}) *Node {
	ptype := strings.ToLower(strings.TrimSpace(str(m["type"])))
	if ptype == "" || ptype == "direct" {
		return nil
	}
	server := strings.TrimSpace(str(m["server"]))
	port, ok := yamlPort(m["port"])
	if !ok || strings.TrimSpace(server) == "" {
		return nil
	}

	// ss-rust 归一到 ss。
	if ptype == "ss-rust" {
		ptype = "ss"
	}

	name := tryDecodeUTF8(str(m["name"]))
	return finalizeNode(&Node{Name: name, Type: ptype, Server: server, Port: port}, "")
}

// str 将 interface{} 安全转为字符串。
func str(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// yamlPort 处理 port 为 int、int64、float64 或 string 的情况。
func yamlPort(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, n > 0 && n <= 65535
	case int64:
		p := int(n)
		return p, p > 0 && p <= 65535
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
