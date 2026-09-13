// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	proxypkg "github.com/QuantumNous/new-api/setting/proxy"
)

const (
	// maxSubscriptionFetchBytes 拉取订阅内容的响应体上限（8MB）。
	maxSubscriptionFetchBytes = 8 << 20
	// subscriptionFetchTimeout 拉取订阅内容的 http 客户端超时。
	subscriptionFetchTimeout = 30 * time.Second
	// maxRuleChannels 单条规则可管理的渠道数量上限。
	maxRuleChannels = 500
	// maxSubscriptionNodes 订阅解析出的节点数量上限，超出拒绝保存以避免 option 巨型 JSON。
	maxSubscriptionNodes = 5000
)

// proxyRuleMutex 保护规则清单的 read-modify-write（读旧规则→strip→写回），防止并发建/删规则丢更新。
var proxyRuleMutex sync.Mutex

// ---------------------------------------------------------------------------
// option 读写（与 setting/autoscaling 的状态持久化模式一致）
// ---------------------------------------------------------------------------

func getOption(key string) string {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return common.OptionMap[key]
}

// GetProxySubscriptionURL 返回已保存的订阅地址（可能为空）。
func GetProxySubscriptionURL() string {
	return strings.TrimSpace(getOption(operation_setting.ProxySubscriptionURLOptionKey))
}

// LoadProxySubscriptionData 读回订阅节点缓存；无缓存或解析失败时返回空结构。
func LoadProxySubscriptionData() operation_setting.SubscriptionData {
	var data operation_setting.SubscriptionData
	raw := getOption(operation_setting.ProxySubscriptionDataOptionKey)
	if strings.TrimSpace(raw) == "" {
		return data
	}
	if err := common.UnmarshalJsonStr(raw, &data); err != nil {
		common.SysError("failed to parse proxy subscription data, reset to empty: " + err.Error())
		return operation_setting.SubscriptionData{}
	}
	return data
}

// LoadProxyRules 读回规则列表；无数据或解析失败时返回空切片。
func LoadProxyRules() []operation_setting.Rule {
	raw := getOption(operation_setting.ProxyRulesOptionKey)
	if strings.TrimSpace(raw) == "" {
		return []operation_setting.Rule{}
	}
	var rules []operation_setting.Rule
	if err := common.UnmarshalJsonStr(raw, &rules); err != nil {
		common.SysError("failed to parse proxy rules, reset to empty: " + err.Error())
		return []operation_setting.Rule{}
	}
	return rules
}

func saveProxySubscriptionURL(dataURL string) error {
	return model.UpdateOption(operation_setting.ProxySubscriptionURLOptionKey, strings.TrimSpace(dataURL))
}

func saveProxySubscriptionData(data operation_setting.SubscriptionData) error {
	raw, err := common.Marshal(data)
	if err != nil {
		return err
	}
	return model.UpdateOption(operation_setting.ProxySubscriptionDataOptionKey, string(raw))
}

func saveProxyRules(rules []operation_setting.Rule) error {
	raw, err := common.Marshal(rules)
	if err != nil {
		return err
	}
	return model.UpdateOption(operation_setting.ProxyRulesOptionKey, string(raw))
}

// ---------------------------------------------------------------------------
// 订阅拉取与缓存
// ---------------------------------------------------------------------------

// FetchProxySubscription 校验订阅地址、拉取并解析节点（GET + 30s 超时 + 8MB 响应体上限）。
// 订阅地址是管理员提供的任意 URL，需经 SSRF 保护校验后抓取。
// 返回的错误信息不包含订阅内容或 query 敏感参数（ParseSubscription 内部已对地址做 sanitize）。
func FetchProxySubscription(ctx context.Context, rawURL string) ([]proxypkg.Node, error) {
	target := strings.TrimSpace(rawURL)
	if target == "" {
		return nil, errors.New("subscription url is required")
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("subscription url must use http or https")
	}
	if err := validateSubscriptionFetchURL(target); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, subscriptionFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", proxypkg.V2rayNSubscriptionUserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := GetProxySubscriptionHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription fetch failed with status: %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionFetchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSubscriptionFetchBytes {
		return nil, fmt.Errorf("subscription content exceeds %d bytes", maxSubscriptionFetchBytes)
	}

	nodes, err := proxypkg.ParseSubscription(body, target)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("no nodes parsed from subscription")
	}
	if len(nodes) > maxSubscriptionNodes {
		return nil, fmt.Errorf("订阅节点数量超过上限（%d），拒绝保存，请精简订阅来源", maxSubscriptionNodes)
	}
	return nodes, nil
}

