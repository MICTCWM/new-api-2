// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// findNode 按 server 与类型（可为空表示任意）查找节点，用于精确断言。
func findNode(t *testing.T, nodes []Node, server, typ string) *Node {
	t.Helper()
	for i := range nodes {
		if nodes[i].Server == server && (typ == "" || nodes[i].Type == typ) {
			return &nodes[i]
		}
	}
	return nil
}

// mixedPlain 返回混排明文行样例。
func mixedPlain() string {
	vmessJSON := `{"v":2,"ps":"vmess-node","add":"5.6.7.8","port":"443"}`
	lines := []string{
		"ss://aes-128-gcm:abc@1.2.3.4:8388#ss-node",
		"vmess://" + b64(vmessJSON),
		"trojan://password@t1.example.com:443#trojan-node",
		"socks5://user:pass@6.7.8.9:1080#socks5-node",
		"http://1.2.3.4:8080#http-node",
		"socks://u:p@10.0.0.1:1080#socks-node",
	}
	return strings.Join(lines, "\n")
}

func TestParseSubscriptionBase64Whole(t *testing.T) {
	raw := []byte(b64(mixedPlain()))
	nodes, err := ParseSubscription(raw, "https://sub.example.com/api?token=secret")
	require.NoError(t, err)
	require.Len(t, nodes, 6)

	ssN := findNode(t, nodes, "1.2.3.4", "ss")
	require.NotNil(t, ssN)
	assert.Equal(t, "ss-node", ssN.Name)
	assert.Equal(t, "ss", ssN.Type)
	assert.Equal(t, 8388, ssN.Port)
	assert.True(t, ssN.Bindable)
	assert.True(t, ssN.Relay)

	vm := findNode(t, nodes, "5.6.7.8", "")
	require.NotNil(t, vm)
	assert.Equal(t, "vmess-node", vm.Name)
	assert.Equal(t, "vmess", vm.Type)
	assert.Equal(t, 443, vm.Port)
	assert.True(t, vm.Bindable)
	assert.True(t, vm.Relay)
	assert.NotEmpty(t, vm.ShareURL)

	tr := findNode(t, nodes, "t1.example.com", "")
	require.NotNil(t, tr)
	assert.Equal(t, "trojan-node", tr.Name)
	assert.Equal(t, "trojan", tr.Type)
	assert.Equal(t, 443, tr.Port)
	assert.True(t, tr.Bindable)
	assert.True(t, tr.Relay)

	s5 := findNode(t, nodes, "6.7.8.9", "")
	require.NotNil(t, s5)
	assert.Equal(t, "socks5-node", s5.Name)
	assert.Equal(t, "socks5", s5.Type)
	assert.Equal(t, 1080, s5.Port)
	assert.True(t, s5.Bindable)
	assert.Equal(t, "socks5://6.7.8.9:1080", s5.ProxyURL)

	httpN := findNode(t, nodes, "1.2.3.4", "http")
	require.NotNil(t, httpN)
	assert.Equal(t, "http-node", httpN.Name)
	assert.Equal(t, "http", httpN.Type)
	assert.Equal(t, 8080, httpN.Port)
	assert.True(t, httpN.Bindable)
	assert.Equal(t, "http://1.2.3.4:8080", httpN.ProxyURL)

	socksN := findNode(t, nodes, "10.0.0.1", "")
	require.NotNil(t, socksN)
	assert.Equal(t, "socks5", socksN.Type)
	assert.True(t, socksN.Bindable)
	assert.Equal(t, "socks5://10.0.0.1:1080", socksN.ProxyURL)
}

func TestParseSubscriptionPlainLines(t *testing.T) {
	nodes, err := ParseSubscription([]byte(mixedPlain()), "")
	require.NoError(t, err)
	require.Len(t, nodes, 6)
}

func TestParseSubscriptionVmessNumericPort(t *testing.T) {
	// port 为数字而非字符串。
	js := `{"v":2,"ps":"n","add":"9.9.9.9","port":8443}`
	raw := "vmess://" + b64(js)
	nodes, err := ParseSubscription([]byte(raw), "")
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, 8443, nodes[0].Port)
	assert.Equal(t, "n", nodes[0].Name)
}

func TestParseSubscriptionDedup(t *testing.T) {
	// 同一节点重复出现只保留一次。
	raw := strings.Join([]string{
		"http://1.2.3.4:8080#a",
		"http://1.2.3.4:8080#a",
	}, "\n")
	nodes, err := ParseSubscription([]byte(raw), "")
	require.NoError(t, err)
	require.Len(t, nodes, 1)
}

