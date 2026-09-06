package autoscaling

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	autoDLBaseURL = "https://api.autodl.com"
	autoDLTimeout = 30 * time.Second

	autoDLListPath      = "/api/v1/dev/instance/pro/list"
	autoDLPowerOnPath   = "/api/v1/dev/instance/pro/power_on"
	autoDLPowerOffPath  = "/api/v1/dev/instance/pro/power_off"
	autoDLPageSize      = 100
	// autoDLMaxPages 是实例列表翻页的最大页数上界，防止异常响应导致死循环。
	autoDLMaxPages      = 1000
	autoDLSuccessCode   = "Success"
)

// ErrNoInstance 表示 AutoDL 实例列表中不存在可用的容器实例 Pro（列表为空或全部缺少 uuid）。
var ErrNoInstance = errors.New("autodl: no available instance")

// AutoDLClient 封装 AutoDL 容器实例 Pro REST API（https://api.autodl.com）。
// 鉴权采用请求头 Authorization 直接携带 JWT，无 Bearer 前缀。
type AutoDLClient struct {
	token   string
	baseURL string
}

// NewAutoDLClient 创建一个 AutoDL 客户端。
func NewAutoDLClient(token string) *AutoDLClient {
	return &AutoDLClient{token: token, baseURL: autoDLBaseURL}
}

// InstancePro 表示 AutoDL 容器实例 Pro 列表中的一项，仅保留本功能需要的字段。
type InstancePro struct {
	InstanceUUID string `json:"uuid"`
	Name         string `json:"name"`
	Status       string `json:"status"`
}

// autoDLResponse 是 AutoDL API 的统一响应外壳。code=="Success" 表示业务成功。
type autoDLResponse struct {
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// doRequest 发送一次 HTTP 请求并解析统一响应外壳。业务失败（code!=Success）与
// 非 2xx 状态码都会在错误信息中带上 HTTP 状态码与 msg。
func (c *AutoDLClient) doRequest(method, path string, body any) (*autoDLResponse, error) {
	client := &http.Client{Timeout: autoDLTimeout}

	var reader io.Reader
	if body != nil {
		data, err := common.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create request %s %s: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var parsed autoDLResponse
	if err := common.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse response (http %d): %w", resp.StatusCode, err)
	}
	if parsed.Code != autoDLSuccessCode {
		return &parsed, fmt.Errorf("autodl api error (http %d): code=%s msg=%s", resp.StatusCode, parsed.Code, parsed.Msg)
	}
	return &parsed, nil
}

// ListInstances 列出当前账号下的全部容器实例 Pro（自动翻页拉取全部）。
// 每页请求 autoDLPageSize 条；当某页返回不足一页或为空时视为已拉完，停止翻页；
// page 有 autoDLMaxPages 上界兜底，防止异常响应导致死循环。
func (c *AutoDLClient) ListInstances() ([]InstancePro, error) {
	var all []InstancePro
	for page := 1; page <= autoDLMaxPages; page++ {
		resp, err := c.doRequest(http.MethodPost, autoDLListPath, map[string]interface{}{
			"page_index": page,
			"page_size":  autoDLPageSize,
		})
		if err != nil {
			return nil, err
		}
		if len(resp.Data) == 0 {
			break
		}
		var payload struct {
			List []InstancePro `json:"list"`
		}
		if err := common.Unmarshal(resp.Data, &payload); err != nil {
			return nil, fmt.Errorf("parse instance list: %w", err)
		}
		all = append(all, payload.List...)
		// 不足一页说明已到最后一页，结束翻页。
		if len(payload.List) < autoDLPageSize {
			break
		}
	}
	return all, nil
}

// TestConnection 仅调用 list 接口校验 token 是否有效。返回实例列表（可能为空）。
func (c *AutoDLClient) TestConnection() ([]InstancePro, error) {
	return c.ListInstances()
}

// operateInstances 对指定的 uuids 执行统一的 power 操作。
// 枚举给定 uuids（跳过空串与仅空白段）；空列表时返回 ErrNoInstance。
// 逐个操作，单实例失败不中断其余实例。返回成功操作的实例 uuid 列表；
// 存在失败时同时返回聚合错误（"N/M instances failed: ..."），全部成功返回完整列表与 nil。
func (c *AutoDLClient) operateInstances(path string, uuids []string, bodyFor func(uuid string) map[string]interface{}) ([]string, error) {
	var valid []string
	for _, uuid := range uuids {
		if strings.TrimSpace(uuid) != "" {
			valid = append(valid, uuid)
		}
	}
	if len(valid) == 0 {
		return nil, ErrNoInstance
	}
	succeeded := make([]string, 0, len(valid))
	var failures []string
	for _, uuid := range valid {
		if _, err := c.doRequest(http.MethodPost, path, bodyFor(uuid)); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", uuid, err))
			continue
		}
		succeeded = append(succeeded, uuid)
	}
	if len(failures) > 0 {
		return succeeded, fmt.Errorf("%d/%d instances failed: %s", len(failures), len(valid), strings.Join(failures, "; "))
	}
	return succeeded, nil
}

