// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package xrayconfig

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Build 根据 v2rayN 分享链接生成 Xray 配置 JSON（本地 socks 入站 + 单出站）。
func Build(shareURL string, socksPort int) (map[string]interface{}, error) {
	shareURL = strings.TrimSpace(shareURL)
	if shareURL == "" {
		return nil, fmt.Errorf("share url is required")
	}
	if socksPort <= 0 || socksPort > 65535 {
		return nil, fmt.Errorf("invalid socks port: %d", socksPort)
	}

	var outbound map[string]interface{}
	var err error
	switch {
	case strings.HasPrefix(shareURL, "vmess://"):
		outbound, err = buildVmessOutbound(shareURL)
	case strings.HasPrefix(shareURL, "vless://"):
		outbound, err = buildVlessOutbound(shareURL)
	case strings.HasPrefix(shareURL, "trojan://"):
		outbound, err = buildTrojanOutbound(shareURL)
	case strings.HasPrefix(shareURL, "ss://"):
		outbound, err = buildSSOutbound(shareURL)
	default:
		return nil, fmt.Errorf("unsupported xray share url scheme")
	}
	if err != nil {
		return nil, err
	}
	outbound["tag"] = "proxy"

	return map[string]interface{}{
		"log": map[string]interface{}{"loglevel": "warning"},
		"inbounds": []interface{}{
			map[string]interface{}{
				"listen":   "127.0.0.1",
				"port":     socksPort,
				"protocol": "socks",
				"settings": map[string]interface{}{"udp": true},
				"tag":      "socks-in",
			},
		},
		"outbounds": []interface{}{
			outbound,
			map[string]interface{}{"protocol": "freedom", "tag": "direct"},
		},
	}, nil
}

func buildVmessOutbound(shareURL string) (map[string]interface{}, error) {
	payload := strings.TrimPrefix(shareURL, "vmess://")
	data, err := decodeBase64(payload)
	if err != nil {
		return nil, fmt.Errorf("invalid vmess share url: %w", err)
	}
	var obj map[string]interface{}
	if err := common.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("invalid vmess json: %w", err)
	}

	address := strings.TrimSpace(str(obj["add"]))
	port, ok := intFromAny(obj["port"])
	if !ok || address == "" {
		return nil, fmt.Errorf("vmess missing address or port")
	}
	uuid := strings.TrimSpace(str(obj["id"]))
	if uuid == "" {
		return nil, fmt.Errorf("vmess missing id")
	}
	alterID, _ := intFromAny(obj["aid"])
	security := strings.TrimSpace(str(obj["scy"]))
	if security == "" {
		security = "auto"
	}

	outbound := map[string]interface{}{
		"protocol": "vmess",
		"settings": map[string]interface{}{
			"vnext": []interface{}{
				map[string]interface{}{
					"address": address,
					"port":    port,
					"users": []interface{}{
						map[string]interface{}{
							"id":       uuid,
							"alterId":  alterID,
							"security": security,
						},
					},
				},
			},
		},
	}
	if stream := buildStreamSettingsFromVmess(obj); stream != nil {
		outbound["streamSettings"] = stream
	}
	return outbound, nil
}

