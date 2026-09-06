package autoscaling

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDecide(t *testing.T) {
	cfg := operation_setting.AutoScalingConfig{
		Enabled:         true,
		RPMLimit:        50,
		CooldownMinutes: 30,
		ChannelIDs:      []int{1, 3, 5},
	}
	coolStart := int64(1000)
	coolEnd := coolStart + int64(30*60) // 2800
	afterCool := coolEnd + 1

	// 已启动且全部渠道已启用、但冷却中/冷却窗内保持一致的状态
	active := AutoscalingState{LastActionAt: coolStart, ServerStarted: true, ChannelsEnabled: []int{1, 3, 5}}
	// 部分渠道已启用（缺 5），冷却窗已过
	partial := AutoscalingState{LastActionAt: coolStart, ServerStarted: true, ChannelsEnabled: []int{1, 3}}

	noChannel := cfg
	noChannel.ChannelIDs = nil

	tests := []struct {
		name  string
		cfg   operation_setting.AutoScalingConfig
		rpm   int
		state AutoscalingState
		now   int64
		want  Action
	}{
		{name: "未启用无动作", cfg: operation_setting.AutoScalingConfig{Enabled: false, RPMLimit: 50, CooldownMinutes: 30, ChannelIDs: []int{1, 3, 5}}, rpm: 100, state: AutoscalingState{}, now: afterCool, want: Action{}},
		{name: "超阈值冷启动并全部启用", cfg: cfg, rpm: 100, state: AutoscalingState{}, now: afterCool, want: Action{ShouldUpdate: true, StartServer: true, EnableChannelIDs: []int{1, 3, 5}, Reason: "rpm over limit"}},
		{name: "超阈值但冷却中不动作", cfg: cfg, rpm: 100, state: active, now: coolStart + 60, want: Action{}},
		{name: "已全启且超阈值不动作", cfg: cfg, rpm: 100, state: active, now: afterCool, want: Action{}},
		{name: "等于阈值且已收敛不动作", cfg: cfg, rpm: 50, state: active, now: afterCool, want: Action{}},
		{name: "等于阈值服务器未启动则启动", cfg: cfg, rpm: 50, state: AutoscalingState{}, now: afterCool, want: Action{ShouldUpdate: true, StartServer: true, EnableChannelIDs: []int{1, 3, 5}, Reason: "rpm over limit"}},
		{name: "冷却期满后低于阈值只关服务器渠道保持开启", cfg: cfg, rpm: 30, state: active, now: afterCool, want: Action{ShouldUpdate: true, StopServer: true, Reason: "rpm under limit"}},
		{name: "回落低于阈值但冷却中不关", cfg: cfg, rpm: 30, state: active, now: coolStart + 60, want: Action{}},
		{name: "未绑定渠道仅远端启动", cfg: noChannel, rpm: 100, state: AutoscalingState{}, now: afterCool, want: Action{ShouldUpdate: true, StartServer: true, Reason: "rpm over limit"}},
		{name: "未绑定渠道仅远端停止", cfg: noChannel, rpm: 30, state: AutoscalingState{ServerStarted: true, LastActionAt: coolStart}, now: afterCool, want: Action{ShouldUpdate: true, StopServer: true, Reason: "rpm under limit"}},
		{name: "服务器已启动仅启用全部渠道", cfg: cfg, rpm: 100, state: AutoscalingState{ServerStarted: true}, now: afterCool, want: Action{ShouldUpdate: true, EnableChannelIDs: []int{1, 3, 5}, Reason: "rpm over limit"}},
		{name: "渠道已启用仅启动服务器", cfg: cfg, rpm: 100, state: AutoscalingState{ChannelsEnabled: []int{1, 3, 5}}, now: afterCool, want: Action{ShouldUpdate: true, StartServer: true, Reason: "rpm over limit"}},
		{name: "部分渠道已启用只补缺", cfg: cfg, rpm: 100, state: partial, now: afterCool, want: Action{ShouldUpdate: true, EnableChannelIDs: []int{5}, Reason: "rpm over limit"}},
		{name: "低于阈值只关服务器渠道保持开启", cfg: cfg, rpm: 30, state: partial, now: afterCool, want: Action{ShouldUpdate: true, StopServer: true, Reason: "rpm under limit"}},
		{name: "低于阈值但服务器未启动且渠道已启用时无动作", cfg: cfg, rpm: 30, state: AutoscalingState{ChannelsEnabled: []int{1, 3, 5}}, now: afterCool, want: Action{}},
		{name: "AutoDisabled 渠道未宣称启用时补进启用动作", cfg: cfg, rpm: 100, state: AutoscalingState{ServerStarted: true, ChannelsEnabled: []int{1}}, now: afterCool, want: Action{ShouldUpdate: true, EnableChannelIDs: []int{3, 5}, Reason: "rpm over limit"}},
		{name: "ChannelIDs 为空超阈值只处理服务器", cfg: noChannel, rpm: 100, state: AutoscalingState{ServerStarted: true}, now: afterCool, want: Action{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.cfg, tt.rpm, tt.state, tt.now)
			assert.Equal(t, tt.want, got)
		})
	}
}

