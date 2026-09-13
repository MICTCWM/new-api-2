package autoscaling

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// AutoscalingState 是自动伸缩状态机的持久化形态，以 JSON 字符串存入 options 表
// （键 AutoScalingState），供服务重启后恢复。
type AutoscalingState struct {
	LastActionAt    int64 `json:"last_action_at"`
	ServerStarted   bool  `json:"server_started"`
	ChannelsEnabled []int `json:"channels_enabled,omitempty"`
}

// Action 是状态机在单轮决策中决定要执行的动作集合。
// EnableChannelIDs 为 nil 表示无渠道启用动作。
type Action struct {
	ShouldUpdate     bool
	StartServer      bool
	StopServer       bool
	EnableChannelIDs []int
	Reason           string
}

// Decide 是自动伸缩的纯决策函数，便于单元测试：
//
//	未启用 -> 无动作
//	全局 RPM 达到或超过阈值 -> 冷启动远端服务器 + 启用所有选中的渠道（受冷却窗约束）
//	全局 RPM 低于阈值 -> 关闭远端服务器（受冷却窗约束），渠道保持开启
//
// 冷却窗：last_action_at 起的 CooldownMinutes 分钟内不再产生任何动作，防止 49<->50 抖动。
// 渠道收敛判定基于 cfg.ChannelIDs 与 state.ChannelsEnabled：cfg 选中的渠道全部已启用即视为收敛。
func Decide(cfg operation_setting.AutoScalingConfig, rpm int, state AutoscalingState, now int64) Action {
	act := Action{}
	if !cfg.Enabled {
		return act
	}
	cooldownEnd := state.LastActionAt + int64(cfg.CooldownMinutes*60)
	enabledSet := make(map[int]struct{}, len(state.ChannelsEnabled))
	for _, id := range state.ChannelsEnabled {
		enabledSet[id] = struct{}{}
	}
	if rpm >= cfg.RPMLimit {
		// 收敛判定：服务器已启动且 cfg 选中的渠道全部已启用（cfg 为空时渠道部分视为已收敛）。
		// 等于阈值时也走本侧：已收敛则无动作（保持现状），未启动则启动，避免等于阈值被当作"低于"整体停机。
		converged := state.ServerStarted
		if converged {
			for _, id := range cfg.ChannelIDs {
				if _, ok := enabledSet[id]; !ok {
					converged = false
					break
				}
			}
		}
		if converged {
			return act
		}
		if now < cooldownEnd {
			return act
		}
		if !state.ServerStarted {
			act.StartServer = true
		}
		for _, id := range cfg.ChannelIDs {
			if _, ok := enabledSet[id]; !ok {
				act.EnableChannelIDs = append(act.EnableChannelIDs, id)
			}
		}
		if act.StartServer || len(act.EnableChannelIDs) > 0 {
			act.ShouldUpdate = true
			act.Reason = "rpm over limit"
		}
		return act
	}
	if !state.ServerStarted && len(state.ChannelsEnabled) == 0 {
		return act
	}
	if now < cooldownEnd {
		return act
	}
	if state.ServerStarted {
		act.StopServer = true
	}
	// 语义：RPM 低于阈值时只关闭远端实例，渠道保持开启。
	// 自动扩容只负责实例的启停，渠道的启用/禁用由管理员或其他机制管理，
	// 停机不应波及已在正常服务的渠道，避免把流量整体摘除。
	if act.StopServer {
		act.ShouldUpdate = true
		act.Reason = "rpm under limit"
	}
	return act
}

// StartAutoScalingLoop 每分钟轮询一次全局 RPM 并驱动自动伸缩状态机。
// 仅在 master 节点运行：多节点部署时 slave 不参与决策，避免多节点重复决策
// 竞争写 options 表（与项目其它全局循环保持一致）。
func StartAutoScalingLoop() {
	if !common.IsMasterNode {
		return
	}
	for {
		runAutoScalingOnce()
		time.Sleep(time.Minute)
	}
}