// SaveProxySubscription 拉取并解析订阅，保存订阅地址与节点缓存，返回节点列表。
func SaveProxySubscription(ctx context.Context, rawURL string) ([]proxypkg.Node, error) {
	target := strings.TrimSpace(rawURL)
	nodes, err := FetchProxySubscription(ctx, target)
	if err != nil {
		return nil, err
	}
	data := operation_setting.SubscriptionData{
		FetchedAt: common.GetTimestamp(),
		Nodes:     nodes,
	}
	if err := saveProxySubscriptionURL(target); err != nil {
		return nil, err
	}
	if err := saveProxySubscriptionData(data); err != nil {
		return nil, err
	}
	return nodes, nil
}

// RefreshProxySubscription 用已保存的订阅地址重新拉取并覆盖节点缓存，规则保持不变。
func RefreshProxySubscription(ctx context.Context) ([]proxypkg.Node, error) {
	target := GetProxySubscriptionURL()
	if target == "" {
		return nil, errors.New("no subscription url saved, please set one first")
	}
	nodes, err := FetchProxySubscription(ctx, target)
	if err != nil {
		return nil, err
	}
	data := operation_setting.SubscriptionData{
		FetchedAt: common.GetTimestamp(),
		Nodes:     nodes,
	}
	if err := saveProxySubscriptionData(data); err != nil {
		return nil, err
	}
	return nodes, nil
}

// ClearProxySubscription 清空订阅地址与节点缓存；已创建的规则保留。
func ClearProxySubscription() error {
	if err := saveProxySubscriptionURL(""); err != nil {
		return err
	}
	return saveProxySubscriptionData(operation_setting.SubscriptionData{})
}

// ---------------------------------------------------------------------------
// 渠道代理批量写入
// ---------------------------------------------------------------------------

// normalizeChannelIDs 去重并校验渠道 id（必须为正整数），返回有序去重列表；
// 数量超过 maxRuleChannels 时返回业务错误。
func normalizeChannelIDs(ids []int) ([]int, error) {
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("invalid channel id: %d", id)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if len(out) >= maxRuleChannels {
			return nil, fmt.Errorf("规则管理的渠道数量超过上限（%d）", maxRuleChannels)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one channel is required")
	}
	return out, nil
}

// isSameProxy 比较两个代理是否等价（归一化后比较）；任一为空时只做字符串等价判断。
// 无法归一化的值退化为全等比较。
func isSameProxy(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	na, errA := NormalizeProxyURL(a)
	if errA != nil {
		return a == b
	}
	nb, errB := NormalizeProxyURL(b)
	if errB != nil {
		return a == b
	}
	return na == nb
}

// dedupeAppend 向结果追加去重后的代理值。
func dedupeAppend(dst []string, v string) []string {
	if v == "" {
		return dst
	}
	for _, e := range dst {
		if e == v {
			return dst
		}
	}
	return append(dst, v)
}

// ApplyProxyToChannels 把 proxyURL 批量写入每个渠道的 ChannelSettings.Proxy，返回实际发生变化的
// 旧代理值列表（去重，供代理客户端缓存失效）。部分渠道失败时返回聚合错误，但已成功的渠道保持成功
// （不自动回滚），调用方据此向前端反馈部分生效。
func ApplyProxyToChannels(ctx context.Context, channelIDs []int, proxyURL string) ([]string, error) {
	normalized, err := NormalizeProxyURL(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("节点代理地址无效: %v", err)
	}
	proxyURL = normalized
	changedOld := make([]string, 0, len(channelIDs))
	var errs []string
	for _, id := range channelIDs {
		ch, err := model.GetChannelById(id, true)
		if err != nil {
			errs = append(errs, fmt.Sprintf("channel %d: %v", id, err))
			continue
		}
		setting := ch.GetSetting()
		if isSameProxy(setting.Proxy, proxyURL) {
			continue
		}
		changedOld = dedupeAppend(changedOld, setting.Proxy)
		setting.Proxy = proxyURL
		ch.SetSetting(setting)
		if err := ch.Update(); err != nil {
			errs = append(errs, fmt.Sprintf("channel %d: %v", id, err))
			continue
		}
	}
	if len(errs) > 0 {
		return changedOld, fmt.Errorf("proxy applied to some channels partially, failed: %s", strings.Join(errs, "; "))
	}
	return changedOld, nil
}