// setupChannelDB 用内存 sqlite 初始化 model.DB 并迁移 channels 表，供渠道动作测试使用。
func setupChannelDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
}

// TestApplyChannelAction 覆盖渠道动作的单渠道判定语义，重点是 AutoDisabled 渠道不宣称启用。
func TestApplyChannelAction(t *testing.T) {
	t.Run("无效渠道 id 跳过", func(t *testing.T) {
		succeeded, enabled := applyChannelAction(0)
		assert.False(t, succeeded)
		assert.False(t, enabled)
	})

	t.Run("渠道不存在跳过", func(t *testing.T) {
		setupChannelDB(t)
		succeeded, enabled := applyChannelAction(99)
		assert.False(t, succeeded)
		assert.False(t, enabled)
	})

	t.Run("AutoDisabled 渠道不宣称启用", func(t *testing.T) {
		setupChannelDB(t)
		require.NoError(t, model.DB.Create(&model.Channel{Id: 1, Key: "k", Name: "auto", Status: common.ChannelStatusAutoDisabled}).Error)

		succeeded, enabled := applyChannelAction(1)
		assert.True(t, succeeded)
		assert.False(t, enabled)
		// 状态保持 AutoDisabled，未被覆盖为 Enabled
		ch, err := model.GetChannelById(1, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusAutoDisabled, ch.Status)
	})

	t.Run("已启用渠道视为收敛", func(t *testing.T) {
		setupChannelDB(t)
		require.NoError(t, model.DB.Create(&model.Channel{Id: 2, Key: "k", Name: "on", Status: common.ChannelStatusEnabled}).Error)

		succeeded, enabled := applyChannelAction(2)
		assert.True(t, succeeded)
		assert.True(t, enabled)
	})
}

// TestValidateAutoScalingCooldownMinutesBounds 校验冷却窗配置的上下界，防止破坏性数值。
func TestValidateAutoScalingCooldownMinutesBounds(t *testing.T) {
	// 合法值：边界内可接受
	assert.NoError(t, operation_setting.ValidateAutoScalingCooldownMinutes("30"))
	assert.NoError(t, operation_setting.ValidateAutoScalingCooldownMinutes("1440"))
	// 超上界：超过 1 天应拒绝
	assert.Error(t, operation_setting.ValidateAutoScalingCooldownMinutes("1441"))
	assert.Error(t, operation_setting.ValidateAutoScalingCooldownMinutes("240000"))
	// 低于下限
	assert.Error(t, operation_setting.ValidateAutoScalingCooldownMinutes("0"))
	assert.Error(t, operation_setting.ValidateAutoScalingCooldownMinutes("-5"))
	// 非数字
	assert.Error(t, operation_setting.ValidateAutoScalingCooldownMinutes("abc"))
}

