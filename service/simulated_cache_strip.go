package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

// 模拟缓存开启时，缓存 TTL 由本地引擎接管；这些字段只用于本地判定，
// 不能再发给上游，否则上游可能按自己的 TTL/价格再建一次缓存。
var simulatedCacheTTLStripKeys = map[string]struct{}{
	"cache_control":          {},
	"prompt_cache_options":   {},
	"prompt_cache_retention": {},
	// OpenAI GPT-5.6+ 的显式缓存断点；本地模拟不依赖上游断点。
	"prompt_cache_breakpoint": {},
}

// StripSimulatedCacheTTLFromJSON 从发往上游的 JSON 请求体中移除缓存 TTL
// 及其承载字段。它只处理对象/数组里的字段，不扫描字符串内容；未命中任何
// 字段时原样返回并置 changed=false。
func StripSimulatedCacheTTLFromJSON(raw []byte) ([]byte, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, false, nil
	}
	out, changed, err := stripSimulatedCacheTTLValue(json.RawMessage(raw))
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return raw, false, nil
	}
	return []byte(out), true, nil
}

func stripSimulatedCacheTTLValue(raw json.RawMessage) (json.RawMessage, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, false, nil
	}
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, false, fmt.Errorf("strip simulated cache TTL: %w", err)
		}
		changed := false
		for key, value := range obj {
			if _, strip := simulatedCacheTTLStripKeys[key]; strip {
				delete(obj, key)
				changed = true
				continue
			}
			nested, nestedChanged, err := stripSimulatedCacheTTLValue(value)
			if err != nil {
				return nil, false, err
			}
			if nestedChanged {
				obj[key] = nested
				changed = true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := marshalSimulatedCacheJSON(obj)
		if err != nil {
			return nil, false, err
		}
		return out, true, nil
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, false, fmt.Errorf("strip simulated cache TTL: %w", err)
		}
		changed := false
		for i, value := range arr {
			nested, nestedChanged, err := stripSimulatedCacheTTLValue(value)
			if err != nil {
				return nil, false, err
			}
			if nestedChanged {
				arr[i] = nested
				changed = true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := marshalSimulatedCacheJSON(arr)
		if err != nil {
			return nil, false, err
		}
		return out, true, nil
	default:
		return raw, false, nil
	}
}

// marshalSimulatedCacheJSON 关闭 HTML 转义，避免改写 prompt 文本里的 < > &。
func marshalSimulatedCacheJSON(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// StripSimulatedCacheTTLForUpstream 只在渠道开启模拟缓存时剥离 TTL 字段。
// info.Request 仍保留原始请求，供本地 TTL 判定与响应改写使用。
func StripSimulatedCacheTTLForUpstream(jsonData []byte, info *relaycommon.RelayInfo) ([]byte, error) {
	if info == nil || !info.ChannelOtherSettings.SimulatedCacheCreationEnabled {
		return jsonData, nil
	}
	out, changed, err := StripSimulatedCacheTTLFromJSON(jsonData)
	if err != nil {
		return nil, err
	}
	if !changed {
		return jsonData, nil
	}
	return out, nil
}

// BuildSimulatedCachePassThroughBody 为透传请求构造发往上游的 body。
// 关闭模拟缓存时保持原 storage 的流式复用；开启时读取原始 body、剥离 TTL
// 后重新建 storage。返回的 closer 仅在重建 storage 时非 nil。
func BuildSimulatedCachePassThroughBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, func(), error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, nil, err
	}
	if info == nil || !info.ChannelOtherSettings.SimulatedCacheCreationEnabled {
		return common.NewReplayableBodyReader(storage), nil, nil
	}
	raw, err := storage.Bytes()
	if err != nil {
		return nil, nil, err
	}
	stripped, changed, err := StripSimulatedCacheTTLFromJSON(raw)
	if err != nil {
		return nil, nil, err
	}
	if !changed {
		return common.NewReplayableBodyReader(storage), nil, nil
	}
	newStorage, err := common.CreateBodyStorage(stripped)
	if err != nil {
		return nil, nil, err
	}
	return common.NewReplayableBodyReader(newStorage), func() { _ = newStorage.Close() }, nil
}
