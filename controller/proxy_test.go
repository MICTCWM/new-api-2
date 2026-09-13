// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package controller

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var proxyOptionKeys = []string{
	operation_setting.ProxySubscriptionURLOptionKey,
	operation_setting.ProxySubscriptionDataOptionKey,
	operation_setting.ProxyRulesOptionKey,
}

// setupProxyTestEnv 初始化代理功能测试所需的隔离环境：
// 内存 sqlite（Channel/Option/Ability）、关闭内存渠道缓存、初始化 http client（使订阅抓取可访问 httptest）、
// 关闭 SSRF 劫持本地测试服务，并快照/恢复代理相关 option key。
func setupProxyTestEnv(t *testing.T) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	prevDB := model.DB
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Option{}, &model.Ability{}))
	t.Cleanup(func() { model.DB = prevDB })

	prevMem := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = prevMem })

	service.InitHttpClient()

	fetchSetting := system_setting.GetFetchSetting()
	prevFetch := *fetchSetting
	fetchSetting.EnableSSRFProtection = false
	t.Cleanup(func() { *fetchSetting = prevFetch })

	// 快照并恢复代理 option。
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	prev := make(map[string]string, len(proxyOptionKeys))
	for _, k := range proxyOptionKeys {
		prev[k] = common.OptionMap[k]
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		for _, k := range proxyOptionKeys {
			if prev[k] == "" {
				delete(common.OptionMap, k)
			} else {
				common.OptionMap[k] = prev[k]
			}
		}
	})
}

// newGinContext 构造一个可直接调用 controller handler 的 gin 测试上下文。
func newGinContext(method, target, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	if len(params) > 0 {
		c.Params = params
	}
	return c, rec
}

type apiPayload struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func decodePayload(t *testing.T, rec *httptest.ResponseRecorder) apiPayload {
	t.Helper()
	var p apiPayload
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &p))
	return p
}

func createChannel(t *testing.T, id int) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.Channel{Id: id, Key: "k", Name: "ch"}).Error)
}

func channelProxy(t *testing.T, id int) string {
	t.Helper()
	ch, err := model.GetChannelById(id, true)
	require.NoError(t, err)
	return ch.GetSetting().Proxy
}

// newSubscriptionServer 返回一个用固定内容响应 GET 的订阅服务器。
func newSubscriptionServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mixedSubscription() []byte {
	plain := strings.Join([]string{
		"ss://aes-128-gcm:abc@1.2.3.4:8388#ss-node",
		"socks5://user:pass@6.7.8.9:1080#socks5-node",
		"http://1.2.3.4:8080#http-node",
	}, "\n")
	return []byte(base64.StdEncoding.EncodeToString([]byte(plain)))
}

func clashSubscription() []byte {
	return []byte(`proxies:
  - name: http-node
    type: http
    server: 2.2.2.2
    port: 8080
  - name: socks5-node
    type: socks5
    server: 3.3.3.3
    port: 1080
  - name: ss-node
    type: ss
    server: 1.1.1.1
    port: 8388
    cipher: aes-128-gcm
`)
}

func TestProxySubscriptionFlowBase64(t *testing.T) {
	setupProxyTestEnv(t)
	srv := newSubscriptionServer(t, mixedSubscription())

	c, rec := newGinContext(http.MethodPost, "/api/proxy/subscription", `{"url":"`+srv.URL+`"}`, nil)
	UpdateProxySubscription(c)
	require.Equal(t, http.StatusOK, rec.Code)
	p := decodePayload(t, rec)
	require.True(t, p.Success)
	data := p.Data.(map[string]interface{})
	assert.Equal(t, float64(3), data["node_count"])
	assert.Equal(t, float64(3), data["bindable_count"])

	// GET status 能读回保存的 URL 与节点缓存。
	c, rec = newGinContext(http.MethodGet, "/api/proxy/status", "", nil)
	GetProxyStatus(c)
	p = decodePayload(t, rec)
	require.True(t, p.Success)
	sd := p.Data.(map[string]interface{})
	assert.Equal(t, srv.URL, sd["subscription_url"])
	nodes := sd["nodes"].([]interface{})
	assert.Len(t, nodes, 3)
}