func runAutoScalingOnce() {
	cfg := operation_setting.GetAutoScalingConfig()
	if !cfg.Enabled {
		return
	}
	now := time.Now().Unix()
	loaded := loadState()
	rpm, ok := getGlobalRPM()
	if !ok {
		logger.LogWarn(nil, "auto scaling: query global rpm failed, skip this round")
		return
	}
	state, unusable := reconcileChannelState(loaded, cfg)
	// 过滤掉不可用渠道（不存在或 AutoDisabled）：它们不参与"需启用"计算与收敛检查，
	// 避免 Decide 为这些渠道无限生成启用动作。AutoDisabled 渠道因此不会被强制启用，
	// 也不会在低于阈值时被禁用。
	effectiveCfg := cfg
	if len(unusable) > 0 {
		unusableSet := make(map[int]struct{}, len(unusable))
		for _, id := range unusable {
			unusableSet[id] = struct{}{}
		}
		usable := make([]int, 0, len(cfg.ChannelIDs))
		for _, id := range cfg.ChannelIDs {
			if _, ok := unusableSet[id]; !ok {
				usable = append(usable, id)
			}
		}
		effectiveCfg.ChannelIDs = usable
	}
	act := Decide(effectiveCfg, rpm, state, now)
	if !act.ShouldUpdate {
		// 无动作轮次也可能已由 reconcile 校正状态；若与持久化原值不同，则以低频方式写入一次，
		// 避免持久值长期滞后。reconcile 后状态稳定，下一次轮询值相同，不会再触发写入。
		if !reflect.DeepEqual(state, loaded) {
			if err := persistState(state); err != nil {
				logger.LogError(nil, fmt.Sprintf("auto scaling: persist reconciled state failed: %v", err))
			}
		}
		return
	}

	// 决策后、执行前：记录「检测到什么」。
	if act.StartServer {
		logger.LogInfo(nil, fmt.Sprintf("auto scaling: 检测到全局请求频率 RPM=%d 达到阈值 %d，开始扩容（发送开机命令 + 启用渠道）", rpm, cfg.RPMLimit))
	}
	if act.StopServer {
		logger.LogInfo(nil, fmt.Sprintf("auto scaling: 检测到全局请求频率 RPM=%d 低于阈值 %d，开始缩容（发送关机命令）", rpm, cfg.RPMLimit))
	}

	newState := state
	anySucceeded := false
	// remoteSucceeded 记录本轮远端动作（StartServer/StopServer）是否全部成功。
	// 存在远端动作但未全部成功时，渠道动作即使成功也不刷新冷却，避免停止重试被冷却窗推迟
	// （实例继续计费）；渠道动作本身仍照常执行（更安全）。
	remoteSucceeded := true
	// deferChannelEnable 标记远端启动未成功，本轮跳过渠道启用：
	// 避免无实例可承接流量时启用渠道，也避免渠道启用成功刷新冷却导致启动不再重试。
	deferChannelEnable := false

	if act.StartServer || act.StopServer {
		if act.StartServer {
			logger.LogInfo(nil, "auto scaling: 向 AutoDL 发送开机命令")
		}
		if act.StopServer {
			logger.LogInfo(nil, "auto scaling: 向 AutoDL 发送关机命令")
		}
		ok, err := runRemoteActionFn(cfg.Token, act, cfg.InstanceIDs)
		if err != nil {
			if errors.Is(err, ErrNoInstance) {
				logger.LogWarn(nil, fmt.Sprintf("auto scaling: %s, no available AutoDL instance, skip remote action", act.Reason))
			} else {
				logger.LogError(nil, fmt.Sprintf("auto scaling: remote %s action failed: %v", act.Reason, err))
			}
		}
		// 只有全部实例操作成功（ok）才翻转服务器状态并计入 anySucceeded（刷新冷却）；
		// 部分失败（err!=nil）或 ErrNoInstance 时不翻转状态、不计入 anySucceeded，
		// 下一轮自动按原方向重试。
		remoteSucceeded = ok
		if ok {
			anySucceeded = true
			if act.StartServer {
				newState.ServerStarted = true
				logger.LogInfo(nil, "auto scaling: started AutoDL instances")
				recordUsageLog(fmt.Sprintf("自动伸缩：已启动 %s 个 AutoDL 实例", instanceCountText(cfg.InstanceIDs)))
			}
			if act.StopServer {
				newState.ServerStarted = false
				logger.LogInfo(nil, "auto scaling: stopped AutoDL instances")
				recordUsageLog(fmt.Sprintf("自动伸缩：已关闭 %s 个 AutoDL 实例", instanceCountText(cfg.InstanceIDs)))
			}
		} else if act.StartServer {
			// 远端启动未成功（含无实例）：本轮不启用渠道，随启动一起在下轮重试。
			deferChannelEnable = true
			logger.LogInfo(nil, "auto scaling: server not started, defer channel enable")
		}
	}

	if !deferChannelEnable {
		for _, id := range act.EnableChannelIDs {
			logger.LogInfo(nil, fmt.Sprintf("auto scaling: 为渠道 %d 执行启用", id))
			succeeded, enabled := applyChannelAction(id)
			if !succeeded {
				continue
			}
			// 远端未全部成功时渠道成功不计入 anySucceeded（不刷新冷却），下轮随远端一起重试。
			if remoteSucceeded {
				anySucceeded = true
			}
			if enabled {
				newState.ChannelsEnabled = appendChannelID(newState.ChannelsEnabled, id)
				logger.LogInfo(nil, fmt.Sprintf("auto scaling: enabled channel %d", id))
			} else {
				// 渠道处于 ChannelStatusAutoDisabled：保持禁用语义、不宣称已启用，
				// 视为收敛成功避免无效重试，此处仅 Info 记录一次。
				logger.LogInfo(nil, fmt.Sprintf("auto scaling: channel %d stays auto disabled, kept disabled", id))
			}
		}
	}

	// 只有任一主动作成功（或远端成功、或渠道成功）才刷新冷却时间，防止抖动。
	if anySucceeded {
		newState.LastActionAt = time.Now().Unix()
		if err := persistState(newState); err != nil {
			logger.LogError(nil, fmt.Sprintf("auto scaling: persist state failed: %v", err))
		}
	} else if act.ShouldUpdate {
		logger.LogWarn(nil, "auto scaling: no action succeeded this round, retrying on next tick")
	}
}