// StartInstances 启动指定 uuids 的容器实例 Pro。
// 逐个调用 power_on（body {instance_uuid, payload:"gpu"}），返回成功启动的实例 uuid 列表。
// uuids 为空或全部为空白段时返回 ErrNoInstance。
func (c *AutoDLClient) StartInstances(uuids []string) ([]string, error) {
	return c.operateInstances(autoDLPowerOnPath, uuids, func(uuid string) map[string]interface{} {
		return map[string]interface{}{
			"instance_uuid": uuid,
			"payload":       "gpu",
		}
	})
}

// StopInstances 停止指定 uuids 的容器实例 Pro。
// 逐个调用 power_off（body {instance_uuid}），返回成功停止的实例 uuid 列表。
// uuids 为空或全部为空白段时返回 ErrNoInstance。
func (c *AutoDLClient) StopInstances(uuids []string) ([]string, error) {
	return c.operateInstances(autoDLPowerOffPath, uuids, func(uuid string) map[string]interface{} {
		return map[string]interface{}{
			"instance_uuid": uuid,
		}
	})
}

// StartAllInstances 启动当前账号下的全部容器实例 Pro。
// 先拉取实例列表，再对其全部 uuid 执行 StartInstances。
// 实例列表为空或全部缺少 uuid 时返回 ErrNoInstance。
func (c *AutoDLClient) StartAllInstances() ([]string, error) {
	uuids, err := c.listAllInstanceUUIDs()
	if err != nil {
		return nil, err
	}
	return c.StartInstances(uuids)
}

// StopAllInstances 停止当前账号下的全部容器实例 Pro。
// 先拉取实例列表，再对其全部 uuid 执行 StopInstances。
// 实例列表为空或全部缺少 uuid 时返回 ErrNoInstance。
func (c *AutoDLClient) StopAllInstances() ([]string, error) {
	uuids, err := c.listAllInstanceUUIDs()
	if err != nil {
		return nil, err
	}
	return c.StopInstances(uuids)
}

// listAllInstanceUUIDs 列出当前账号下的全部容器实例 Pro 并提取其 uuid（跳过空 uuid）。
// 列表为空或全部缺少 uuid 时返回 ErrNoInstance。
func (c *AutoDLClient) listAllInstanceUUIDs() ([]string, error) {
	list, err := c.ListInstances()
	if err != nil {
		return nil, err
	}
	var uuids []string
	for _, inst := range list {
		if inst.InstanceUUID != "" {
			uuids = append(uuids, inst.InstanceUUID)
		}
	}
	if len(uuids) == 0 {
		return nil, ErrNoInstance
	}
	return uuids, nil
}