func TestProxySubscriptionFlowClashYAML(t *testing.T) {
	setupProxyTestEnv(t)
	srv := newSubscriptionServer(t, clashSubscription())

	c, rec := newGinContext(http.MethodPost, "/api/proxy/subscription", `{"url":"`+srv.URL+`"}`, nil)
	UpdateProxySubscription(c)
	require.Equal(t, http.StatusOK, rec.Code)
	p := decodePayload(t, rec)
	require.True(t, p.Success)
	data := p.Data.(map[string]interface{})
	assert.Equal(t, float64(3), data["node_count"])
	assert.Equal(t, float64(2), data["bindable_count"])
}

func TestProxyRuleLifecycle(t *testing.T) {
	setupProxyTestEnv(t)
	for _, id := range []int{1, 2, 3} {
		createChannel(t, id)
	}

	// 创建规则 A：渠道 1、2 -> http://1.2.3.4:8080
	c, rec := newGinContext(http.MethodPost, "/api/proxy/rules",
		`{"name":"A","channel_ids":[1,2],"node":{"type":"http","server":"1.2.3.4","port":8080}}`, nil)
	CreateProxyRule(c)
	require.Equal(t, http.StatusOK, rec.Code)
	p := decodePayload(t, rec)
	require.True(t, p.Success)
	ruleA := p.Data.(map[string]interface{})["rule"].(map[string]interface{})
	ruleAID := ruleA["id"].(string)
	assert.Equal(t, "http://1.2.3.4:8080", channelProxy(t, 1))
	assert.Equal(t, "http://1.2.3.4:8080", channelProxy(t, 2))

	// 创建规则 B：渠道 2、3 -> socks5://5.5.5.5:1080。后写覆盖：渠道 2 从 A 移除，代理转交 B。
	c, rec = newGinContext(http.MethodPost, "/api/proxy/rules",
		`{"name":"B","channel_ids":[2,3],"node":{"type":"socks5","server":"5.5.5.5","port":1080}}`, nil)
	CreateProxyRule(c)
	p = decodePayload(t, rec)
	require.True(t, p.Success)
	assert.Equal(t, "socks5://5.5.5.5:1080", channelProxy(t, 2))
	assert.Equal(t, "socks5://5.5.5.5:1080", channelProxy(t, 3))

	// ruleA 的渠道应已移除渠道 2。
	c, rec = newGinContext(http.MethodGet, "/api/proxy/status", "", nil)
	GetProxyStatus(c)
	p = decodePayload(t, rec)
	require.True(t, p.Success)
	rules := p.Data.(map[string]interface{})["rules"].([]interface{})
	require.Len(t, rules, 2)
	for _, r := range rules {
		rm := r.(map[string]interface{})
		if rm["id"] == ruleAID {
			assert.Equal(t, []interface{}{float64(1)}, rm["channel_ids"])
		}
	}
	ruleBID := ""
	for _, r := range rules {
		rm := r.(map[string]interface{})
		if rm["name"] == "B" {
			ruleBID = rm["id"].(string)
		}
	}
	require.NotEmpty(t, ruleBID)

	// 删除规则 B：渠道 2、3 当前代理均为 B 的 socks5 -> 被清空；渠道 1 不受影响。
	c, rec = newGinContext(http.MethodDelete, "/api/proxy/rules/"+ruleBID, "", gin.Params{{Key: "id", Value: ruleBID}})
	DeleteProxyRule(c)
	p = decodePayload(t, rec)
	require.True(t, p.Success)
	assert.Equal(t, "", channelProxy(t, 2))
	assert.Equal(t, "", channelProxy(t, 3))
	assert.Equal(t, "http://1.2.3.4:8080", channelProxy(t, 1))
}