// setupRunEnv 初始化 runAutoScalingOnce 依赖的全局环境并注册恢复：
// model.DB（sqlite，迁移 Channel/Option/Ability）、model.LOG_DB（sqlite，迁移 Log）、
// 自动伸缩配置 atomics 与持久化状态（common.OptionMap 的 AutoScalingStateOptionKey）。
// 之后通过 runRemoteActionFn 注入远端动作 stub，即可驱动一次 runAutoScalingOnce。
func setupRunEnv(t *testing.T, cfg operation_setting.AutoScalingConfig, state AutoscalingState) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	prevDB := model.DB
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Option{}, &model.Ability{}))
	t.Cleanup(func() { model.DB = prevDB })

	prevLogDB := model.LOG_DB
	logDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, logDB.AutoMigrate(&model.Log{}))
	model.LOG_DB = logDB
	t.Cleanup(func() { model.LOG_DB = prevLogDB })

	prevCfg := operation_setting.GetAutoScalingConfig()
	require.NoError(t, operation_setting.SetAutoScalingEnabled(strconv.FormatBool(cfg.Enabled)))
	require.NoError(t, operation_setting.SetAutoScalingRPMLimit(strconv.Itoa(cfg.RPMLimit)))
	require.NoError(t, operation_setting.SetAutoScalingCooldownMinutes(strconv.Itoa(cfg.CooldownMinutes)))
	require.NoError(t, operation_setting.SetAutoScalingToken(cfg.Token))
	require.NoError(t, operation_setting.SetAutoScalingChannelIDs(joinAutoScalingIDs(cfg.ChannelIDs)))
	require.NoError(t, operation_setting.SetAutoScalingInstanceIDs(strings.Join(cfg.InstanceIDs, ",")))
	t.Cleanup(func() {
		_ = operation_setting.SetAutoScalingEnabled(strconv.FormatBool(prevCfg.Enabled))
		_ = operation_setting.SetAutoScalingRPMLimit(strconv.Itoa(prevCfg.RPMLimit))
		_ = operation_setting.SetAutoScalingCooldownMinutes(strconv.Itoa(prevCfg.CooldownMinutes))
		_ = operation_setting.SetAutoScalingToken(prevCfg.Token)
		_ = operation_setting.SetAutoScalingChannelIDs(joinAutoScalingIDs(prevCfg.ChannelIDs))
		_ = operation_setting.SetAutoScalingInstanceIDs(strings.Join(prevCfg.InstanceIDs, ","))
	})

	data, err := common.Marshal(state)
	require.NoError(t, err)
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	prevRaw := common.OptionMap[operation_setting.AutoScalingStateOptionKey]
	common.OptionMap[operation_setting.AutoScalingStateOptionKey] = string(data)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		if prevRaw == "" {
			delete(common.OptionMap, operation_setting.AutoScalingStateOptionKey)
		} else {
			common.OptionMap[operation_setting.AutoScalingStateOptionKey] = prevRaw
		}
		common.OptionMapRWMutex.Unlock()
	})
}

// joinAutoScalingIDs 把渠道 id 列表拼成逗号分隔字符串，供配置 setter 使用。
func joinAutoScalingIDs(ids []int) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	return strings.Join(parts, ",")
}

// createEnabledChannels 批量创建处于启用态的渠道，供自动伸缩测试使用。
func createEnabledChannels(t *testing.T, ids ...int) {
	t.Helper()
	for _, id := range ids {
		require.NoError(t, model.DB.Create(&model.Channel{Id: id, Key: "k", Name: "ch", Status: common.ChannelStatusEnabled}).Error)
	}
}

