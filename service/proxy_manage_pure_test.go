// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeChannelIDs(t *testing.T) {
	t.Run("去重保序", func(t *testing.T) {
		got, err := normalizeChannelIDs([]int{3, 1, 1, 2, 3})
		require.NoError(t, err)
		assert.Equal(t, []int{3, 1, 2}, got)
	})
	t.Run("非正数拒绝", func(t *testing.T) {
		_, err := normalizeChannelIDs([]int{1, -2})
		require.Error(t, err)
	})
	t.Run("空列表拒绝", func(t *testing.T) {
		_, err := normalizeChannelIDs([]int{})
		require.Error(t, err)
	})
	t.Run("超过上限拒绝", func(t *testing.T) {
		ids := make([]int, maxRuleChannels+1)
		for i := range ids {
			ids[i] = i + 1
		}
		_, err := normalizeChannelIDs(ids)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "上限")
	})
	t.Run("恰好等于上限通过", func(t *testing.T) {
		ids := make([]int, maxRuleChannels)
		for i := range ids {
			ids[i] = i + 1
		}
		got, err := normalizeChannelIDs(ids)
		require.NoError(t, err)
		require.Len(t, got, maxRuleChannels)
	})
}

func TestIsSameProxy(t *testing.T) {
	assert.True(t, isSameProxy("http://1.2.3.4:8080", "http://1.2.3.4:8080"))
	assert.False(t, isSameProxy("http://1.2.3.4:8080", "socks5://1.2.3.4:8080"))
	assert.False(t, isSameProxy("http://1.2.3.4:8080", ""))
	assert.True(t, isSameProxy("", ""))
	assert.False(t, isSameProxy("not a proxy", "http://1.2.3.4:8080"))
}

func TestStripTakenChannels(t *testing.T) {
	mkRule := func(id string, proxy string, channels []int) operation_setting.Rule {
		return operation_setting.Rule{ID: id, ProxyURL: proxy, ChannelIDs: channels}
	}

	t.Run("无现存规则返回空", func(t *testing.T) {
		out := stripTakenChannels([]int{1}, nil, map[int]string{})
		assert.Empty(t, out)
	})

	t.Run("取出匹配渠道保留其余", func(t *testing.T) {
		rules := []operation_setting.Rule{mkRule("A", "http://1.2.3.4:8080", []int{1, 2, 3})}
		// 新规则占用渠道 2，且渠道 2 当前代理等于 A 的 ProxyURL -> 从 A 移除。
		out := stripTakenChannels([]int{2}, rules, map[int]string{2: "http://1.2.3.4:8080"})
		require.Len(t, out, 1)
		assert.Equal(t, []int{1, 3}, out[0].ChannelIDs)
	})

	t.Run("渠道当前代理不等于规则值时不移除", func(t *testing.T) {
		rules := []operation_setting.Rule{mkRule("A", "http://1.2.3.4:8080", []int{1, 2})}
		// 渠道 2 当前代理被其它规则覆盖为 socks5，不匹配 A -> A 保留渠道 2。
		out := stripTakenChannels([]int{2}, rules, map[int]string{2: "socks5://5.5.5.5:1080"})
		require.Len(t, out, 1)
		assert.Equal(t, []int{1, 2}, out[0].ChannelIDs)
	})

	t.Run("规则渠道被清空后删除", func(t *testing.T) {
		rules := []operation_setting.Rule{
			mkRule("A", "http://1.2.3.4:8080", []int{1}),
			mkRule("B", "socks5://5.5.5.5:1080", []int{2, 3}),
		}
		out := stripTakenChannels([]int{1}, rules, map[int]string{1: "http://1.2.3.4:8080"})
		require.Len(t, out, 1)
		assert.Equal(t, "B", out[0].ID)
		assert.Equal(t, []int{2, 3}, out[0].ChannelIDs)
	})

	t.Run("多规则各自移除相关渠道", func(t *testing.T) {
		rules := []operation_setting.Rule{
			mkRule("A", "http://1.2.3.4:8080", []int{1}),
			mkRule("B", "socks5://5.5.5.5:1080", []int{2}),
		}
		// 新规则占用渠道 1、2，且两渠道当前代理分别等于 A、B 的 ProxyURL。
		out := stripTakenChannels([]int{1, 2}, rules, map[int]string{1: "http://1.2.3.4:8080", 2: "socks5://5.5.5.5:1080"})
		assert.Empty(t, out) // 两条规则都被清空并被删除
	})
}