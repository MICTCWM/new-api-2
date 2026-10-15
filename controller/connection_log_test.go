package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type connectionLogItem struct {
	RequestID    string `json:"request_id"`
	Username     string `json:"username"`
	State        string `json:"state"`
	Stream       bool   `json:"stream"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	TTFTMS       *int64 `json:"ttft_ms"`
}

type connectionLogsPayload struct {
	Success bool `json:"success"`
	Data    struct {
		Concurrent       int64               `json:"concurrent"`
		TotalInputTokens int64               `json:"total_input_tokens"`
		ServerTime       int64               `json:"server_time"`
		NodeName         string              `json:"node_name"`
		Items            []connectionLogItem `json:"items"`
	} `json:"data"`
}

func requestConnectionLogs(t *testing.T) connectionLogsPayload {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/connection/logs", GetConnectionLogs)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/connection/logs", nil))
	require.Equal(t, http.StatusOK, recorder.Code)

	var payload connectionLogsPayload
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	return payload
}

func findConnectionItem(payload connectionLogsPayload, requestID string) *connectionLogItem {
	for index := range payload.Data.Items {
		if payload.Data.Items[index].RequestID == requestID {
			return &payload.Data.Items[index]
		}
	}
	return nil
}

func newConnectionTestRelayInfo(requestID string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RequestId:       requestID,
		UserId:          5,
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		IsStream:        true,
		StartTime:       time.Now().Add(-1500 * time.Millisecond),
	}
}

func TestGetConnectionLogsHidesOutputTokensWhileOpen(t *testing.T) {
	requestID := "conn-controller-open"
	service.RegisterConnection(newConnectionTestRelayInfo(requestID), "alice", 900)
	// Usage may already be settled upstream; the panel still must not show it.
	service.SettleConnectionTokens(requestID, 900, 120)
	t.Cleanup(func() { service.FinishConnection(requestID) })

	payload := requestConnectionLogs(t)
	require.Positive(t, payload.Data.ServerTime)

	item := findConnectionItem(payload, requestID)
	require.NotNil(t, item, "registered connection must appear in the snapshot")
	require.Equal(t, service.ConnectionStateWaitingFirstToken, item.State)
	require.Equal(t, "alice", item.Username)
	require.True(t, item.Stream)
	require.EqualValues(t, 900, item.InputTokens)
	require.Nil(t, item.OutputTokens, "output tokens stay hidden until the request finishes")
	require.Nil(t, item.TTFTMS, "TTFT stays empty before the first token")
}

func TestGetConnectionLogsRevealsUsageAfterRequestFinishes(t *testing.T) {
	requestID := "conn-controller-settled"
	service.RegisterConnection(newConnectionTestRelayInfo(requestID), "bob", 1200)
	service.MarkConnectionFirstToken(requestID)
	service.SettleConnectionTokens(requestID, 1500, 480)
	service.FinishConnection(requestID)

	item := findConnectionItem(requestConnectionLogs(t), requestID)
	require.NotNil(t, item)
	require.Equal(t, service.ConnectionStateCompleted, item.State)
	require.EqualValues(t, 1500, item.InputTokens)
	require.NotNil(t, item.OutputTokens)
	require.EqualValues(t, 480, *item.OutputTokens)
	require.NotNil(t, item.TTFTMS)
	require.Less(t, int64(0), *item.TTFTMS)
}
