package controller

import (
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// How often an idle stream sends a comment line to keep proxies and browsers
// from treating the connection as dead.
const connectionLogStreamHeartbeat = 15 * time.Second

// GetConnectionLogs serves the live snapshot behind the admin 连接日志 panel.
func GetConnectionLogs(c *gin.Context) {
	if c.Query("progress") == "true" {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    service.GetConnectionLogProgressSnapshot(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    service.GetConnectionLogSnapshot(),
	})
}

// StreamConnectionLogs pushes the live snapshot to the admin 连接日志 panel as
// Server-Sent Events, so a new connection appears without polling.
func StreamConnectionLogs(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// Without this a buffering reverse proxy (nginx) holds frames until the
	// stream closes, which would defeat the whole point.
	c.Header("X-Accel-Buffering", "no")

	events, unsubscribe := service.SubscribeConnectionLogs()
	defer unsubscribe()

	payload, err := common.Marshal(service.GetConnectionLogSnapshot())
	if err != nil {
		common.SysError("failed to serialize connection log snapshot: " + err.Error())
	} else {
		writeConnectionLogFrame(c, payload)
	}

	heartbeat := time.NewTicker(connectionLogStreamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case payload, ok := <-events:
			if !ok {
				return
			}
			writeConnectionLogFrame(c, payload)
		case <-heartbeat.C:
			_, _ = c.Writer.WriteString(": ping\n\n")
			c.Writer.Flush()
		}
	}
}

func writeConnectionLogFrame(c *gin.Context, payload []byte) {
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", payload)
	c.Writer.Flush()
}
