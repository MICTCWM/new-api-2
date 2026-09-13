// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	proxypkg "github.com/QuantumNous/new-api/setting/proxy"
	"github.com/QuantumNous/new-api/setting/proxy/xrayconfig"
)

const (
	xrayRelayPortStart = 31000
	xrayRelayPortEnd   = 31999
)

type xrayRelayInstance struct {
	port       int
	configPath string
	cmd        *exec.Cmd
}

type xrayRelayManager struct {
	mutex     sync.Mutex
	instances map[string]*xrayRelayInstance
	nextPort  int
}

var proxyXrayRelay = &xrayRelayManager{
	instances: make(map[string]*xrayRelayInstance),
	nextPort:  xrayRelayPortStart,
}

func xrayBinaryPath() string {
	if p := strings.TrimSpace(os.Getenv("XRAY_BINARY_PATH")); p != "" {
		return p
	}
	return "/usr/local/bin/xray"
}

func xrayRelayConfigDir() string {
	if p := strings.TrimSpace(os.Getenv("XRAY_RELAY_CONFIG_DIR")); p != "" {
		return p
	}
	return "/data/xray-relay"
}

func (m *xrayRelayManager) localProxyURL(port int) string {
	return fmt.Sprintf("socks5://127.0.0.1:%d", port)
}

func (m *xrayRelayManager) allocatePort() (int, error) {
	for i := 0; i <= xrayRelayPortEnd-xrayRelayPortStart; i++ {
		port := m.nextPort
		m.nextPort++
		if m.nextPort > xrayRelayPortEnd {
			m.nextPort = xrayRelayPortStart
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no free xray relay port available")
}

func (m *xrayRelayManager) start(ruleID, shareURL string) (string, error) {
	ruleID = strings.TrimSpace(ruleID)
	shareURL = strings.TrimSpace(shareURL)
	if ruleID == "" || shareURL == "" {
		return "", fmt.Errorf("rule id and share url are required")
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	if inst, ok := m.instances[ruleID]; ok {
		return m.localProxyURL(inst.port), nil
	}

	port, err := m.allocatePort()
	if err != nil {
		return "", err
	}

	cfg, err := xrayconfig.Build(shareURL, port)
	if err != nil {
		return "", err
	}
	raw, err := common.Marshal(cfg)
	if err != nil {
		return "", err
	}

	configDir := xrayRelayConfigDir()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", err
	}
	configPath := filepath.Join(configDir, ruleID+".json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		return "", err
	}

	binary := xrayBinaryPath()
	if _, err := os.Stat(binary); err != nil {
		return "", fmt.Errorf("xray binary not found at %s: install Xray-core or set XRAY_BINARY_PATH", binary)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary, "run", "-c", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("failed to start xray relay: %w", err)
	}

	// 等待本地 socks 端口就绪。
	if err := waitForLocalPort(port, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		cancel()
		_ = os.Remove(configPath)
		return "", err
	}

	m.instances[ruleID] = &xrayRelayInstance{
		port:       port,
		configPath: configPath,
		cmd:        cmd,
	}
	go func() {
		_ = cmd.Wait()
		cancel()
	}()

	return m.localProxyURL(port), nil
}

func (m *xrayRelayManager) stop(ruleID string) {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return
	}

	m.mutex.Lock()
	inst, ok := m.instances[ruleID]
	if ok {
		delete(m.instances, ruleID)
	}
	m.mutex.Unlock()

	if !ok {
		return
	}
	if inst.cmd != nil && inst.cmd.Process != nil {
		_ = inst.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = inst.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = inst.cmd.Process.Kill()
		}
	}
	if inst.configPath != "" {
		_ = os.Remove(inst.configPath)
	}
}

func waitForLocalPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("xray relay did not listen on %s within %s", addr, timeout)
}

// ResolveRuleProxyURL 解析规则应写入渠道的代理地址：直连或 Xray 本地 socks。
func ResolveRuleProxyURL(ruleID string, node proxypkg.Node) (string, proxypkg.Node, error) {
	node.ShareURL = strings.TrimSpace(node.ShareURL)
	if !proxypkg.CanBindNode(node) {
		return "", node, fmt.Errorf("节点不可用或协议不支持: %s", node.Type)
	}
	if proxypkg.IsDirectBindableType(node.Type) {
		proxyURL, err := proxypkg.NormalizeNodeToProxyURL(node)
		if err != nil {
			return "", node, err
		}
		node.Relay = false
		node.ProxyURL = proxyURL
		return proxyURL, node, nil
	}
	if !proxypkg.IsXrayRelayType(node.Type) {
		return "", node, fmt.Errorf("unsupported proxy node type: %s", node.Type)
	}
	if node.ShareURL == "" {
		return "", node, fmt.Errorf("xray relay requires share_url")
	}
	proxyURL, err := proxyXrayRelay.start(ruleID, node.ShareURL)
	if err != nil {
		return "", node, err
	}
	node.Relay = true
	node.ProxyURL = proxyURL
	return proxyURL, node, nil
}

// StopRuleXrayRelay 停止规则对应的 Xray 中继进程。
func StopRuleXrayRelay(ruleID string) {
	proxyXrayRelay.stop(ruleID)
}

// InitProxyXrayRelays 启动时恢复所有需要 Xray 中继的规则。
func InitProxyXrayRelays(rules []operation_setting.Rule) {
	for _, rule := range rules {
		if !proxypkg.IsXrayRelayType(rule.Node.Type) {
			continue
		}
		shareURL := strings.TrimSpace(rule.Node.ShareURL)
		if shareURL == "" {
			continue
		}
		if _, err := proxyXrayRelay.start(rule.ID, shareURL); err != nil {
			common.SysError(fmt.Sprintf("failed to restore xray relay for rule %s: %v", rule.ID, err))
		}
	}
}
