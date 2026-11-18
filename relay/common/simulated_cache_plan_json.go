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

// ReplaceResponsesUsageRawJSON 只改 Responses 响应的顶层 usage，保留 input_tokens /
// output_tokens / input_tokens_details 的官方字段形态，不把 Chat 的 prompt_tokens
// 字段混进 Responses 响应，也不丢失 usage 子树里的未知字段。
func ReplaceResponsesUsageRawJSON(body []byte, usage *dto.Usage) ([]byte, error) {
	if len(body) == 0 || usage == nil {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, err
	}
	usageRaw, ok := obj["usage"]
	if !ok {
		return body, nil
	}
	patched, err := patchResponsesUsageRaw(usageRaw, usage)
	if err != nil {
		return body, err
	}
	obj["usage"] = patched
	return common.Marshal(obj)
}

// ReplaceResponsesEventUsageRawJSON 只改 Responses SSE 事件 response.usage，
// response 对象其余字段与事件外层字段以 RawMessage 保留。
func ReplaceResponsesEventUsageRawJSON(data string, usage *dto.Usage) (string, error) {
	if data == "" || usage == nil {
		return data, nil
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return data, err
	}
	responseRaw, ok := event["response"]
	if !ok {
		return data, nil
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		return data, err
	}
	usageRaw, ok := response["usage"]
	if !ok {
		return data, nil
	}
	patched, err := patchResponsesUsageRaw(usageRaw, usage)
	if err != nil {
		return data, err
	}
	response["usage"] = patched
	newResponse, err := common.Marshal(response)
	if err != nil {
		return data, err
	}
	event["response"] = newResponse
	out, err := common.Marshal(event)
	if err != nil {
		return data, err
	}
	return string(out), nil
}

// ReplaceResponsesUsageRawJSONClean 用一份干净的官方 Responses usage 形态替换顶层
// usage，丢弃从 Chat usage 继承来的 prompt_tokens / completion_tokens 等字段。
// 仅用于 Chat→Responses 转换输出，避免客户端看到混合 schema。
func ReplaceResponsesUsageRawJSONClean(body []byte, usage *dto.Usage) ([]byte, error) {
	if len(body) == 0 || usage == nil {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, err
	}
	clean, err := responsesUsageCleanJSON(usage)
	if err != nil {
		return body, err
	}
	obj["usage"] = clean
	return common.Marshal(obj)
}

// ReplaceResponsesEventUsageRawJSONClean 同 ReplaceResponsesEventUsageRawJSON，
// 但把 response.usage 整体换成干净的官方 Responses 形态。
func ReplaceResponsesEventUsageRawJSONClean(data string, usage *dto.Usage) (string, error) {
	if data == "" || usage == nil {
		return data, nil
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return data, err
	}
	responseRaw, ok := event["response"]
	if !ok {
		return data, nil
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		return data, err
	}
	clean, err := responsesUsageCleanJSON(usage)
	if err != nil {
		return data, err
	}
	response["usage"] = clean
	newResponse, err := common.Marshal(response)
	if err != nil {
		return data, err
	}
	event["response"] = newResponse
	out, err := common.Marshal(event)
	if err != nil {
		return data, err
	}
	return string(out), nil
}

func responsesUsageCleanJSON(usage *dto.Usage) (json.RawMessage, error) {
	input := usage.InputTokens
	if input == 0 {
		input = usage.PromptTokens
	}
	output := usage.OutputTokens
	if output == 0 {
		output = usage.CompletionTokens
	}
	obj := map[string]any{
		"input_tokens":  input,
		"output_tokens": output,
		"total_tokens":  input + output,
	}
	details := map[string]any{}
	if usage.InputTokensDetails != nil {
		details["cached_tokens"] = usage.InputTokensDetails.CachedTokens
		details["cache_write_tokens"] = usage.InputTokensDetails.CacheWriteTokens
		if usage.InputTokensDetails.CachedCreationTokens != 0 {
			details["cached_creation_tokens"] = usage.InputTokensDetails.CachedCreationTokens
		}
		if usage.InputTokensDetails.TextTokens != 0 {
			details["text_tokens"] = usage.InputTokensDetails.TextTokens
		}
		if usage.InputTokensDetails.AudioTokens != 0 {
			details["audio_tokens"] = usage.InputTokensDetails.AudioTokens
		}
		if usage.InputTokensDetails.ImageTokens != 0 {
			details["image_tokens"] = usage.InputTokensDetails.ImageTokens
		}
	}
	obj["input_tokens_details"] = details
	outDetails := map[string]any{}
	if usage.CompletionTokenDetails.ReasoningTokens != 0 {
		outDetails["reasoning_tokens"] = usage.CompletionTokenDetails.ReasoningTokens
	}
	if usage.CompletionTokenDetails.AudioTokens != 0 {
		outDetails["audio_tokens"] = usage.CompletionTokenDetails.AudioTokens
	}
	obj["output_tokens_details"] = outDetails
	b, err := common.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func patchResponsesUsageRaw(usageRaw json.RawMessage, usage *dto.Usage) (json.RawMessage, error) {
	if usage == nil {
		return usageRaw, nil
	}
	var obj map[string]json.RawMessage
	if len(usageRaw) > 0 && string(usageRaw) != "null" {
		if err := json.Unmarshal(usageRaw, &obj); err != nil {
			return usageRaw, err
		}
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	input := usage.InputTokens
	if input == 0 {
		input = usage.PromptTokens
	}
	output := usage.OutputTokens
	if output == 0 {
		output = usage.CompletionTokens
	}
	if input != 0 {
		obj["input_tokens"] = mustJSON(input)
	}
	if output != 0 {
		obj["output_tokens"] = mustJSON(output)
	}
	if input != 0 || output != 0 {
		obj["total_tokens"] = mustJSON(input + output)
	}
	if usage.InputTokensDetails != nil {
		detailsRaw, ok := obj["input_tokens_details"]
		var details map[string]json.RawMessage
		if ok && len(detailsRaw) > 0 && string(detailsRaw) != "null" {
			if err := json.Unmarshal(detailsRaw, &details); err != nil {
				return usageRaw, err
			}
		}
		if details == nil {
			details = map[string]json.RawMessage{}
		}
		details["cached_tokens"] = mustJSON(usage.InputTokensDetails.CachedTokens)
		details["cache_write_tokens"] = mustJSON(usage.InputTokensDetails.CacheWriteTokens)
		if _, exists := details["cached_creation_tokens"]; exists {
			details["cached_creation_tokens"] = mustJSON(usage.InputTokensDetails.CachedCreationTokens)
		}
		detailsJSON, err := common.Marshal(details)
		if err != nil {
			return usageRaw, err
		}
		obj["input_tokens_details"] = detailsJSON
	}
	out, err := common.Marshal(obj)
	if err != nil {
		return usageRaw, err
	}
	return out, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := common.Marshal(v)
	return b
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
