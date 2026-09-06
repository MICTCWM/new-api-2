package autoscaling

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeAutoDLResponse(t *testing.T, w http.ResponseWriter, code string, msg string, data any) {
	t.Helper()
	body, err := common.Marshal(map[string]interface{}{"code": code, "msg": msg, "data": data})
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// readJSONBody 解析请求体为 map，便于断言 power 请求的参数。
func readJSONBody(t *testing.T, r *http.Request) map[string]interface{} {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	var body map[string]interface{}
	require.NoError(t, common.Unmarshal(data, &body))
	return body
}

// multiInstanceServer 构造返回多实例列表的 AutoDL 假服务器，power_on/power_off 由传入的 handler 处理。
func multiInstanceServer(t *testing.T, powerOn func(uuid string) bool, powerOff func(uuid string) bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/dev/instance/pro/list":
			writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{
				"list": []map[string]interface{}{
					{"uuid": "pro-1", "name": "worker-a", "status": "stopped"},
					{"uuid": "pro-2", "name": "worker-b", "status": "stopped"},
					{"uuid": "pro-3", "name": "worker-c", "status": "stopped"},
				},
			})
		case "/api/v1/dev/instance/pro/power_on":
			body := readJSONBody(t, r)
			uuid := body["instance_uuid"].(string)
			assert.Equal(t, "gpu", body["payload"])
			if powerOn(uuid) {
				writeAutoDLResponse(t, w, "Success", "", nil)
			} else {
				writeAutoDLResponse(t, w, "Error", "power on failed", nil)
			}
		case "/api/v1/dev/instance/pro/power_off":
			uuid := readJSONBody(t, r)["instance_uuid"].(string)
			if powerOff(uuid) {
				writeAutoDLResponse(t, w, "Success", "", nil)
			} else {
				writeAutoDLResponse(t, w, "Error", "power off failed", nil)
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestListInstances(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{
			"list": []map[string]interface{}{
				{"uuid": "pro-7879cfe84650", "name": "worker-a", "status": "running"},
				{"uuid": "pro-2", "name": "worker-b", "status": "stopped"},
			},
		})
	}))
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	list, err := client.ListInstances()
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "pro-7879cfe84650", list[0].InstanceUUID)
	assert.Equal(t, "worker-b", list[1].Name)
	assert.Equal(t, "running", list[0].Status)
}

// TestListInstancesPagination 覆盖实例超过一页时自动翻页拉取全部，
// 且最后一页不足 page_size 时停止翻页。
func TestListInstancesPagination(t *testing.T) {
	var pages []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(t, r)
		page := int(body["page_index"].(float64))
		pages = append(pages, page)
		if page == 1 {
			list := make([]map[string]interface{}, 0, autoDLPageSize)
			for i := 0; i < autoDLPageSize; i++ {
				list = append(list, map[string]interface{}{"uuid": fmt.Sprintf("pro-%d", i), "name": "w", "status": "stopped"})
			}
			writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{"list": list})
			return
		}
		writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{
			"list": []map[string]interface{}{
				{"uuid": "pro-last-1", "name": "w", "status": "stopped"},
				{"uuid": "pro-last-2", "name": "w", "status": "stopped"},
			},
		})
	}))
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	list, err := client.ListInstances()
	require.NoError(t, err)
	require.Len(t, list, autoDLPageSize+2)
	assert.Equal(t, "pro-0", list[0].InstanceUUID)
	assert.Equal(t, "pro-99", list[99].InstanceUUID)
	assert.Equal(t, "pro-last-1", list[100].InstanceUUID)
	assert.Equal(t, "pro-last-2", list[101].InstanceUUID)
	assert.Equal(t, []int{1, 2}, pages)
}

func TestStartAllInstances(t *testing.T) {
	var poweredOn []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/dev/instance/pro/list":
			writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{
				"list": []map[string]interface{}{
					{"uuid": "pro-1", "name": "worker-a", "status": "stopped"},
					{"uuid": "pro-2", "name": "worker-b", "status": "stopped"},
				},
			})
		case "/api/v1/dev/instance/pro/power_on":
			body := readJSONBody(t, r)
			poweredOn = append(poweredOn, body["instance_uuid"].(string))
			assert.Equal(t, "gpu", body["payload"])
			writeAutoDLResponse(t, w, "Success", "", nil)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	uuids, err := client.StartAllInstances()
	require.NoError(t, err)
	assert.Equal(t, []string{"pro-1", "pro-2"}, uuids)
	assert.Equal(t, []string{"pro-1", "pro-2"}, poweredOn)
}