func TestProxyRuleDeleteOnlyClearsMatching(t *testing.T) {
	setupProxyTestEnv(t)
	for _, id := range []int{1, 2} {
		createChannel(t, id)
	}

	c, rec := newGinContext(http.MethodPost, "/api/proxy/rules",
		`{"name":"C","channel_ids":[1,2],"node":{"type":"http","server":"9.9.9.9","port":80}}`, nil)
	CreateProxyRule(c)
	p := decodePayload(t, rec)
	require.True(t, p.Success)
	ruleID := p.Data.(map[string]interface{})["rule"].(map[string]interface{})["id"].(string)
	assert.Equal(t, "http://9.9.9.9:80", channelProxy(t, 1))
	assert.Equal(t, "http://9.9.9.9:80", channelProxy(t, 2))

	// 手动改掉渠道 1 的代理（模拟手动配置）后删除规则：渠道 2 被清空，渠道 1 不被误清。
	ch, err := model.GetChannelById(1, true)
	require.NoError(t, err)
	setting := ch.GetSetting()
	setting.Proxy = "http://manual:3128"
	ch.SetSetting(setting)
	require.NoError(t, ch.Update())

	c, rec = newGinContext(http.MethodDelete, "/api/proxy/rules/"+ruleID, "", gin.Params{{Key: "id", Value: ruleID}})
	DeleteProxyRule(c)
	p = decodePayload(t, rec)
	require.True(t, p.Success)
	assert.Equal(t, "http://manual:3128", channelProxy(t, 1))
	assert.Equal(t, "", channelProxy(t, 2))
}

func TestProxyRefreshKeepsRules(t *testing.T) {
	setupProxyTestEnv(t)
	createChannel(t, 1)
	srv := newSubscriptionServer(t, mixedSubscription())

	c, rec := newGinContext(http.MethodPost, "/api/proxy/subscription", `{"url":"`+srv.URL+`"}`, nil)
	UpdateProxySubscription(c)
	require.True(t, decodePayload(t, rec).Success)

	c, rec = newGinContext(http.MethodPost, "/api/proxy/rules",
		`{"name":"A","channel_ids":[1],"node":{"type":"http","server":"7.7.7.7","port":80}}`, nil)
	CreateProxyRule(c)
	require.True(t, decodePayload(t, rec).Success)

	// 重新拉取订阅：节点覆盖，规则保留。
	c, rec = newGinContext(http.MethodPost, "/api/proxy/refresh", "", nil)
	RefreshProxySubscription(c)
	require.True(t, decodePayload(t, rec).Success)

	c, rec = newGinContext(http.MethodGet, "/api/proxy/status", "", nil)
	GetProxyStatus(c)
	p := decodePayload(t, rec)
	require.True(t, p.Success)
	sd := p.Data.(map[string]interface{})
	assert.Len(t, sd["nodes"].([]interface{}), 3)
	require.Len(t, sd["rules"].([]interface{}), 1)
}

func TestProxyCreateRuleRejectsInvalidNode(t *testing.T) {
	setupProxyTestEnv(t)
	createChannel(t, 1)

	// vmess 节点不可绑定，应被拒绝（不信任客户端 ProxyURL）。
	c, rec := newGinContext(http.MethodPost, "/api/proxy/rules",
		`{"name":"bad","channel_ids":[1],"node":{"type":"vmess","server":"1.1.1.1","port":443}}`, nil)
	CreateProxyRule(c)
	p := decodePayload(t, rec)
	assert.False(t, p.Success)
	assert.Equal(t, "", channelProxy(t, 1))
}

func TestProxyCreateRuleChannelNotFound(t *testing.T) {
	setupProxyTestEnv(t)
	// 渠道 99 不存在。
	c, rec := newGinContext(http.MethodPost, "/api/proxy/rules",
		`{"name":"X","channel_ids":[99],"node":{"type":"http","server":"1.1.1.1","port":80}}`, nil)
	CreateProxyRule(c)
	p := decodePayload(t, rec)
	assert.False(t, p.Success)
}