// TestRunAutoScalingOnceStopPartialFailureKeepsCooldown 覆盖审查发现的问题 P2-2 场景：
// 远端停止部分失败时，服务器状态不翻转、冷却不刷新，下轮按原方向重试；
// 由于停机不再禁用渠道，处于启用态的渠道保持启用不被波及。
func TestRunAutoScalingOnceStopPartialFailureKeepsCooldown(t *testing.T) {
	cfg := operation_setting.AutoScalingConfig{
		Enabled:         true,
		RPMLimit:        50,
		CooldownMinutes: 30,
		Token:           "test-token",
		ChannelIDs:      []int{1, 3, 5},
	}
	// 初始状态：服务器已启动、渠道已全部启用，LastActionAt 留 0 保证冷却已过。
	setupRunEnv(t, cfg, AutoscalingState{ServerStarted: true, ChannelsEnabled: []int{1, 3, 5}})
	createEnabledChannels(t, 1, 3, 5)

	// 注入远端 stub：停止部分失败。
	prevFn := runRemoteActionFn
	runRemoteActionFn = func(token string, act Action, instanceIDs []string) (bool, error) {
		assert.Equal(t, "test-token", token)
		assert.True(t, act.StopServer)
		return false, errors.New("1/3 instances failed")
	}
	t.Cleanup(func() { runRemoteActionFn = prevFn })

	runAutoScalingOnce()

	// 停机不操作渠道：渠道保持启用态。
	for _, id := range []int{1, 3, 5} {
		ch, err := model.GetChannelById(id, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, ch.Status)
	}
	// 冷却未刷新、服务器状态未翻转：持久化状态保持原值。
	persisted := loadState()
	assert.True(t, persisted.ServerStarted)
	assert.Equal(t, int64(0), persisted.LastActionAt)
}

// TestRunAutoScalingOnceStopSuccessRefreshesCooldown 验证正常路径：
// 远端停止全部成功时刷新冷却、翻转服务器状态，渠道保持启用不被禁用。
func TestRunAutoScalingOnceStopSuccessRefreshesCooldown(t *testing.T) {
	cfg := operation_setting.AutoScalingConfig{
		Enabled:         true,
		RPMLimit:        50,
		CooldownMinutes: 30,
		Token:           "test-token",
		ChannelIDs:      []int{1, 3, 5},
	}
	setupRunEnv(t, cfg, AutoscalingState{ServerStarted: true, ChannelsEnabled: []int{1, 3, 5}})
	createEnabledChannels(t, 1, 3, 5)

	// 注入远端 stub：停止全部成功。
	prevFn := runRemoteActionFn
	runRemoteActionFn = func(token string, act Action, instanceIDs []string) (bool, error) {
		assert.Equal(t, "test-token", token)
		assert.True(t, act.StopServer)
		return true, nil
	}
	t.Cleanup(func() { runRemoteActionFn = prevFn })

	runAutoScalingOnce()

	// 停机不操作渠道：渠道保持启用态。
	for _, id := range []int{1, 3, 5} {
		ch, err := model.GetChannelById(id, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusEnabled, ch.Status)
	}
	// 冷却已刷新、服务器状态已翻转。
	persisted := loadState()
	assert.False(t, persisted.ServerStarted)
	assert.Greater(t, persisted.LastActionAt, int64(0))
}

// TestRunAutoScalingOnceStartPartialFailureDefersChannelEnable 验证既有正确行为保留：
// 远端启动部分失败时，本轮不启用渠道（deferChannelEnable），且不刷新冷却。
func TestRunAutoScalingOnceStartPartialFailureDefersChannelEnable(t *testing.T) {
	cfg := operation_setting.AutoScalingConfig{
		Enabled:         true,
		RPMLimit:        50,
		CooldownMinutes: 30,
		Token:           "test-token",
		ChannelIDs:      []int{1, 3, 5},
	}
	// 初始状态：服务器未启动、无已启用渠道，LastActionAt 留 0 保证冷却已过。
	setupRunEnv(t, cfg, AutoscalingState{})
	// 渠道初始为禁用态：reconcile 不会把它们加入 ChannelsEnabled，Decide 启动侧将生成启用动作。
	createDisabledChannels(t, 1, 3, 5)
	// 插入 50 条消费日志使全局 RPM=50（达到阈值），触发启动侧。
	now := time.Now().Unix()
	for i := 0; i < 50; i++ {
		require.NoError(t, model.LOG_DB.Create(&model.Log{Type: model.LogTypeConsume, CreatedAt: now}).Error)
	}

	// 注入远端 stub：启动部分失败。
	prevFn := runRemoteActionFn
	runRemoteActionFn = func(token string, act Action, instanceIDs []string) (bool, error) {
		assert.True(t, act.StartServer)
		return false, errors.New("1/3 instances failed")
	}
	t.Cleanup(func() { runRemoteActionFn = prevFn })

	runAutoScalingOnce()

	// 渠道未被启用（保持 ManuallyDisabled）。
	for _, id := range []int{1, 3, 5} {
		ch, err := model.GetChannelById(id, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusManuallyDisabled, ch.Status)
	}
	// 冷却未刷新、服务器状态未翻转。
	persisted := loadState()
	assert.False(t, persisted.ServerStarted)
	assert.Equal(t, int64(0), persisted.LastActionAt)
}

