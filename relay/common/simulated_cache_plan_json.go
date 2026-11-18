package common

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// ReplaceTopLevelUsageRawJSON 把原始响应 JSON 顶层的 usage 字段替换为 usage 编码
// 后的 JSON；其余字段以 json.RawMessage 原样保留，不整体反序列化成 DTO 再序列化，
// 避免丢掉上游未知字段。解析失败 / usage 为 nil 时原样返回 body。
func ReplaceTopLevelUsageRawJSON(body []byte, usage any) ([]byte, error) {
	if len(body) == 0 || usage == nil {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, err
	}
	// 上游响应原本就没有 usage 字段时不新增（避免给无 usage 的帧/响应补一个 usage）。
	if _, ok := obj["usage"]; !ok {
		return body, nil
	}
	usageRaw, err := common.Marshal(usage)
	if err != nil {
		return body, err
	}
	obj["usage"] = usageRaw
	return common.Marshal(obj)
}

// ReplaceClaudeMessageStartUsageRawJSON 替换 Claude message_start 事件 data 中
// message.usage 字段；事件其余字段（含 message 内除 usage 外的字段）以 RawMessage
// 保留。data 为空 / usage 为 nil / 事件结构不符时原样返回。
func ReplaceClaudeMessageStartUsageRawJSON(data string, usage *dto.ClaudeUsage) (string, error) {
	if data == "" || usage == nil {
		return data, nil
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return data, err
	}
	messageRaw, ok := event["message"]
	if !ok {
		return data, nil
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(messageRaw, &message); err != nil {
		return data, err
	}
	usageRaw, err := common.Marshal(usage)
	if err != nil {
		return data, err
	}
	message["usage"] = usageRaw
	newMessage, err := common.Marshal(message)
	if err != nil {
		return data, err
	}
	event["message"] = newMessage
	out, err := common.Marshal(event)
	if err != nil {
		return data, err
	}
	return string(out), nil
}