// ClearProxyFromChannelsIfMatches 仅当渠道当前 Proxy 恰好等于 proxyURL 时将其清空，避免误清手动配置。
// 返回被实际清空的数量；部分失败时返回聚合错误。
func ClearProxyFromChannelsIfMatches(ctx context.Context, channelIDs []int, proxyURL string) (int, error) {
	cleared := 0
	var errs []string
	for _, id := range channelIDs {
		ch, err := model.GetChannelById(id, true)
		if err != nil {
			errs = append(errs, fmt.Sprintf("channel %d: %v", id, err))
			continue
		}
		setting := ch.GetSetting()
		if !isSameProxy(setting.Proxy, proxyURL) {
			continue
		}
		setting.Proxy = ""
		ch.SetSetting(setting)
		if err := ch.Update(); err != nil {
			errs = append(errs, fmt.Sprintf("channel %d: %v", id, err))
			continue
		}
		cleared++
	}
	if len(errs) > 0 {
		return cleared, fmt.Errorf("proxy cleared partially, failed: %s", strings.Join(errs, "; "))
	}
	return cleared, nil
}

// invalidateChangedProxies 重建渠道缓存后，对发生变化的旧代理逐个失效其 http client 缓存。
func invalidateChangedProxies(changedOld []string) {
	model.InitChannelCache()
	for _, old := range changedOld {
		if old != "" {
			InvalidateProxyClient(old)
		}
	}
}

// ---------------------------------------------------------------------------
// 规则 CRUD
// ---------------------------------------------------------------------------

// stripTakenChannels 实现规则"后写覆盖"：新规则占用的渠道，只要其当前代理恰好等于某条现存规则的
// ProxyURL，就把该渠道从那两条规则中移除，避免同一渠道被多条规则同时管理。通道被移空后规则删除。
// 纯函数，便于单元测试。currentProxy 为渠道 id -> 当前代理。
func stripTakenChannels(channelIDs []int, rules []operation_setting.Rule, currentProxy map[int]string) []operation_setting.Rule {
	take := make(map[int]struct{}, len(channelIDs))
	for _, id := range channelIDs {
		take[id] = struct{}{}
	}
	out := make([]operation_setting.Rule, 0, len(rules))
	for _, r := range rules {
		kept := make([]int, 0, len(r.ChannelIDs))
		for _, cid := range r.ChannelIDs {
			if _, taken := take[cid]; taken && isSameProxy(currentProxy[cid], r.ProxyURL) {
				continue
			}
			kept = append(kept, cid)
		}
		if len(kept) > 0 {
			r.ChannelIDs = kept
			out = append(out, r)
		}
	}
	return out
}

