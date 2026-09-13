package controller

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/autoscaling"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// TestAutoScalingConnection 从 POST JSON body 读取 token（表单中当前输入、可能尚未保存的值）
// 测试 AutoDL 容器实例 Pro API 连通性；未提供时回退到已保存的 AutoDL Token。管理员权限。
func TestAutoScalingConnection(c *gin.Context) {
	cfg := operation_setting.GetAutoScalingConfig()
	var req struct {
		Token string `json:"token"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "请求体解析失败")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		token = strings.TrimSpace(cfg.Token)
	}
	if token == "" {
		common.ApiErrorMsg(c, "未配置 AutoDL Token")
		return
	}
	client := autoscaling.NewAutoDLClient(token)
	list, err := client.TestConnection()
	if err != nil {
		logger.LogError(nil, "auto scaling test connection failed: "+err.Error())
		common.ApiErrorMsg(c, "AutoDL 连接失败")
		return
	}
	type brief struct {
		InstanceUUID string `json:"instance_uuid"`
		Name         string `json:"name"`
		Status       string `json:"status"`
	}
	items := make([]brief, 0, len(list))
	for _, d := range list {
		items = append(items, brief{InstanceUUID: d.InstanceUUID, Name: d.Name, Status: d.Status})
	}
	common.ApiSuccess(c, gin.H{
		"count":       len(list),
		"deployments": items,
	})
}

// TestStartAutoScaling 从 POST JSON body 读取 token（trim，空则回退已保存 cfg.Token），
// 真实向 AutoDL 发送开机命令以验证连通性。cfg.InstanceIDs 非空时只对选中实例开机，
// 否则对全部实例开机；成功返回成功操作的实例数。管理员权限。
// 部分实例失败时仍返回 error，但消息中带上成功操作的实例数，便于管理员评估影响。
func TestStartAutoScaling(c *gin.Context) {
	cfg := operation_setting.GetAutoScalingConfig()
	var req struct {
		Token string `json:"token"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "请求体解析失败")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		token = strings.TrimSpace(cfg.Token)
	}
	if token == "" {
		common.ApiErrorMsg(c, "未配置 AutoDL Token")
		return
	}
	client := autoscaling.NewAutoDLClient(token)
	var succeeded []string
	var err error
	if len(cfg.InstanceIDs) > 0 {
		succeeded, err = client.StartInstances(cfg.InstanceIDs)
	} else {
		succeeded, err = client.StartAllInstances()
	}
	if err != nil {
		logger.LogError(nil, "auto scaling test-start failed: "+err.Error())
		common.ApiErrorMsg(c, testActionFailMsg("AutoDL 开机失败", succeeded, err))
		return
	}
	common.ApiSuccess(c, gin.H{"count": len(succeeded)})
}

// TestStopAutoScaling 从 POST JSON body 读取 token（trim，空则回退已保存 cfg.Token），
// 真实向 AutoDL 发送关机命令以验证连通性。cfg.InstanceIDs 非空时只对选中实例关机，
// 否则对全部实例关机；成功返回成功操作的实例数。管理员权限。
// 部分实例失败时仍返回 error，但消息中带上成功操作的实例数，便于管理员评估影响。
func TestStopAutoScaling(c *gin.Context) {
	cfg := operation_setting.GetAutoScalingConfig()
	var req struct {
		Token string `json:"token"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "请求体解析失败")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		token = strings.TrimSpace(cfg.Token)
	}
	if token == "" {
		common.ApiErrorMsg(c, "未配置 AutoDL Token")
		return
	}
	client := autoscaling.NewAutoDLClient(token)
	var succeeded []string
	var err error
	if len(cfg.InstanceIDs) > 0 {
		succeeded, err = client.StopInstances(cfg.InstanceIDs)
	} else {
		succeeded, err = client.StopAllInstances()
	}
	if err != nil {
		logger.LogError(nil, "auto scaling test-stop failed: "+err.Error())
		common.ApiErrorMsg(c, testActionFailMsg("AutoDL 关机失败", succeeded, err))
		return
	}
	common.ApiSuccess(c, gin.H{"count": len(succeeded)})
}

// GetAutoScalingInstances 返回当前 AutoDL 账号下可选的容器实例列表，供前端配置勾选。
// 使用已保存的 AutoDL Token；未配置时返回错误。管理员权限。
func GetAutoScalingInstances(c *gin.Context) {
	cfg := operation_setting.GetAutoScalingConfig()
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		common.ApiErrorMsg(c, "未配置 AutoDL Token")
		return
	}
	client := autoscaling.NewAutoDLClient(token)
	list, err := client.ListInstances()
	if err != nil {
		logger.LogError(nil, "auto scaling list instances failed: "+err.Error())
		common.ApiErrorMsg(c, "AutoDL 实例列表获取失败")
		return
	}
	type brief struct {
		InstanceUUID string `json:"instance_uuid"`
		Name         string `json:"name"`
		Status       string `json:"status"`
	}
	items := make([]brief, 0, len(list))
	for _, d := range list {
		items = append(items, brief{InstanceUUID: d.InstanceUUID, Name: d.Name, Status: d.Status})
	}
	common.ApiSuccess(c, gin.H{
		"count":     len(list),
		"instances": items,
	})
}

// GetAutoScalingStatus 返回当前自动伸缩运行状态，供前端展示。管理员权限。
func GetAutoScalingStatus(c *gin.Context) {
	cfg := operation_setting.GetAutoScalingConfig()
	state := autoscaling.GetCurrentState()
	now := time.Now().Unix()

	cooldownLeft := int64(0)
	if state.LastActionAt > 0 {
		end := state.LastActionAt + int64(cfg.CooldownMinutes*60)
		if end > now {
			cooldownLeft = end - now
		}
	}

	common.ApiSuccess(c, gin.H{
		"enabled":               cfg.Enabled,
		"rpm_limit":             cfg.RPMLimit,
		"cooldown_minutes":      cfg.CooldownMinutes,
		"current_rpm":           autoscaling.GetGlobalRPM(),
		"server_started":        state.ServerStarted,
		"channels_enabled":      state.ChannelsEnabled,
		"channel_ids":           cfg.ChannelIDs,
		"last_action_at":        state.LastActionAt,
		"cooldown_left_seconds": cooldownLeft,
	})
}

// testActionFailMsg 生成 test-start/test-stop 部分失败时的错误消息。
// 聚合错误形如 "N/M instances failed"（见 setting/autoscaling 的 operateInstances），
// 从中解析出总数 M 后拼接为「scene：成功 N/M」；解析失败或拿不到总数时，
// 至少带上已成功操作的实例数（len(succeeded)），便于管理员评估影响。
func testActionFailMsg(scene string, succeeded []string, err error) string {
	msg := fmt.Sprintf("%s：成功 %d", scene, len(succeeded))
	if err != nil {
		var failed, total int
		if n, _ := fmt.Sscanf(err.Error(), "%d/%d instances failed", &failed, &total); n == 2 && total > 0 {
			msg = fmt.Sprintf("%s：成功 %d/%d", scene, len(succeeded), total)
		}
	}
	return msg
}
