package service

import "github.com/QuantumNous/new-api/model"

// ObserveSpeedProtectionRequest records slow failures for active protection
// and counts fast streaming requests for a kicked channel/group recovery
// window. Newly configured channels still require an active probe before they
// can enter protection.
func ObserveSpeedProtectionRequest(channelID int, group string, firstResponseMs int64) error {
	return model.ObserveSpeedProtectionRequest(channelID, group, firstResponseMs)
}