// CreateProxyRule 创建一条规则：校验输入、归一化代理地址、按后写覆盖解决渠道归属冲突、先持久化规则
// 再批量写入渠道最后失效缓存。返回新规则；部分渠道写入失败时返回聚合错误（规则已保存并返回）。
func CreateProxyRule(ctx context.Context, name string, channelIDs []int, node proxypkg.Node) (operation_setting.Rule, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return operation_setting.Rule{}, errors.New("rule name is required")
	}
	channelIDs, err := normalizeChannelIDs(channelIDs)
	if err != nil {
		return operation_setting.Rule{}, err
	}

	ruleID := strconv.FormatInt(time.Now().UnixNano(), 36)

	proxyRuleMutex.Lock()
	defer proxyRuleMutex.Unlock()

	// 合并渠道存在性校验与当前代理收集，一次循环完成（P3-4）。
	currentProxy := make(map[int]string, len(channelIDs))
	for _, id := range channelIDs {
		ch, err := model.GetChannelById(id, true)
		if err != nil {
			return operation_setting.Rule{}, fmt.Errorf("channel %d not found: %w", id, err)
		}
		currentProxy[id] = ch.GetSetting().Proxy
	}

	rules := LoadProxyRules()
	// 后写覆盖：把本规则占用的渠道从其它现存规则中移除。
	rules = stripTakenChannels(channelIDs, rules, currentProxy)

	proxyURL, relayPort, node, resolveErr := ResolveRuleProxyURL(ruleID, node, 0)
	if resolveErr != nil {
		return operation_setting.Rule{}, resolveErr
	}
	proxyURL, err = NormalizeProxyURL(proxyURL)
	if err != nil {
		StopRuleXrayRelay(ruleID)
		return operation_setting.Rule{}, fmt.Errorf("节点代理地址无效: %v", err)
	}

	rule := operation_setting.Rule{
		ID:         ruleID,
		Name:       name,
		ChannelIDs: channelIDs,
		Node:       node,
		ProxyURL:   proxyURL,
		RelayPort:  relayPort,
		CreatedAt:  common.GetTimestamp(),
	}
	rules = append(rules, rule)

	// 先落库规则（含 strip 后的结果），再写渠道，最后无条件失效缓存。
	if persistErr := saveProxyRules(rules); persistErr != nil {
		StopRuleXrayRelay(ruleID)
		return operation_setting.Rule{}, persistErr
	}
	changedOld, applyErr := ApplyProxyToChannels(ctx, channelIDs, proxyURL)
	invalidateChangedProxies(changedOld)
	if applyErr != nil {
		return rule, applyErr
	}
	return rule, nil
}