func TestParseSubscriptionClashYAML(t *testing.T) {
	yaml := `proxies:
  - name: "http-node"
    type: http
    server: 2.2.2.2
    port: 8080
  - name: "socks5-node"
    type: socks5
    server: 3.3.3.3
    port: "1080"
  - name: "vmess-node"
    type: vmess
    server: 4.4.4.4
    port: 443
    cipher: auto
  - name: "ss-node"
    type: ss
    server: 1.1.1.1
    port: 8388
    cipher: aes-128-gcm
  - name: "direct-node"
    type: direct
    server: 0.0.0.0
    port: 1
  - name: "bad-port"
    type: http
    server: 5.5.5.5
    port: 0
  - name: "ss-rust-node"
    type: ss-rust
    server: 6.6.6.6
    port: 9090
`
	nodes, err := ParseSubscription([]byte(yaml), "")
	require.NoError(t, err)
	// 期望: http-node, socks5-node, vmess-node, ss-node, ss-rust-node（direct 与 bad-port 跳过）=> 5
	require.Len(t, nodes, 5)

	assert.True(t, findNode(t, nodes, "2.2.2.2", "http").Bindable)
	assert.Equal(t, "http://2.2.2.2:8080", findNode(t, nodes, "2.2.2.2", "http").ProxyURL)

	s5 := findNode(t, nodes, "3.3.3.3", "")
	assert.Equal(t, "socks5", s5.Type)
	assert.Equal(t, 1080, s5.Port)
	assert.Equal(t, "socks5://3.3.3.3:1080", s5.ProxyURL)

	vm := findNode(t, nodes, "4.4.4.4", "")
	assert.Equal(t, "vmess", vm.Type)
	assert.False(t, vm.Bindable)

	sr := findNode(t, nodes, "6.6.6.6", "")
	assert.Equal(t, "ss", sr.Type)
	assert.Equal(t, 9090, sr.Port)
}

func TestParseSubscriptionErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace", "   \n  "},
		{"garbage", "not a proxy at all!!!"},
		{"invalid-only-lines", "ss://\ntrojan://xxx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSubscription([]byte(tc.raw), "https://s.example.com/p?token=abc")
			require.Error(t, err)
			// 错误信息不含订阅原始内容（非空时），且不含 query 敏感参数。
			if tc.raw != "" {
				assert.NotContains(t, err.Error(), tc.raw)
			}
			assert.NotContains(t, err.Error(), "token=abc")
			if tc.name == "empty" {
				assert.Equal(t, "empty subscription", err.Error())
			}
		})
	}
}

func TestParseSubscriptionV2rayNBase64First(t *testing.T) {
	// v2rayN 常见形态：整段 base64，解码后为 vmess/ss 行。
	raw := []byte(b64(mixedPlain()))
	nodes, err := ParseSubscription(raw, "https://sub.example.com/api?token=secret")
	require.NoError(t, err)
	require.Len(t, nodes, 6)
}

func TestParseSubscriptionV2rayNExtendedProtocols(t *testing.T) {
	plain := strings.Join([]string{
		"hy2://password@1.1.1.1:8443#hy2-node",
		"tuic://uuid@2.2.2.2:443#tuic-node",
		"wireguard://key@3.3.3.3:51820#wg-node",
		"anytls://token@4.4.4.4:443#any-node",
		"naive+https://user:pass@5.5.5.5:443#naive-node",
	}, "\n")
	nodes, err := ParseSubscription([]byte(plain), "")
	require.NoError(t, err)
	require.Len(t, nodes, 5)
	assert.Equal(t, "hysteria2", findNode(t, nodes, "1.1.1.1", "").Type)
	assert.Equal(t, "tuic", findNode(t, nodes, "2.2.2.2", "").Type)
	assert.Equal(t, "wireguard", findNode(t, nodes, "3.3.3.3", "").Type)
	assert.Equal(t, "anytls", findNode(t, nodes, "4.4.4.4", "").Type)
	assert.Equal(t, "naive", findNode(t, nodes, "5.5.5.5", "").Type)
	for _, n := range nodes {
		assert.False(t, n.Bindable, "type=%s", n.Type)
	}
}

func TestParseSubscriptionSingboxJSON(t *testing.T) {
	raw := []byte(`{
  "outbounds": [
    {"type":"direct","tag":"direct"},
    {"type":"http","tag":"http-node","server":"8.8.8.8","server_port":8080},
    {"type":"socks","tag":"socks-node","server":"9.9.9.9","server_port":1080},
    {"type":"vmess","tag":"vm-node","server":"1.2.3.4","server_port":443}
  ]
}`)
	nodes, err := ParseSubscription(raw, "")
	require.NoError(t, err)
	require.Len(t, nodes, 3)
	assert.True(t, findNode(t, nodes, "8.8.8.8", "http").Bindable)
	assert.True(t, findNode(t, nodes, "9.9.9.9", "socks5").Bindable)
	assert.False(t, findNode(t, nodes, "1.2.3.4", "vmess").Bindable)
}

func TestNormalizeNodeToProxyURL(t *testing.T) {
	cases := []struct {
		name string
		node Node
		want string
		err  bool
	}{
		{"http", Node{Type: "http", Server: "1.2.3.4", Port: 8080}, "http://1.2.3.4:8080", false},
		{"https", Node{Type: "https", Server: "a.com", Port: 443}, "https://a.com:443", false},
		{"socks5", Node{Type: "socks5", Server: "1.2.3.4", Port: 1080}, "socks5://1.2.3.4:1080", false},
		{"socks-to-socks5", Node{Type: "socks", Server: "1.2.3.4", Port: 1080}, "socks5://1.2.3.4:1080", false},
		{"upper-case", Node{Type: "HTTP", Server: "1.2.3.4", Port: 80}, "http://1.2.3.4:80", false},
		{"vmess-not-direct", Node{Type: "vmess", Server: "1.2.3.4", Port: 443}, "", true},
		{"ss-unbindable", Node{Type: "ss", Server: "1.2.3.4", Port: 8388}, "", true},
		{"bad-port", Node{Type: "http", Server: "1.2.3.4", Port: 0}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeNodeToProxyURL(tc.node)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := NormalizeNodeToProxyURL(Node{Type: "vmess", Server: "1.2.3.4", Port: 443})
	assert.Contains(t, err.Error(), "unsupported direct proxy node type: vmess")
}
