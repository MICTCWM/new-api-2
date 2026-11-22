package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// RegisterScheduledSystemTasks wires the periodic channel test, upstream model
// update, and async task polling (Midjourney / Suno / video) jobs into the
// system task framework so a DB lease dedups execution across multiple master
// instances and each run is recorded as one task row. Call this before
// service.StartSystemTaskRunner.
func RegisterScheduledSystemTasks() {
	service.RegisterSystemTaskHandler(channelTestHandler{})
	service.RegisterSystemTaskHandler(opencodeUsageCalibrationHandler{})
	service.RegisterSystemTaskHandler(commandUsageCalibrationHandler{})
	service.RegisterSystemTaskHandler(commandGoPoolRefreshHandler{})
	service.RegisterSystemTaskHandler(speedProtectionHandler{})
	service.RegisterSystemTaskHandler(modelUpdateHandler{})
	service.RegisterSystemTaskHandler(midjourneyPollHandler{})
	service.RegisterSystemTaskHandler(asyncTaskPollHandler{})
}

type commandUsageCalibrationHandler struct{}

func (commandUsageCalibrationHandler) Type() string { return model.SystemTaskTypeCommandUsage }

func (commandUsageCalibrationHandler) Enabled() bool { return true }

// The scheduler wakes this handler once a minute; each Command channel
// persists its own ten-minute calibration slot (staggered by channel ID).
func (commandUsageCalibrationHandler) Interval() time.Duration { return time.Minute }

func (commandUsageCalibrationHandler) NewPayload() any { return nil }

func (commandUsageCalibrationHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	calibrated, err := service.CalibrateDueCommandChannels(ctx, time.Now().UTC())
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, map[string]int{"calibrated": calibrated}, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, map[string]int{"calibrated": calibrated}, nil)
}

// commandGoPoolRefreshHandler refreshes every Command Go pool account on a
// ten-minute cadence. It only updates quota snapshots and the no_quota marker;
// an account already in error/no_quota never becomes active automatically
// (recovery is a manual admin action).
type commandGoPoolRefreshHandler struct{}

func (commandGoPoolRefreshHandler) Type() string { return model.SystemTaskTypeCommandGoPool }

func (commandGoPoolRefreshHandler) Enabled() bool { return true }

func (commandGoPoolRefreshHandler) Interval() time.Duration { return 10 * time.Minute }

func (commandGoPoolRefreshHandler) NewPayload() any { return nil }

func (commandGoPoolRefreshHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	refreshed := 0
	failed := 0
	for _, channel := range channels {
		if ctx.Err() != nil {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, map[string]int{"refreshed": refreshed, "failed": failed}, ctx.Err())
			return
		}
		if channel == nil || channel.Type != constant.ChannelTypeCommandGo {
			continue
		}
		if channel.Status != common.ChannelStatusEnabled && channel.Status != common.ChannelStatusAutoDisabled {
			continue
		}
		count, countErr := service.CountCommandGoAccountsByChannel(channel.Id)
		if countErr != nil {
			common.SysError(fmt.Sprintf("failed to count Command Go accounts for channel #%d: %v", channel.Id, countErr))
			failed++
			continue
		}
		if count == 0 {
			continue
		}
		channelRefreshed, failures := service.RefreshAllCommandGoAccounts(ctx, channel.Id)
		refreshed += channelRefreshed
		failed += len(failures)
		for _, failure := range failures {
			common.SysError(fmt.Sprintf("Command Go channel #%d account #%d (%s) scheduled refresh failed: %s", channel.Id, failure.Id, failure.Name, failure.Error))
		}
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, map[string]int{"refreshed": refreshed, "failed": failed}, nil)
}

type opencodeUsageCalibrationHandler struct{}

func (opencodeUsageCalibrationHandler) Type() string { return model.SystemTaskTypeOpencodeUsage }

func (opencodeUsageCalibrationHandler) Enabled() bool { return true }

// The scheduler wakes this handler once a minute. Each channel persists its
// own ten-minute slot, which keeps upstream usage reads staggered.
func (opencodeUsageCalibrationHandler) Interval() time.Duration { return time.Minute }

func (opencodeUsageCalibrationHandler) NewPayload() any { return nil }

func (opencodeUsageCalibrationHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	disabled, enabled, reconcileErr := model.ReconcileCodeProviderQuotaChannels(time.Now().Unix())
	if reconcileErr != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, map[string]int{"disabled": disabled, "enabled": enabled}, reconcileErr)
		return
	}
	calibrated, err := model.CalibrateDueOpencodeChannels(ctx, time.Now().Unix(), opencodeCalibrationClientFactory)
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, map[string]int{"calibrated": calibrated, "disabled": disabled, "enabled": enabled}, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, map[string]int{"calibrated": calibrated, "disabled": disabled, "enabled": enabled}, nil)
}

type speedProtectionHandler struct{}

func (speedProtectionHandler) Type() string { return model.SystemTaskTypeSpeedProtection }

func (speedProtectionHandler) Enabled() bool {
	return operation_setting.GetMonitorSetting().SpeedProtectionProbeIntervalMinutes > 0
}

func (speedProtectionHandler) Interval() time.Duration {
	minutes := operation_setting.GetMonitorSetting().SpeedProtectionProbeIntervalMinutes
	if minutes <= 0 {
		minutes = operation_setting.DefaultSpeedProtectionProbeIntervalMinutes
	}
	return time.Duration(minutes * float64(time.Minute))
}

func (speedProtectionHandler) NewPayload() any { return nil }