// runRemoteActionFn 是远端动作执行器的可注入入口，生产环境指向 runRemoteAction。
// 测试可替换为 stub 以验证状态机对远端部分失败的处理（避免访问真实 AutoDL）。
var runRemoteActionFn = runRemoteAction

// recordUsageLog 在自动伸缩远端动作成功后写一条使用日志（LogTypeConsume）。
// 生产环境指向 model.RecordAutoScalingLog（始终写入，不受 LogConsumeEnabled 限制，
// 保证管理员无论是否开启消费日志都能在使用日志页看到自动伸缩动作时间线）。
// 测试可替换为 stub 以断言写入参数，无需依赖日志库环境。
var recordUsageLog = model.RecordAutoScalingLog

// instanceCountText 生成自动伸缩日志中的实例数量描述：
// 配置了具体实例时用数量，否则用「全部」。
func instanceCountText(instanceIDs []string) string {
	if len(instanceIDs) > 0 {
		return strconv.Itoa(len(instanceIDs))
	}
	return "全部"
}

// autoDLClientFactory 是 AutoDL 客户端的构造入口，生产环境指向 NewAutoDLClient。
// 测试可替换以把客户端 baseURL 指向假服务器，从而直接驱动 runRemoteAction 自身分流。
var autoDLClientFactory = NewAutoDLClient

// runRemoteAction 执行远端开始/停止实例的操作。instanceIDs 非空时只操作选中的实例，
// 为空时回退到操作全部实例（向后兼容：老配置未勾选实例时仍操作全部）。
// 返回 (是否全部成功, error)：err==nil 即全部成功；返回聚合错误或 ErrNoInstance 时视为未全部成功，
// 调用方据此决定是否翻转服务器状态与刷新冷却。
func runRemoteAction(token string, act Action, instanceIDs []string) (bool, error) {
	client := autoDLClientFactory(token)
	if act.StartServer {
		if len(instanceIDs) > 0 {
			_, err := client.StartInstances(instanceIDs)
			return err == nil, err
		}
		_, err := client.StartAllInstances()
		return err == nil, err
	}
	if act.StopServer {
		if len(instanceIDs) > 0 {
			_, err := client.StopInstances(instanceIDs)
			return err == nil, err
		}
		_, err := client.StopAllInstances()
		return err == nil, err
	}
	return false, nil
}