func buildStreamSettingsFromVmess(obj map[string]interface{}) map[string]interface{} {
	network := strings.ToLower(strings.TrimSpace(str(obj["net"])))
	if network == "" || network == "none" || network == "raw" {
		network = "tcp"
	}
	stream := map[string]interface{}{"network": network}
	host := strings.TrimSpace(str(obj["host"]))
	path := strings.TrimSpace(str(obj["path"]))
	headerType := strings.TrimSpace(str(obj["type"]))

	tlsFlag := strings.EqualFold(strings.TrimSpace(str(obj["tls"])), "tls")
	sni := strings.TrimSpace(str(obj["sni"]))
	if tlsFlag {
		stream["security"] = "tls"
		tlsSettings := map[string]interface{}{}
		if sni == "" {
			sni = firstCommaValue(host)
		}
		if sni != "" {
			tlsSettings["serverName"] = sni
		}
		if fp := strings.TrimSpace(str(obj["fp"])); fp != "" {
			tlsSettings["fingerprint"] = fp
		}
		if alpn := strings.TrimSpace(str(obj["alpn"])); alpn != "" {
			tlsSettings["alpn"] = strings.Split(alpn, ",")
		}
		stream["tlsSettings"] = tlsSettings
	}

	switch network {
	case "ws":
		wsSettings := map[string]interface{}{"path": path}
		if host != "" {
			wsSettings["host"] = host
		}
		stream["wsSettings"] = wsSettings
	case "httpupgrade":
		stream["httpupgradeSettings"] = map[string]interface{}{
			"path": path,
			"host": host,
		}
	case "grpc":
		grpcSettings := map[string]interface{}{"serviceName": path}
		if host != "" {
			grpcSettings["authority"] = host
		}
		if headerType == "multi" {
			grpcSettings["multiMode"] = true
		}
		stream["grpcSettings"] = grpcSettings
	case "xhttp", "splithttp":
		xhttpSettings := map[string]interface{}{
			"path": path,
			"host": host,
		}
		if headerType != "" {
			xhttpSettings["mode"] = headerType
		}
		stream["xhttpSettings"] = xhttpSettings
	case "h2", "http":
		httpSettings := map[string]interface{}{"path": path}
		if host != "" {
			httpSettings["host"] = []interface{}{host}
		}
		stream["httpSettings"] = httpSettings
	case "tcp":
		if headerType != "" && headerType != "none" {
			header := map[string]interface{}{"type": headerType}
			if headerType == "http" {
				header = buildTCPHTTPObfsHeader(host, path)
			}
			stream["rawSettings"] = map[string]interface{}{
				"header": header,
			}
		}
	}
	return stream
}

func buildVlessOutbound(shareURL string) (map[string]interface{}, error) {
	u, err := url.Parse(shareURL)
	if err != nil {
		return nil, err
	}
	address := u.Hostname()
	if address == "" || u.User == nil {
		return nil, fmt.Errorf("vless missing address or uuid")
	}
	port, err := portFromURL(u)
	if err != nil {
		return nil, err
	}
	uuid := strings.TrimSpace(u.User.Username())
	if uuid == "" {
		return nil, fmt.Errorf("vless missing uuid")
	}
	q := u.Query()
	encryption := q.Get("encryption")
	if encryption == "" {
		encryption = "none"
	}

	outbound := map[string]interface{}{
		"protocol": "vless",
		"settings": map[string]interface{}{
			"vnext": []interface{}{
				map[string]interface{}{
					"address": address,
					"port":    port,
					"users": []interface{}{
						map[string]interface{}{
							"id":         uuid,
							"encryption": encryption,
							"flow":       q.Get("flow"),
						},
					},
				},
			},
		},
	}
	if stream := buildStreamSettingsFromQuery(q); stream != nil {
		outbound["streamSettings"] = stream
	}
	return outbound, nil
}

func buildTrojanOutbound(shareURL string) (map[string]interface{}, error) {
	u, err := url.Parse(shareURL)
	if err != nil {
		return nil, err
	}
	address := u.Hostname()
	if address == "" || u.User == nil {
		return nil, fmt.Errorf("trojan missing address or password")
	}
	port, err := portFromURL(u)
	if err != nil {
		return nil, err
	}
	password := strings.TrimSpace(u.User.Username())
	if password == "" {
		if u.User != nil {
			password, _ = url.QueryUnescape(u.User.String())
		}
	}
	if password == "" {
		return nil, fmt.Errorf("trojan missing password")
	}

	outbound := map[string]interface{}{
		"protocol": "trojan",
		"settings": map[string]interface{}{
			"servers": []interface{}{
				map[string]interface{}{
					"address":  address,
					"port":     port,
					"password": password,
				},
			},
		},
	}
	if stream := buildStreamSettingsFromQuery(u.Query()); stream != nil {
		outbound["streamSettings"] = stream
	}
	return outbound, nil
}