func TestStopAllInstances(t *testing.T) {
	var poweredOff []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/dev/instance/pro/list":
			writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{
				"list": []map[string]interface{}{
					{"uuid": "pro-1", "name": "worker-a", "status": "running"},
					{"uuid": "pro-2", "name": "worker-b", "status": "running"},
				},
			})
		case "/api/v1/dev/instance/pro/power_off":
			body := readJSONBody(t, r)
			poweredOff = append(poweredOff, body["instance_uuid"].(string))
			assert.NotContains(t, body, "payload")
			writeAutoDLResponse(t, w, "Success", "", nil)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	uuids, err := client.StopAllInstances()
	require.NoError(t, err)
	assert.Equal(t, []string{"pro-1", "pro-2"}, uuids)
	assert.Equal(t, []string{"pro-1", "pro-2"}, poweredOff)
}

func TestStartAllInstancesPartialFailure(t *testing.T) {
	server := multiInstanceServer(t, func(uuid string) bool {
		return uuid != "pro-2"
	}, func(uuid string) bool { return true })
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	uuids, err := client.StartAllInstances()
	require.Error(t, err)
	// 部分失败：返回成功部分 + 聚合错误，不中断其余实例
	assert.Equal(t, []string{"pro-1", "pro-3"}, uuids)
	assert.Contains(t, err.Error(), "1/3 instances failed")
	assert.Contains(t, err.Error(), "pro-2")
}

func TestStopAllInstancesPartialFailure(t *testing.T) {
	server := multiInstanceServer(t, func(uuid string) bool { return true }, func(uuid string) bool {
		return uuid != "pro-1"
	})
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	uuids, err := client.StopAllInstances()
	require.Error(t, err)
	assert.Equal(t, []string{"pro-2", "pro-3"}, uuids)
	assert.Contains(t, err.Error(), "1/3 instances failed")
	assert.Contains(t, err.Error(), "pro-1")
}

func TestStartAllInstancesEmptyReturnsNoInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{"list": []map[string]interface{}{}})
	}))
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	_, err := client.StartAllInstances()
	assert.ErrorIs(t, err, ErrNoInstance)
}

func TestStopAllInstancesNoUUIDReturnsNoInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{
			"list": []map[string]interface{}{{"uuid": "", "name": "x", "status": "stopped"}},
		})
	}))
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	_, err := client.StopAllInstances()
	assert.ErrorIs(t, err, ErrNoInstance)
}

func TestStartAllInstancesAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/dev/instance/pro/list" && r.Header.Get("Authorization") != "test-token" {
			writeAutoDLResponse(t, w, "AuthError", "invalid token", nil)
			return
		}
		writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{
			"list": []map[string]interface{}{{"uuid": "pro-1", "name": "a", "status": "stopped"}},
		})
	}))
	defer server.Close()
	client := &AutoDLClient{token: "bad-token", baseURL: server.URL}

	_, err := client.StartAllInstances()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code=AuthError")
	assert.Contains(t, err.Error(), "invalid token")
}

// TestPowerOnAPIError 覆盖 power_on 接口返回业务失败（code!=Success）时能正确报错。
func TestPowerOnAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/dev/instance/pro/list":
			writeAutoDLResponse(t, w, "Success", "", map[string]interface{}{
				"list": []map[string]interface{}{
					{"uuid": "pro-1", "name": "worker-a", "status": "stopped"},
				},
			})
		case "/api/v1/dev/instance/pro/power_on":
			writeAutoDLResponse(t, w, "Error", "power on failed", nil)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	_, err := client.StartAllInstances()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code=Error")
	assert.Contains(t, err.Error(), "power on failed")
}

func TestInvalidJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `not-json`)
	}))
	defer server.Close()
	client := &AutoDLClient{token: "test-token", baseURL: server.URL}

	_, err := client.TestConnection()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse response")
}
