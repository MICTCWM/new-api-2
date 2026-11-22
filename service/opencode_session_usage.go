package service

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

type OpencodeSessionUsage struct {
	ChannelID      int    `json:"channel_id"`
	ChannelName    string `json:"channel_name"`
	ChannelType    int    `json:"channel_type"`
	ActiveSessions int    `json:"active_sessions"`
	Capacity       int    `json:"capacity"`
}

func GetOpencodeSessionUsage() ([]OpencodeSessionUsage, error) {
	var channels []*model.Channel
	if err := model.DB.Where("type IN ? AND status = ?", []int{constant.ChannelTypeOpencode, constant.ChannelTypeCommand, constant.ChannelTypeCommandGo}, common.ChannelStatusEnabled).Order("id ASC").Find(&channels).Error; err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	capacity := operation_setting.GetOpencodeConcurrencyLimit()
	usage := make([]OpencodeSessionUsage, 0, len(channels))
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		active, err := countOpencodeActiveSessions(channel.Id, now)
		if err != nil {
			return nil, err
		}
		usage = append(usage, OpencodeSessionUsage{
			ChannelID:      channel.Id,
			ChannelName:    channel.Name,
			ChannelType:    channel.Type,
			ActiveSessions: active,
			Capacity:       capacity,
		})
	}
	return usage, nil
}