func buildSSOutbound(shareURL string) (map[string]interface{}, error) {
	u, err := url.Parse(shareURL)
	if err != nil {
		return nil, err
	}
	address := u.Hostname()
	port, err := portFromURL(u)
	if err != nil {
		return nil, err
	}

	method, password, err := parseSSUserInfo(u)
	if err != nil {
		rest := strings.TrimPrefix(shareURL, "ss://")
		if idx := strings.IndexByte(rest, '#'); idx >= 0 {
			rest = rest[:idx]
		}
		decoded, decErr := decodeBase64(rest)
		if decErr != nil {
			return nil, fmt.Errorf("invalid ss share url")
		}
		at := strings.LastIndexByte(string(decoded), '@')
		if at < 0 {
			return nil, fmt.Errorf("invalid ss share url")
		}
		userinfo := string(decoded[:at])
		hostport := string(decoded[at+1:])
		method, password, err = splitSSUserInfo(userinfo)
		if err != nil {
			return nil, err
		}
		decodedAddress, decodedPort, splitErr := net.SplitHostPort(hostport)
		if splitErr != nil {
			return nil, fmt.Errorf("invalid ss hostport: %w", splitErr)
		}
		address = decodedAddress
		port, err = strconv.Atoi(decodedPort)
		if err != nil || port <= 0 || port > 65535 {
			return nil, fmt.Errorf("invalid ss port")
		}
	}

	if method == "" || address == "" || port <= 0 {
		return nil, fmt.Errorf("ss missing method or server")
	}

	return map[string]interface{}{
		"protocol": "shadowsocks",
		"settings": map[string]interface{}{
			"servers": []interface{}{
				map[string]interface{}{
					"address":  address,
					"port":     port,
					"method":   method,
					"password": password,
				},
			},
		},
	}, nil
}

func buildStreamSettingsFromQuery(q url.Values) map[string]interface{} {
	network := strings.ToLower(strings.TrimSpace(q.Get("type")))
	if network == "" || network == "raw" {
		network = "tcp"
	}
	stream := map[string]interface{}{"network": network}

	security := strings.ToLower(strings.TrimSpace(q.Get("security")))
	if security == "" && strings.EqualFold(q.Get("tls"), "1") {
		security = "tls"
	}
	if security == "tls" {
		stream["security"] = security
		tlsSettings := map[string]interface{}{}
		if sni := strings.TrimSpace(q.Get("sni")); sni != "" {
			tlsSettings["serverName"] = sni
		} else if host := strings.TrimSpace(q.Get("host")); host != "" {
			tlsSettings["serverName"] = firstCommaValue(host)
		}
		if fp := strings.TrimSpace(q.Get("fp")); fp != "" {
			tlsSettings["fingerprint"] = fp
		}
		if alpn := strings.TrimSpace(q.Get("alpn")); alpn != "" {
			tlsSettings["alpn"] = strings.Split(alpn, ",")
		}
		stream["tlsSettings"] = tlsSettings
	} else if security == "reality" {
		stream["security"] = security
		realitySettings := map[string]interface{}{}
		if fp := strings.TrimSpace(q.Get("fp")); fp != "" {
			realitySettings["fingerprint"] = fp
		}
		if sni := strings.TrimSpace(q.Get("sni")); sni != "" {
			realitySettings["serverName"] = sni
		}
		if pbk := strings.TrimSpace(q.Get("pbk")); pbk != "" {
			realitySettings["publicKey"] = pbk
		}
		if sid := strings.TrimSpace(q.Get("sid")); sid != "" {
			realitySettings["shortId"] = sid
		}
		if spx := strings.TrimSpace(q.Get("spx")); spx != "" {
			realitySettings["spiderX"] = spx
		}
		if pqv := strings.TrimSpace(q.Get("pqv")); pqv != "" {
			realitySettings["mldsa65Verify"] = pqv
		}
		stream["realitySettings"] = realitySettings
	}

	path := strings.TrimSpace(q.Get("path"))
	host := strings.TrimSpace(q.Get("host"))
	switch network {
	case "ws":
		wsSettings := map[string]interface{}{"path": path}
		if host != "" {
			wsSettings["host"] = host
		}
		stream["wsSettings"] = wsSettings
	case "httpupgrade":
		stream["httpupgradeSettings"] = map[string]interface{}{
			"path": path,
			"host": host,
		}
	case "grpc":
		serviceName := strings.TrimSpace(q.Get("serviceName"))
		if serviceName == "" {
			serviceName = path
		}
		grpcSettings := map[string]interface{}{"serviceName": serviceName}
		if authority := strings.TrimSpace(q.Get("authority")); authority != "" {
			grpcSettings["authority"] = authority
		}
		if q.Get("mode") == "multi" {
			grpcSettings["multiMode"] = true
		}
		stream["grpcSettings"] = grpcSettings
	case "xhttp", "splithttp":
		xhttpSettings := map[string]interface{}{
			"path": path,
			"host": host,
		}
		if mode := strings.TrimSpace(q.Get("mode")); mode != "" {
			xhttpSettings["mode"] = mode
		}
		if extra := strings.TrimSpace(q.Get("extra")); extra != "" {
			var extraValue interface{}
			if err := common.UnmarshalJsonStr(extra, &extraValue); err == nil {
				xhttpSettings["extra"] = extraValue
			}
		}
		stream["xhttpSettings"] = xhttpSettings
	case "h2", "http":
		httpSettings := map[string]interface{}{"path": path}
		if host != "" {
			httpSettings["host"] = []interface{}{host}
		}
		stream["httpSettings"] = httpSettings
	case "tcp":
		if headerType := strings.TrimSpace(q.Get("headerType")); headerType != "" && headerType != "none" {
			header := map[string]interface{}{"type": headerType}
			if headerType == "http" {
				header = buildTCPHTTPObfsHeader(host, path)
			}
			stream["rawSettings"] = map[string]interface{}{
				"header": header,
			}
		}
	}
	return stream
}