func TestProxyCreateRuleRejectsMalformedServer(t *testing.T) {
	setupProxyTestEnv(t)
	createChannel(t, 1)

	// server 含路径（如订阅 yaml 中的 "/etc/passwd"）会在归一化后产生 "http:///etc/passwd:8080"，
	// 该类畸形地址不应被写入渠道（P2-1）。
	c, rec := newGinContext(http.MethodPost, "/api/proxy/rules",
		`{"name":"bad","channel_ids":[1],"node":{"type":"http","server":"/etc/passwd","port":8080}}`, nil)
	CreateProxyRule(c)
	p := decodePayload(t, rec)
	assert.False(t, p.Success)
	assert.Contains(t, p.Message, "节点代理地址无效")
	assert.Equal(t, "", channelProxy(t, 1))

	// server 含前后空白，解析端应 TrimSpace 后得到正常代理地址（P2-1）。
	c, rec = newGinContext(http.MethodPost, "/api/proxy/rules",
		`{"name":"ok","channel_ids":[1],"node":{"type":"http","server":"  1.2.3.4  ","port":8080}}`, nil)
	CreateProxyRule(c)
	p = decodePayload(t, rec)
	require.True(t, p.Success)
	assert.Equal(t, "http://1.2.3.4:8080", channelProxy(t, 1))
}

func TestProxyCreateRuleChannelCountLimit(t *testing.T) {
	setupProxyTestEnv(t)
	// 构造超过单条规则渠道数量上限（service.maxRuleChannels=500）的渠道，创建规则应被拒绝且不落库、不改渠道。
	const maxRuleChannels = 500
	for i := 1; i <= maxRuleChannels+1; i++ {
		createChannel(t, i)
	}
	ids := make([]string, 0, maxRuleChannels+1)
	for i := 1; i <= maxRuleChannels+1; i++ {
		ids = append(ids, strconv.Itoa(i))
	}
	body := `{"name":"many","channel_ids":[` + strings.Join(ids, ",") +
		`],"node":{"type":"http","server":"1.1.1.1","port":80}}`
	c, rec := newGinContext(http.MethodPost, "/api/proxy/rules", body, nil)
	CreateProxyRule(c)
	p := decodePayload(t, rec)
	assert.False(t, p.Success)
	assert.Contains(t, p.Message, "上限")
	assert.Equal(t, "", channelProxy(t, 1))
	c, rec = newGinContext(http.MethodGet, "/api/proxy/status", "", nil)
	GetProxyStatus(c)
	p = decodePayload(t, rec)
	require.True(t, p.Success)
	require.Empty(t, p.Data.(map[string]interface{})["rules"].([]interface{}))
}

func TestProxySubscriptionNodeCountLimit(t *testing.T) {
	setupProxyTestEnv(t)
	var b strings.Builder
	for i := 0; i <= 5000; i++ {
		b.WriteString("http://node")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(".example.com:8080\n")
	}
	srv := newSubscriptionServer(t, []byte(b.String()))
	c, rec := newGinContext(http.MethodPost, "/api/proxy/subscription", `{"url":"`+srv.URL+`"}`, nil)
	UpdateProxySubscription(c)
	p := decodePayload(t, rec)
	assert.False(t, p.Success)
	assert.Contains(t, p.Message, "上限")
	assert.Equal(t, "", service.GetProxySubscriptionURL())
}

func TestProxyClearSubscriptionKeepsRules(t *testing.T) {
	setupProxyTestEnv(t)
	createChannel(t, 1)
	srv := newSubscriptionServer(t, mixedSubscription())

	c, rec := newGinContext(http.MethodPost, "/api/proxy/subscription", `{"url":"`+srv.URL+`"}`, nil)
	UpdateProxySubscription(c)
	require.True(t, decodePayload(t, rec).Success)
	c, rec = newGinContext(http.MethodPost, "/api/proxy/rules",
		`{"name":"A","channel_ids":[1],"node":{"type":"http","server":"7.7.7.7","port":80}}`, nil)
	CreateProxyRule(c)
	require.True(t, decodePayload(t, rec).Success)

	c, rec = newGinContext(http.MethodDelete, "/api/proxy/subscription", "", nil)
	ClearProxySubscription(c)
	require.True(t, decodePayload(t, rec).Success)

	c, rec = newGinContext(http.MethodGet, "/api/proxy/status", "", nil)
	GetProxyStatus(c)
	p := decodePayload(t, rec)
	require.True(t, p.Success)
	sd := p.Data.(map[string]interface{})
	assert.Equal(t, "", sd["subscription_url"])
	assert.Empty(t, sd["nodes"])
	assert.Len(t, sd["rules"].([]interface{}), 1)
}