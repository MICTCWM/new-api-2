package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

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
