package helper

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIToolValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newJSONContext := func(t *testing.T, body string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/relay", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}
	bodyWithTool := func(name, choice string) string {
		return `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"` + name + `","parameters":{"type":"object"}}}],"tool_choice":` + choice + `}`
	}

	t.Run("128 character tool name accepted", func(t *testing.T) {
		c := newJSONContext(t, bodyWithTool(strings.Repeat("a", 128), `"auto"`))
		_, err := GetAndValidateTextRequest(c, relayconstant.RelayModeChatCompletions)
		require.NoError(t, err)
	})
	t.Run("129 character tool name rejected", func(t *testing.T) {
		c := newJSONContext(t, bodyWithTool(strings.Repeat("a", 129), `"auto"`))
		_, err := GetAndValidateTextRequest(c, relayconstant.RelayModeChatCompletions)
		require.Error(t, err)
		require.Contains(t, err.Error(), "exceeds 128 characters")
	})
	t.Run("tool name starting with digit rejected", func(t *testing.T) {
		c := newJSONContext(t, bodyWithTool("1tool", `"auto"`))
		_, err := GetAndValidateTextRequest(c, relayconstant.RelayModeChatCompletions)
		require.Error(t, err)
		require.Contains(t, err.Error(), "must start with a letter or underscore")
	})
	t.Run("declared tool choice accepted", func(t *testing.T) {
		c := newJSONContext(t, bodyWithTool("get_weather", `{"type":"function","function":{"name":"get_weather"}}`))
		_, err := GetAndValidateTextRequest(c, relayconstant.RelayModeChatCompletions)
		require.NoError(t, err)
	})
	t.Run("undeclared tool choice rejected", func(t *testing.T) {
		c := newJSONContext(t, bodyWithTool("get_weather", `{"type":"function","function":{"name":"missing"}}`))
		_, err := GetAndValidateTextRequest(c, relayconstant.RelayModeChatCompletions)
		require.Error(t, err)
		require.Contains(t, err.Error(), "undeclared tool")
	})
}