func (speedProtectionHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	probed := 0
	for _, channel := range channels {
		if ctx.Err() != nil {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, ctx.Err())
			return
		}
		if channel == nil || channel.Status != common.ChannelStatusEnabled || !relaycommon.IsStreamSupportedChannel(channel.Type) {
			continue
		}
		status, statusErr := model.GetSpeedProtectionStatus(channel)
		if statusErr != nil {
			common.SysError(fmt.Sprintf("failed to load speed protection config: channel_id=%d err=%v", channel.Id, statusErr))
			continue
		}
		if !status.Enabled {
			continue
		}
		for _, group := range status.Groups {
			if ctx.Err() != nil {
				finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, ctx.Err())
				return
			}
			result := testChannelWithGroup(ctx, channel, testUserID, "", "", true, group)
			now := time.Now().Unix()
			if updateErr := model.UpdateSpeedProtectionProbe(channel.Id, group, result.firstResponseMillis, result.localErr == nil && result.firstResponseSeen, now); updateErr != nil {
				common.SysError(fmt.Sprintf("failed to update speed protection state: channel_id=%d group=%s err=%v", channel.Id, group, updateErr))
			}
			probed++
		}
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, map[string]int{"probed": probed}, nil)
}

// channelTestHandler runs the scheduled "test all channels" job. Enablement and
// cadence still come from the monitor settings; only the execution path moved
// into the system task runner.
type channelTestHandler struct{}

func (channelTestHandler) Type() string { return model.SystemTaskTypeChannelTest }

func (channelTestHandler) Enabled() bool {
	return operation_setting.GetMonitorSetting().AutoTestChannelEnabled
}

func (channelTestHandler) Interval() time.Duration {
	minutes := operation_setting.GetMonitorSetting().AutoTestChannelMinutes
	if minutes <= 0 {
		minutes = 10
	}
	return time.Duration(minutes * float64(time.Minute))
}

func (channelTestHandler) NewPayload() any { return nil }

// channelTestTaskPayload controls one channel_test run. A nil/empty payload is a
// scheduled run, which uses the configured monitor ChannelTestMode and does not
// notify. A manual "test all channels" trigger sets Mode=scheduled_all and
// Notify=true to reproduce the legacy manual behavior (test every channel and
// notify root on completion).
type channelTestTaskPayload struct {
	Mode   string `json:"mode,omitempty"`
	Notify bool   `json:"notify,omitempty"`
}

func (channelTestHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := channelTestTaskPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary, err := runChannelTestTask(ctx, payload.Mode, payload.Notify, service.NewSystemTaskProgressReporter(task, runnerID))
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// modelUpdateHandler runs the scheduled upstream model update detection job.
type modelUpdateHandler struct{}

func (modelUpdateHandler) Type() string { return model.SystemTaskTypeModelUpdate }

func (modelUpdateHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED", true)
}

func (modelUpdateHandler) Interval() time.Duration {
	intervalMinutes := common.GetEnvOrDefault(
		"CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES",
		channelUpstreamModelUpdateTaskDefaultIntervalMinutes,
	)
	if intervalMinutes < 1 {
		intervalMinutes = channelUpstreamModelUpdateTaskDefaultIntervalMinutes
	}
	return time.Duration(intervalMinutes) * time.Minute
}

func (modelUpdateHandler) NewPayload() any { return nil }

// modelUpdateTaskPayload controls one model_update run. A scheduled run
// (Manual=false) respects the per-channel minimum check interval and may
// auto-apply detected models when a channel has auto-sync enabled. A manual
// "detect all" trigger sets Manual=true to reproduce the legacy detect-all
// semantics: force a re-check regardless of the interval and never auto-apply,
// so the admin reviews and applies changes explicitly.
type modelUpdateTaskPayload struct {
	Manual bool `json:"manual,omitempty"`
}

func (modelUpdateHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := modelUpdateTaskPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary := runChannelUpstreamModelUpdateTaskOnce(ctx, payload.Manual, !payload.Manual, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// midjourneyPollHandler runs one Midjourney polling pass per scheduled run.
// Enabled() folds the "are there unfinished tasks?" check into enablement so the
// scheduler creates no row when the system is idle; only when at least one
// Midjourney task is in progress does a row get scheduled.
type midjourneyPollHandler struct{}

func (midjourneyPollHandler) Type() string { return model.SystemTaskTypeMidjourneyPoll }

func (midjourneyPollHandler) Enabled() bool {
	return constant.UpdateTask && model.HasUnfinishedMidjourneyTasks()
}

func (midjourneyPollHandler) Interval() time.Duration { return 15 * time.Second }

func (midjourneyPollHandler) NewPayload() any { return nil }

func (midjourneyPollHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary := runMidjourneyTaskUpdateOnce(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// asyncTaskPollHandler runs one async-task (Suno/video) polling pass per
// scheduled run. Like midjourneyPollHandler, Enabled() folds in the unfinished
// task existence check so an idle system schedules no rows.
type asyncTaskPollHandler struct{}

func (asyncTaskPollHandler) Type() string { return model.SystemTaskTypeAsyncTaskPoll }

func (asyncTaskPollHandler) Enabled() bool {
	return constant.UpdateTask && model.HasUnfinishedSyncTasks()
}

func (asyncTaskPollHandler) Interval() time.Duration { return 15 * time.Second }

func (asyncTaskPollHandler) NewPayload() any { return nil }

func (asyncTaskPollHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary := service.RunTaskPollingOnce(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

func finishSystemTaskHandler(task *model.SystemTask, runnerID string, status model.SystemTaskStatus, result any, runErr error) {
	errorMessage := ""
	if runErr != nil {
		errorMessage = runErr.Error()
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, status, result, errorMessage); err != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, err))
	}
}