// applyChannelAction 启用单个渠道，返回 (是否可视为成功, 期望的启用态)。
// 避免覆盖 ChannelStatusAutoDisabled：对 AutoDisabled 渠道不强行启用（保持既有语义），
// 视为成功返回 true 但 enabled 保持 false（不宣称已启用）。AutoDisabled 渠道已由
// reconcileChannelState 从决策目标中剔除，此分支仅为决策后竞态（渠道被系统标记 AutoDisabled）的兜底。
func applyChannelAction(channelID int) (bool, bool) {
	if channelID <= 0 {
		logger.LogWarn(nil, "auto scaling: invalid channel id, skip channel action")
		return false, false
	}
	channel, err := model.GetChannelById(channelID, false)
	if err != nil {
		logger.LogWarn(nil, fmt.Sprintf("auto scaling: channel %d not found (%v), skip channel action", channelID, err))
		return false, false
	}
	switch channel.Status {
	case common.ChannelStatusAutoDisabled:
		// 保持 AutoDisabled 语义：不强制覆盖禁用状态，视为收敛成功，enabled 保持 false。
		return true, false
	case common.ChannelStatusEnabled:
		// 状态已收敛：无需实际切换，仍视为成功以同步状态机。
		return true, true
	}
	return model.UpdateChannelStatus(channelID, "", common.ChannelStatusEnabled, "auto scaling: rpm over limit"), true
}

// appendChannelID 向启用列表追加渠道 id，已存在则保持不变。
func appendChannelID(ids []int, id int) []int {
	for _, v := range ids {
		if v == id {
			return ids
		}
	}
	return append(ids, id)
}

// reconcileChannelState 用渠道表真实状态校正状态机中的 ChannelsEnabled，防止状态漂移。
// 遍历 cfg.ChannelIDs：渠道不存在/查询出错时记入 unusable（warn 一次）；真实状态为启用态的
// 收集进 ChannelsEnabled；处于 ChannelStatusAutoDisabled 的渠道记入 unusable 且不加入
// ChannelsEnabled（保持禁用语义，不参与收敛检查，也不会在低于阈值时被波及）。
// cfg.ChannelIDs 为空时 ChannelsEnabled 置 nil。有变化时记录日志。
// 返回校正后的状态与不可用渠道 id 列表，调用方应把 unusable 从 cfg.ChannelIDs 中过滤后再决策。
func reconcileChannelState(state AutoscalingState, cfg operation_setting.AutoScalingConfig) (AutoscalingState, []int) {
	var enabled []int
	var unusable []int
	for _, id := range cfg.ChannelIDs {
		channel, err := model.GetChannelById(id, false)
		if err != nil {
			logger.LogWarn(nil, fmt.Sprintf("auto scaling: channel %d not found (%v), skip reconcile", id, err))
			unusable = append(unusable, id)
			continue
		}
		switch channel.Status {
		case common.ChannelStatusEnabled:
			enabled = append(enabled, id)
		case common.ChannelStatusAutoDisabled:
			// 保持 AutoDisabled 语义：不强制启用、不加入 ChannelsEnabled，也不参与收敛检查。
			unusable = append(unusable, id)
		}
	}
	if !reflect.DeepEqual(state.ChannelsEnabled, enabled) {
		logger.LogInfo(nil, fmt.Sprintf("auto scaling: reconcile channels_enabled %v -> %v", state.ChannelsEnabled, enabled))
		state.ChannelsEnabled = enabled
	}
	return state, unusable
}

// getGlobalRPM 统计最近 60 秒内的全局请求数（LogTypeConsume）。
// 第二个返回值表示查询是否成功；失败时返回的 RPM 无意义，调用方应跳过本轮。
func getGlobalRPM() (int, bool) {
	now := time.Now().Unix()
	stat, err := model.SumUsedQuota(model.LogTypeConsume, now-60, now, "", "", "", 0, "", "auto-scaling")
	if err != nil {
		logger.LogError(nil, fmt.Sprintf("auto scaling: query global rpm failed: %v", err))
		return 0, false
	}
	return stat.Rpm, true
}

// GetGlobalRPM 对外暴露当前全局 RPM，供接口展示。
func GetGlobalRPM() int {
	rpm, _ := getGlobalRPM()
	return rpm
}

func loadState() AutoscalingState {
	var st AutoscalingState
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[operation_setting.AutoScalingStateOptionKey]
	common.OptionMapRWMutex.RUnlock()
	if raw == "" {
		return st
	}
	if err := common.UnmarshalJsonStr(raw, &st); err != nil {
		logger.LogWarn(nil, "auto scaling: parse persisted state failed, reset to empty: "+err.Error())
		return AutoscalingState{}
	}
	return st
}

// GetCurrentState 对外暴露当前状态机的恢复态，供状态接口展示。
func GetCurrentState() AutoscalingState {
	return loadState()
}

func persistState(st AutoscalingState) error {
	data, err := common.Marshal(st)
	if err != nil {
		return err
	}
	return model.UpdateOption(operation_setting.AutoScalingStateOptionKey, string(data))
}
