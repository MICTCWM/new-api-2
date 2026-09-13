// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// xrayRelayStartCooldown 是启动失败后的重试冷却时间。
	xrayRelayStartCooldown = 15 * time.Second
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
	// lastStartFailure 记录各规则最近一次启动失败时间，请求路径自愈时用于
	// 冷却退避，避免下游不可达时每个请求都重复拉起进程打满锁。
	lastStartFailure map[string]time.Time
}

var proxyXrayRelay = &xrayRelayManager{
	instances:        make(map[string]*xrayRelayInstance),
	nextPort:         xrayRelayPortStart,
	lastStartFailure: make(map[string]time.Time),
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

func (m *xrayRelayManager) start(ruleID, shareURL string, preferredPort int) (int, error) {
	ruleID = strings.TrimSpace(ruleID)
	shareURL = strings.TrimSpace(shareURL)
	if ruleID == "" || shareURL == "" {
		return 0, fmt.Errorf("rule id and share url are required")
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	if inst, ok := m.instances[ruleID]; ok {
		return inst.port, nil
	}
	// 冷却期内不再重复拉起：请求路径的自愈检查会高频触发，避免反复 exec。
	if failedAt, ok := m.lastStartFailure[ruleID]; ok && time.Since(failedAt) < xrayRelayStartCooldown {
		return 0, fmt.Errorf("xray relay for rule %s recently failed to start, retry later", ruleID)
	}

	port := 0
	// 优先复用规则持久化的端口，保证重启后渠道里保存的 ProxyURL 仍然有效；
	// 端口被非本管理器进程占用时才退回自动分配。
	if preferredPort >= xrayRelayPortStart && preferredPort <= xrayRelayPortEnd && m.portFree(preferredPort) {
		port = preferredPort
		m.nextPort = preferredPort + 1
		if m.nextPort > xrayRelayPortEnd {
			m.nextPort = xrayRelayPortStart
		}
	} else {
		allocated, err := m.allocatePort()
		if err != nil {
			return 0, err
		}
		port = allocated
	}

	cfg, err := xrayconfig.Build(shareURL, port)
	if err != nil {
		m.lastStartFailure[ruleID] = time.Now()
		return 0, err
	}
	raw, err := common.Marshal(cfg)
	if err != nil {
		m.lastStartFailure[ruleID] = time.Now()
		return 0, err
	}

	configDir := xrayRelayConfigDir()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		m.lastStartFailure[ruleID] = time.Now()
		return 0, err
	}
	configPath := filepath.Join(configDir, ruleID+".json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		m.lastStartFailure[ruleID] = time.Now()
		return 0, err
	}

	binary := xrayBinaryPath()
	if _, err := os.Stat(binary); err != nil {
		m.lastStartFailure[ruleID] = time.Now()
		return 0, fmt.Errorf("xray binary not found at %s: install Xray-core or set XRAY_BINARY_PATH", binary)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary, "run", "-c", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		m.lastStartFailure[ruleID] = time.Now()
		return 0, fmt.Errorf("failed to start xray relay: %w", err)
	}

	// 等待本地 socks 端口就绪。
	if err := waitForLocalPort(port, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		cancel()
		_ = os.Remove(configPath)
		m.lastStartFailure[ruleID] = time.Now()
		return 0, err
	}

	m.instances[ruleID] = &xrayRelayInstance{
		port:       port,
		configPath: configPath,
		cmd:        cmd,
	}
	delete(m.lastStartFailure, ruleID)
	go func() {
		_ = cmd.Wait()
		cancel()
		// 进程退出后移除实例条目，避免健康检查误报存活、
		// 且后续 Resolve/自愈能按同一端口重新拉起。
		m.mutex.Lock()
		if inst, ok := m.instances[ruleID]; ok && inst.cmd == cmd {
			delete(m.instances, ruleID)
		}
		m.mutex.Unlock()
	}()

	return port, nil
}

// portFree 检查端口当前是否可监听（须在持有 mutex 时调用）。
func (m *xrayRelayManager) portFree(port int) bool {
	for _, inst := range m.instances {
		if inst.port == port {
			return false
		}
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// runningOnPort 报告是否有运行中的中继实例监听在指定端口（快速健康检查）。
func (m *xrayRelayManager) runningOnPort(port int) bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	for _, inst := range m.instances {
		if inst.port == port {
			return true
		}
	}
	return false
}

// relayPortFromProxyURL 从本地中继代理地址中解析监听端口，解析失败返回 0。
func relayPortFromProxyURL(proxyURL string) int {
	u, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil {
		return 0
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "socks" && scheme != "socks5" {
		return 0
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return 0
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < xrayRelayPortStart || port > xrayRelayPortEnd {
		return 0
	}
	return port
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
// preferredPort 是规则此前持久化的中继端口（0 表示新规则，自动分配），
// 返回的 relayPort 供规则持久化，保证重启后按固定端口恢复。
func ResolveRuleProxyURL(ruleID string, node proxypkg.Node, preferredPort int) (string, int, proxypkg.Node, error) {
	node.ShareURL = strings.TrimSpace(node.ShareURL)
	if !proxypkg.CanBindNode(node) {
		return "", 0, node, fmt.Errorf("节点不可用或协议不支持: %s", node.Type)
	}
	if proxypkg.IsDirectBindableType(node.Type) {
		proxyURL, err := proxypkg.NormalizeNodeToProxyURL(node)
		if err != nil {
			return "", 0, node, err
		}
		node.Relay = false
		node.ProxyURL = proxyURL
		return proxyURL, 0, node, nil
	}
	if !proxypkg.IsXrayRelayType(node.Type) {
		return "", 0, node, fmt.Errorf("unsupported proxy node type: %s", node.Type)
	}
	if node.ShareURL == "" {
		return "", 0, node, fmt.Errorf("xray relay requires share_url")
	}
	port, err := proxyXrayRelay.start(ruleID, node.ShareURL, preferredPort)
	if err != nil {
		return "", 0, node, err
	}
	proxyURL := proxyXrayRelay.localProxyURL(port)
	node.Relay = true
	node.ProxyURL = proxyURL
	return proxyURL, port, node, nil
}

// StopRuleXrayRelay 停止规则对应的 Xray 中继进程。
func StopRuleXrayRelay(ruleID string) {
	proxyXrayRelay.stop(ruleID)
}

// InitProxyXrayRelays 启动时恢复所有需要 Xray 中继的规则。
// 端口优先取规则持久化的 RelayPort，历史数据回退为从 ProxyURL 解析，
// 与渠道里保存的代理地址保持一致，避免重启后端口错位。
func InitProxyXrayRelays(rules []operation_setting.Rule) {
	for _, rule := range rules {
		if !proxypkg.IsXrayRelayType(rule.Node.Type) {
			continue
		}
		shareURL := strings.TrimSpace(rule.Node.ShareURL)
		if shareURL == "" {
			continue
		}
		port := rule.RelayPort
		if port == 0 {
			port = relayPortFromProxyURL(rule.ProxyURL)
		}
		if _, err := proxyXrayRelay.start(rule.ID, shareURL, port); err != nil {
			common.SysError(fmt.Sprintf("failed to restore xray relay for rule %s: %v", rule.ID, err))
		}
	}
}

// EnsureXrayRelayReady 请求路径自愈：代理地址指向本地 Xray 中继但对应进程
// 不在运行（典型场景是重启后恢复失败或进程被杀）时，按规则持久化的配置重新拉起。
// 外部代理地址或中继健康时为快速 no-op。返回中继当前是否可用。
func EnsureXrayRelayReady(proxyURL string) bool {
	port := relayPortFromProxyURL(proxyURL)
	if port == 0 {
		return true
	}
	if proxyXrayRelay.runningOnPort(port) {
		return true
	}
	target := strings.TrimSpace(proxyURL)
	for _, rule := range LoadProxyRules() {
		if !proxypkg.IsXrayRelayType(rule.Node.Type) {
			continue
		}
		if !isSameProxy(strings.TrimSpace(rule.ProxyURL), target) {
			continue
		}
		preferred := rule.RelayPort
		if preferred == 0 {
			preferred = port
		}
		if _, err := proxyXrayRelay.start(rule.ID, strings.TrimSpace(rule.Node.ShareURL), preferred); err != nil {
			common.SysError(fmt.Sprintf("failed to lazily restore xray relay for rule %s: %v", rule.ID, err))
			return false
		}
		return true
	}
	// 没有规则认领这个本地中继地址：可能规则已删除但渠道仍引用，按不可用处理。
	return false
}