func parseSSUserInfo(u *url.URL) (string, string, error) {
	if u.User == nil {
		return "", "", fmt.Errorf("missing ss user info")
	}
	if password, ok := u.User.Password(); ok {
		return u.User.Username(), password, nil
	}
	decoded, err := decodeBase64(u.User.Username())
	if err != nil {
		return "", "", fmt.Errorf("invalid ss user info")
	}
	return splitSSUserInfo(string(decoded))
}

func splitSSUserInfo(userInfo string) (string, string, error) {
	method, password, ok := strings.Cut(userInfo, ":")
	if !ok || method == "" || password == "" {
		return "", "", fmt.Errorf("invalid ss credentials")
	}
	return method, password, nil
}

func firstCommaValue(value string) string {
	if index := strings.IndexByte(value, ','); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

// buildTCPHTTPObfsHeader 生成 TCP 网络 HTTP 伪装的完整 header 配置。
// 缺少 request 对象时 Xray 会回退到内置默认 Host（www.baidu.com 等），
// 服务端校验 Host/path 时客户端伪装头不匹配会直接被对端断开。
func buildTCPHTTPObfsHeader(host, path string) map[string]interface{} {
	paths := []interface{}{"/"}
	if values := splitCommaList(path); len(values) > 0 {
		paths = toInterfaceSlice(values)
	}
	hosts := []interface{}{"www.baidu.com", "www.bing.com"}
	if values := splitCommaList(host); len(values) > 0 {
		hosts = toInterfaceSlice(values)
	}
	return map[string]interface{}{
		"type": "http",
		"request": map[string]interface{}{
			"version": "1.1",
			"method":  "GET",
			"path":    paths,
			"headers": map[string]interface{}{
				"Host":            hosts,
				"User-Agent":      []interface{}{"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/53.0.2785.143 Safari/537.36"},
				"Accept-Encoding": []interface{}{"gzip, deflate"},
				"Connection":      []interface{}{"keep-alive"},
				"Pragma":          []interface{}{"no-cache"},
			},
		},
	}
}

func splitCommaList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func toInterfaceSlice(values []string) []interface{} {
	result := make([]interface{}, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func portFromURL(u *url.URL) (int, error) {
	portStr := u.Port()
	if portStr == "" {
		switch strings.ToLower(u.Scheme) {
		case "http", "ws":
			portStr = "80"
		default:
			portStr = "443"
		}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port in share url")
	}
	return port, nil
}

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
	return nil, fmt.Errorf("invalid base64")
}

func str(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func intFromAny(v interface{}) (int, bool) {
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