// UpdateProxyRule 编辑规则：可改名称、绑定渠道与节点。
// - 节点未变化（ShareURL 与类型一致）时保持现有代理地址与中继进程不动；
// - 节点变化时按旧端口优先重启 Xray 中继，再更新渠道代理；
// - 新渠道集按"后写覆盖"从其它规则接管渠道；被移出的渠道在其当前代理
//   恰等于本规则旧代理时清空（避免误清手动配置）。
// 返回更新后的规则；部分渠道写入失败时返回聚合错误（规则已保存并返回）。
func UpdateProxyRule(ctx context.Context, id string, name string, channelIDs []int, node proxypkg.Node) (operation_setting.Rule, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		return operation_setting.Rule{}, errors.New("rule id is required")
	}
	if name == "" {
		return operation_setting.Rule{}, errors.New("rule name is required")
	}
	channelIDs, err := normalizeChannelIDs(channelIDs)
	if err != nil {
		return operation_setting.Rule{}, err
	}

	proxyRuleMutex.Lock()
	defer proxyRuleMutex.Unlock()

	rules := LoadProxyRules()
	index := -1
	var old operation_setting.Rule
	for i := range rules {
		if rules[i].ID == id {
			index = i
			old = rules[i]
			break
		}
	}
	if index < 0 {
		return operation_setting.Rule{}, fmt.Errorf("rule %s not found", id)
	}

	// 合并渠道存在性校验与当前代理收集。
	currentProxy := make(map[int]string, len(channelIDs))
	for _, cid := range channelIDs {
		ch, err := model.GetChannelById(cid, true)
		if err != nil {
			return operation_setting.Rule{}, fmt.Errorf("channel %d not found: %w", cid, err)
		}
		currentProxy[cid] = ch.GetSetting().Proxy
	}

	// 节点是否变化：类型/服务器/端口/分享链接任一变化即视为新节点。
	// 直连节点的身份是 server:port，中继节点的身份是 ShareURL。
	nodeChanged := !strings.EqualFold(strings.TrimSpace(node.Type), strings.TrimSpace(old.Node.Type)) ||
		strings.TrimSpace(node.Server) != strings.TrimSpace(old.Node.Server) ||
		node.Port != old.Node.Port ||
		strings.TrimSpace(node.ShareURL) != strings.TrimSpace(old.Node.ShareURL)

	proxyURL := old.ProxyURL
	relayPort := old.RelayPort
	if nodeChanged {
		// 先停旧中继释放端口，再按旧端口优先拉起新节点。
		StopRuleXrayRelay(id)
		preferred := old.RelayPort
		if preferred == 0 {
			preferred = relayPortFromProxyURL(old.ProxyURL)
		}
		resolvedURL, resolvedPort, resolvedNode, resolveErr := ResolveRuleProxyURL(id, node, preferred)
		if resolveErr != nil {
			return operation_setting.Rule{}, resolveErr
		}
		proxyURL, err = NormalizeProxyURL(resolvedURL)
		if err != nil {
			StopRuleXrayRelay(id)
			return operation_setting.Rule{}, fmt.Errorf("节点代理地址无效: %v", err)
		}
		relayPort = resolvedPort
		node = resolvedNode
	} else {
		node = old.Node
	}

	// 渠道变更集合：新集合接管其它规则中冲突的渠道；被移出的渠道按旧代理清空。
	removed := make([]int, 0, len(old.ChannelIDs))
	newSet := make(map[int]struct{}, len(channelIDs))
	for _, cid := range channelIDs {
		newSet[cid] = struct{}{}
	}
	for _, cid := range old.ChannelIDs {
		if _, kept := newSet[cid]; !kept {
			removed = append(removed, cid)
		}
	}

	others := make([]operation_setting.Rule, 0, len(rules))
	others = append(others, rules[:index]...)
	others = append(others, rules[index+1:]...)
	others = stripTakenChannels(channelIDs, others, currentProxy)

	updated := operation_setting.Rule{
		ID:         id,
		Name:       name,
		ChannelIDs: channelIDs,
		Node:       node,
		ProxyURL:   proxyURL,
		RelayPort:  relayPort,
		CreatedAt:  old.CreatedAt,
	}
	insertAt := index
	if insertAt > len(others) {
		insertAt = len(others)
	}
	others = append(others, operation_setting.Rule{})
	copy(others[insertAt+1:], others[insertAt:])
	others[insertAt] = updated

	// 先落库规则，再写渠道/清渠道，最后失效缓存。
	if persistErr := saveProxyRules(others); persistErr != nil {
		return operation_setting.Rule{}, persistErr
	}
	changedOld, applyErr := ApplyProxyToChannels(ctx, channelIDs, proxyURL)
	if len(removed) > 0 {
		cleared, clearErr := ClearProxyFromChannelsIfMatches(ctx, removed, old.ProxyURL)
		if cleared > 0 {
			changedOld = dedupeAppend(changedOld, old.ProxyURL)
		}
		if clearErr != nil {
			if applyErr != nil {
				applyErr = fmt.Errorf("%v; %v", applyErr, clearErr)
			} else {
				applyErr = clearErr
			}
		}
	}
	invalidateChangedProxies(changedOld)
	return updated, applyErr
}

// DeleteProxyRule 删除规则，并清空该规则管辖渠道中"当前代理恰等于本规则 ProxyURL"的渠道的代理
// （其余渠道不动，避免误清手动配置）。返回规则不存在错误或聚合错误。
func DeleteProxyRule(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("rule id is required")
	}

	proxyRuleMutex.Lock()
	defer proxyRuleMutex.Unlock()

	rules := LoadProxyRules()
	var target *operation_setting.Rule
	remaining := make([]operation_setting.Rule, 0, len(rules))
	for i := range rules {
		if rules[i].ID == id {
			r := rules[i]
			target = &r
		} else {
			remaining = append(remaining, rules[i])
		}
	}
	if target == nil {
		return fmt.Errorf("rule %s not found", id)
	}

	// 先删规则落库，再清渠道，最后失效缓存。
	if persistErr := saveProxyRules(remaining); persistErr != nil {
		return persistErr
	}
	cleared, er := ClearProxyFromChannelsIfMatches(ctx, target.ChannelIDs, target.ProxyURL)
	changedOld := []string{}
	if cleared > 0 {
		changedOld = append(changedOld, target.ProxyURL)
	}
	StopRuleXrayRelay(id)
	invalidateChangedProxies(changedOld)
	if er != nil {
		return er
	}
	return nil
}

// BootstrapProxyXrayRelays 在 DB/Option 就绪后恢复 Xray 中继进程。
func BootstrapProxyXrayRelays() {
	InitProxyXrayRelays(LoadProxyRules())
}