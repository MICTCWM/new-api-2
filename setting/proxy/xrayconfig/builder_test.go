// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package xrayconfig

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildVmess(t *testing.T) {
	js := `{"v":2,"ps":"n","add":"1.2.3.4","port":"443","id":"00000000-0000-0000-0000-000000000001","aid":"0","scy":"auto","net":"ws","tls":"tls","host":"example.com","path":"/ws"}`
	share := "vmess://" + base64.StdEncoding.EncodeToString([]byte(js))
	cfg, err := Build(share, 31001)
	require.NoError(t, err)
	outbounds := cfg["outbounds"].([]interface{})
	require.Len(t, outbounds, 2)
	proxy := outbounds[0].(map[string]interface{})
	assert.Equal(t, "vmess", proxy["protocol"])
}

func TestBuildVless(t *testing.T) {
	share := "vless://00000000-0000-0000-0000-000000000001@9.9.9.9:443?encryption=none&security=tls&sni=example.com#node"
	cfg, err := Build(share, 31002)
	require.NoError(t, err)
	proxy := cfg["outbounds"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, "vless", proxy["protocol"])
}

func TestBuildVlessRealityUsesRealitySettings(t *testing.T) {
	share := "vless://00000000-0000-0000-0000-000000000001@9.9.9.9:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=example.com&fp=chrome&pbk=public-key&sid=abcd&spx=%2F#node"
	cfg, err := Build(share, 31003)
	require.NoError(t, err)
	proxy := cfg["outbounds"].([]interface{})[0].(map[string]interface{})
	stream := proxy["streamSettings"].(map[string]interface{})
	require.NotContains(t, stream, "tlsSettings")
	reality := stream["realitySettings"].(map[string]interface{})
	assert.Equal(t, "chrome", reality["fingerprint"])
	assert.Equal(t, "example.com", reality["serverName"])
	assert.Equal(t, "public-key", reality["publicKey"])
	assert.Equal(t, "abcd", reality["shortId"])
}

func TestBuildVlessGRPCUsesV2rayNQueryNames(t *testing.T) {
	share := "vless://00000000-0000-0000-0000-000000000001@9.9.9.9:443?encryption=none&security=tls&type=grpc&authority=grpc.example.com&serviceName=api&mode=multi"
	cfg, err := Build(share, 31004)
	require.NoError(t, err)
	proxy := cfg["outbounds"].([]interface{})[0].(map[string]interface{})
	stream := proxy["streamSettings"].(map[string]interface{})
	grpc := stream["grpcSettings"].(map[string]interface{})
	assert.Equal(t, "api", grpc["serviceName"])
	assert.Equal(t, "grpc.example.com", grpc["authority"])
	assert.Equal(t, true, grpc["multiMode"])
}

func TestBuildShadowsocksSIP002(t *testing.T) {
	credentials := base64.RawStdEncoding.EncodeToString([]byte("aes-128-gcm:password"))
	share := "ss://" + credentials + "@1.2.3.4:8388#node"
	cfg, err := Build(share, 31005)
	require.NoError(t, err)
	proxy := cfg["outbounds"].([]interface{})[0].(map[string]interface{})
	settings := proxy["settings"].(map[string]interface{})
	servers := settings["servers"].([]interface{})
	server := servers[0].(map[string]interface{})
	assert.Equal(t, "aes-128-gcm", server["method"])
	assert.Equal(t, "password", server["password"])
}

func extractRawSettings(t *testing.T, proxy map[string]interface{}) map[string]interface{} {
	t.Helper()
	stream, ok := proxy["streamSettings"].(map[string]interface{})
	require.True(t, ok, "outbound should have streamSettings")
	raw, ok := stream["rawSettings"].(map[string]interface{})
	require.True(t, ok, "streamSettings should have rawSettings")
	return raw
}

func assertTCPHTTPObfsHeader(t *testing.T, raw map[string]interface{}) {
	t.Helper()
	header := raw["header"].(map[string]interface{})
	assert.Equal(t, "http", header["type"])
	request := header["request"].(map[string]interface{})
	assert.Equal(t, "1.1", request["version"])
	assert.Equal(t, "GET", request["method"])
	headers := request["headers"].(map[string]interface{})
	for _, name := range []string{"Host", "User-Agent", "Accept-Encoding", "Connection", "Pragma"} {
		require.Contains(t, headers, name, "missing standard obfuscation header %s", name)
		assert.NotEmpty(t, headers[name], "empty obfuscation header %s", name)
	}
}

func TestBuildVlessTCPHTTPObfsUsesHostAndPath(t *testing.T) {
	share := "vless://00000000-0000-0000-0000-000000000001@9.9.9.9:443?encryption=none&type=tcp&headerType=http&host=obfs.example.com&path=%2Fhpath#node"
	cfg, err := Build(share, 31006)
	require.NoError(t, err)
	proxy := cfg["outbounds"].([]interface{})[0].(map[string]interface{})
	raw := extractRawSettings(t, proxy)
	assertTCPHTTPObfsHeader(t, raw)

	request := raw["header"].(map[string]interface{})["request"].(map[string]interface{})
	assert.Equal(t, []interface{}{"/hpath"}, request["path"])
	headers := request["headers"].(map[string]interface{})
	assert.Equal(t, []interface{}{"obfs.example.com"}, headers["Host"])
}

func TestBuildVmessTCPHTTPObfsUsesHostAndPath(t *testing.T) {
	js := `{"v":2,"ps":"n","add":"1.2.3.4","port":"443","id":"00000000-0000-0000-0000-000000000001","aid":"0","scy":"auto","net":"tcp","type":"http","host":"obfs.example.com","path":"/vpath"}`
	share := "vmess://" + base64.StdEncoding.EncodeToString([]byte(js))
	cfg, err := Build(share, 31007)
	require.NoError(t, err)
	proxy := cfg["outbounds"].([]interface{})[0].(map[string]interface{})
	raw := extractRawSettings(t, proxy)
	assertTCPHTTPObfsHeader(t, raw)

	request := raw["header"].(map[string]interface{})["request"].(map[string]interface{})
	assert.Equal(t, []interface{}{"/vpath"}, request["path"])
	headers := request["headers"].(map[string]interface{})
	assert.Equal(t, []interface{}{"obfs.example.com"}, headers["Host"])
}
