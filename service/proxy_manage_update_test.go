// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	proxypkg "github.com/QuantumNous/new-api/setting/proxy"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRelayPortFromProxyURL(t *testing.T) {
	assert.Equal(t, 31002, relayPortFromProxyURL("socks5://127.0.0.1:31002"))
	assert.Equal(t, 31002, relayPortFromProxyURL("socks5://localhost:31002"))
	assert.Equal(t, 0, relayPortFromProxyURL("socks5://10.0.0.8:31002"), "外部代理地址不属于本地中继")
	assert.Equal(t, 0, relayPortFromProxyURL("socks5://127.0.0.1:30000"), "范围外端口不视为中继")
	assert.Equal(t, 0, relayPortFromProxyURL("http://127.0.0.1:31002"))
	assert.Equal(t, 0, relayPortFromProxyURL(""))
	assert.Equal(t, 0, relayPortFromProxyURL("not a url"))
}

// newProxyManageTestDB 建立带 Option 表的内存库，并重置 OptionMap 中的代理键。
func newProxyManageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousMemoryCache := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Option{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = false

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	for _, key := range []string{
		operation_setting.ProxySubscriptionURLOptionKey,
		operation_setting.ProxySubscriptionDataOptionKey,
		operation_setting.ProxyRulesOptionKey,
	} {
		delete(common.OptionMap, key)
	}
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.MemoryCacheEnabled = previousMemoryCache
		if previousMemoryCache && previousDB != nil {
			model.InitChannelCache()
		}
	})
	return db
}

func mustCreateProxyChannel(t *testing.T, db *gorm.DB, name string, proxy string) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Name:   name,
		Type:   1,
		Status: common.ChannelStatusEnabled,
		Key:    "sk-" + name,
	}
	require.NoError(t, db.Create(channel).Error)
	if proxy != "" {
		setting := channel.GetSetting()
		setting.Proxy = proxy
		channel.SetSetting(setting)
		require.NoError(t, channel.Update())
	}
	return channel
}

func seedProxyRules(t *testing.T, rules []operation_setting.Rule) {
	t.Helper()
	raw, err := json.Marshal(rules)
	require.NoError(t, err)
	common.OptionMapRWMutex.Lock()
	common.OptionMap[operation_setting.ProxyRulesOptionKey] = string(raw)
	common.OptionMapRWMutex.Unlock()
}

func mkDirectNode(name, server string, port int) proxypkg.Node {
	return proxypkg.Node{
		Name:     name,
		Type:     "socks5",
		Server:   server,
		Port:     port,
		Bindable: true,
	}
}

// 编辑规则：改名 + 换渠道 + 换节点。被移出的渠道若代理等于旧规则代理则清空；
// 新渠道写入新代理；规则本身持久化（含 RelayPort 字段形状）。
func TestUpdateProxyRule_EditsNameChannelsAndNode(t *testing.T) {
	db := newProxyManageTestDB(t)

	oldProxy := "socks5://10.0.0.8:1080"
	newProxy := "socks5://10.0.0.9:1080"
	kept := mustCreateProxyChannel(t, db, "kept", oldProxy)
	removed := mustCreateProxyChannel(t, db, "removed", oldProxy)
	added := mustCreateProxyChannel(t, db, "added", "")

	other := operation_setting.Rule{
		ID: "rule-other", Name: "other",
		ChannelIDs: []int{999001},
		Node:       mkDirectNode("other", "10.0.0.7", 1080),
		ProxyURL:   "socks5://10.0.0.7:1080",
		CreatedAt:  time.Now().Unix(),
	}
	target := operation_setting.Rule{
		ID: "rule-target", Name: "target",
		ChannelIDs: []int{kept.Id, removed.Id},
		Node:       mkDirectNode("old-node", "10.0.0.8", 1080),
		ProxyURL:   oldProxy,
		CreatedAt:  time.Now().Unix(),
	}
	seedProxyRules(t, []operation_setting.Rule{other, target})

	updated, err := UpdateProxyRule(
		context.Background(), target.ID, "renamed",
		[]int{kept.Id, added.Id}, mkDirectNode("new-node", "10.0.0.9", 1080),
	)
	require.NoError(t, err)
	assert.Equal(t, "renamed", updated.Name)
	assert.Equal(t, []int{kept.Id, added.Id}, updated.ChannelIDs)
	assert.Equal(t, newProxy, updated.ProxyURL)

	// 规则清单持久化：目标规则被替换，其它规则保留，顺序保持。
	rules := LoadProxyRules()
	require.Len(t, rules, 2)
	assert.Equal(t, "rule-other", rules[0].ID)
	assert.Equal(t, "rule-target", rules[1].ID)
	assert.Equal(t, "renamed", rules[1].Name)

	// kept 渠道代理已切换到新节点；removed 渠道代理被清空；added 渠道写入新代理。
	keptCh, err := model.GetChannelById(kept.Id, true)
	require.NoError(t, err)
	assert.Equal(t, newProxy, keptCh.GetSetting().Proxy)
	removedCh, err := model.GetChannelById(removed.Id, true)
	require.NoError(t, err)
	assert.Empty(t, removedCh.GetSetting().Proxy)
	addedCh, err := model.GetChannelById(added.Id, true)
	require.NoError(t, err)
	assert.Equal(t, newProxy, addedCh.GetSetting().Proxy)
}

// 编辑不存在/空名称/渠道为空时拒绝，且不改动已保存规则。
func TestUpdateProxyRule_ValidatesInput(t *testing.T) {
	db := newProxyManageTestDB(t)
	ch := mustCreateProxyChannel(t, db, "ch", "")
	target := operation_setting.Rule{
		ID: "rule-x", Name: "x",
		ChannelIDs: []int{ch.Id},
		Node:       mkDirectNode("n", "10.0.0.8", 1080),
		ProxyURL:   "socks5://10.0.0.8:1080",
	}
	seedProxyRules(t, []operation_setting.Rule{target})

	ctx := context.Background()

	_, err := UpdateProxyRule(ctx, "missing", "n", []int{ch.Id}, mkDirectNode("n", "10.0.0.8", 1080))
	require.Error(t, err)
	_, err = UpdateProxyRule(ctx, "rule-x", "  ", []int{ch.Id}, mkDirectNode("n", "10.0.0.8", 1080))
	require.Error(t, err)
	_, err = UpdateProxyRule(ctx, "rule-x", "n", []int{}, mkDirectNode("n", "10.0.0.8", 1080))
	require.Error(t, err)

	rules := LoadProxyRules()
	require.Len(t, rules, 1)
	assert.Equal(t, "x", rules[0].Name)
}