// createDisabledChannels 批量创建处于手动禁用态的渠道，供自动伸缩测试使用。
func createDisabledChannels(t *testing.T, ids ...int) {
	t.Helper()
	for _, id := range ids {
		require.NoError(t, model.DB.Create(&model.Channel{Id: id, Key: "k", Name: "ch", Status: common.ChannelStatusManuallyDisabled}).Error)
	}
}

// insertConsumeLogs 向 LOG_DB 插入 count 条消费日志，使全局 RPM 达到 count。
func insertConsumeLogs(t *testing.T, count int) {
	t.Helper()
	now := time.Now().Unix()
	for i := 0; i < count; i++ {
		require.NoError(t, model.LOG_DB.Create(&model.Log{Type: model.LogTypeConsume, CreatedAt: now}).Error)
	}
}

// TestRunAutoScalingOnceUsesSelectedInstances 验证指定实例列表时，远端动作按选中的实例执行：
// runRemoteActionFn 应收到配置中的 InstanceIDs，并把它们传给远端执行器。
func TestRunAutoScalingOnceUsesSelectedInstances(t *testing.T) {
	cfg := operation_setting.AutoScalingConfig{
		Enabled:         true,
		RPMLimit:        50,
		CooldownMinutes: 30,
		Token:           "test-token",
		InstanceIDs:     []string{"uu-1", "uu-2"},
	}
	setupRunEnv(t, cfg, AutoscalingState{})
	insertConsumeLogs(t, 50)

	prevFn := runRemoteActionFn
	runRemoteActionFn = func(token string, act Action, instanceIDs []string) (bool, error) {
		assert.True(t, act.StartServer)
		assert.Equal(t, []string{"uu-1", "uu-2"}, instanceIDs)
		return true, nil
	}
	t.Cleanup(func() { runRemoteActionFn = prevFn })

	runAutoScalingOnce()

	assert.True(t, loadState().ServerStarted)
}

// TestRunAutoScalingOnceEmptyInstanceIDsFallsBackToAll 验证未配置实例列表时回退到全量操作：
// runRemoteActionFn 收到的 instanceIDs 为空。
func TestRunAutoScalingOnceEmptyInstanceIDsFallsBackToAll(t *testing.T) {
	cfg := operation_setting.AutoScalingConfig{
		Enabled:         true,
		RPMLimit:        50,
		CooldownMinutes: 30,
		Token:           "test-token",
	}
	setupRunEnv(t, cfg, AutoscalingState{})
	insertConsumeLogs(t, 50)

	prevFn := runRemoteActionFn
	runRemoteActionFn = func(token string, act Action, instanceIDs []string) (bool, error) {
		assert.True(t, act.StartServer)
		assert.Empty(t, instanceIDs)
		return true, nil
	}
	t.Cleanup(func() { runRemoteActionFn = prevFn })

	runAutoScalingOnce()

	assert.True(t, loadState().ServerStarted)
}

