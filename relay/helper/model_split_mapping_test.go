// Copyright (C) 2023-2026 QuantumNous
// SPDX-License-Identifier: AGPL-3.0-or-later

package helper

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestModelMappedHelperWalksSplitTargetOverChannelMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Set("model_mapping", service.ApplyModelSplitMappingOverride(`{"nn":"v4.1"}`, "nn", "split-target"))

	info := &relaycommon.RelayInfo{
		OriginModelName: "nn",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "nn",
		},
	}
	require.NoError(t, ModelMappedHelper(c, info, nil))
	require.True(t, info.IsModelMapped)
	require.Equal(t, "nn", info.OriginModelName)
	require.Equal(t, "split-target", info.UpstreamModelName)
}

// TestModelMappedHelperSplitBackToOriginKeepsChannelMapping covers the split
// rule whose target equals the original model: the override is a no-op, so the
// channel mapping chain still applies and the request is not treated as an
// unmapped model (which whitelist channels reject).
func TestModelMappedHelperSplitBackToOriginKeepsChannelMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Set("model_mapping", service.ApplyModelSplitMappingOverride(`{"alias":"native"}`, "alias", "alias"))

	info := &relaycommon.RelayInfo{
		OriginModelName: "alias",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "alias",
		},
	}
	require.NoError(t, ModelMappedHelper(c, info, nil))
	require.True(t, info.IsModelMapped)
	require.Equal(t, "alias", info.OriginModelName)
	require.Equal(t, "native", info.UpstreamModelName)
}