// TestSetAutoScalingInstanceIDs 覆盖实例 uuid 列表配置的解析：空串置 nil、去重、跳过空段与 trim。
func TestSetAutoScalingInstanceIDs(t *testing.T) {
	prevCfg := operation_setting.GetAutoScalingConfig()
	t.Cleanup(func() {
		_ = operation_setting.SetAutoScalingInstanceIDs(strings.Join(prevCfg.InstanceIDs, ","))
	})

	t.Run("空串置 nil", func(t *testing.T) {
		require.NoError(t, operation_setting.SetAutoScalingInstanceIDs(""))
		assert.Nil(t, operation_setting.GetAutoScalingConfig().InstanceIDs)
	})

	t.Run("解析并 trim", func(t *testing.T) {
		require.NoError(t, operation_setting.SetAutoScalingInstanceIDs(" uu-1 , uu-2 ,uu-3 "))
		assert.Equal(t, []string{"uu-1", "uu-2", "uu-3"}, operation_setting.GetAutoScalingConfig().InstanceIDs)
	})

	t.Run("去重保留首次出现", func(t *testing.T) {
		require.NoError(t, operation_setting.SetAutoScalingInstanceIDs("aa,bb,aa,cc"))
		assert.Equal(t, []string{"aa", "bb", "cc"}, operation_setting.GetAutoScalingConfig().InstanceIDs)
	})

	t.Run("全部为空段置 nil", func(t *testing.T) {
		require.NoError(t, operation_setting.SetAutoScalingInstanceIDs(" , , "))
		assert.Nil(t, operation_setting.GetAutoScalingConfig().InstanceIDs)
	})
}

// TestRunRemoteActionInstanceSelection 直接覆盖 runRemoteAction 自身的分流：
// 选中实例非空时只对选中的 uuid 发 power 请求，而为空时先 list 拿全部 uuid 再请求全部。
// 通过注入 autoDLClientFactory 把 AutoDL 客户端 baseURL 指向假服务器驱动真实分流，
// 不依赖 runRemoteActionFn 的 stub（后者只验证了 instanceIDs 被传入注入点）。
func TestRunRemoteActionInstanceSelection(t *testing.T) {
	listBody := map[string]interface{}{
		"list": []map[string]interface{}{
			{"uuid": "pro-1", "name": "worker-a", "status": "stopped"},
			{"uuid": "pro-2", "name": "worker-b", "status": "stopped"},
			{"uuid": "pro-3", "name": "worker-c", "status": "stopped"},
		},
	}

	tests := []struct {
		name        string
		stop        bool
		instanceIDs []string
		// wantPowered 是期望被发出 power 请求的 uuid 集合（请求到达顺序）。
		wantPowered []string
	}{
		{name: "启动非空只操作选中实例", stop: false, instanceIDs: []string{"uu-1", "uu-2"}, wantPowered: []string{"uu-1", "uu-2"}},
		{name: "启动为空回退操作全部", stop: false, instanceIDs: nil, wantPowered: []string{"pro-1", "pro-2", "pro-3"}},
		{name: "停止非空只操作选中实例", stop: true, instanceIDs: []string{"uu-2"}, wantPowered: []string{"uu-2"}},
		{name: "停止为空回退操作全部", stop: true, instanceIDs: nil, wantPowered: []string{"pro-1", "pro-2", "pro-3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			powerPath := autoDLPowerOnPath
			if tt.stop {
				powerPath = autoDLPowerOffPath
			}
			var operated []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case autoDLListPath:
					writeAutoDLResponse(t, w, "Success", "", listBody)
				case powerPath:
					operated = append(operated, readJSONBody(t, r)["instance_uuid"].(string))
					writeAutoDLResponse(t, w, "Success", "", nil)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			prevFactory := autoDLClientFactory
			autoDLClientFactory = func(token string) *AutoDLClient {
				return &AutoDLClient{token: token, baseURL: server.URL}
			}
			t.Cleanup(func() { autoDLClientFactory = prevFactory })

			act := Action{}
			if tt.stop {
				act.StopServer = true
			} else {
				act.StartServer = true
			}
			ok, err := runRemoteAction("test-token", act, tt.instanceIDs)
			require.NoError(t, err)
			assert.True(t, ok)
			assert.Equal(t, tt.wantPowered, operated)
		})
	}
}
