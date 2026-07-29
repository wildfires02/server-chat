package server

import (
	"encoding/json"
	"time"

	"chat/server/logs"
)

// interfaceMapToByteMap 完成interface映射ToByte映射所需的内部处理。
func interfaceMapToByteMap(in map[string]any) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for key, val := range in {
		if val != nil {
			out[key], _ = json.Marshal(val)
		}
	}
	return out
}

// byteMapToInterfaceMap 完成byte映射ToInterface映射所需的内部处理。
func byteMapToInterfaceMap(in map[string][]byte) map[string]any {
	out := make(map[string]any, len(in))
	for key, raw := range in {
		if val := bytesToInterface(raw); val != nil {
			out[key] = val
		}
	}
	return out
}

// interfaceToBytes 完成interfaceToBytes所需的内部处理。
func interfaceToBytes(in any) []byte {
	if in != nil {
		out, _ := json.Marshal(in)
		return out
	}
	return nil
}

// bytesToInterface 完成bytesToInterface所需的内部处理。
func bytesToInterface(in []byte) any {
	var out any
	if len(in) > 0 {
		err := json.Unmarshal(in, &out)
		if err != nil {
			logs.Warn.Println("pbx: failed to parse bytes", string(in), err)
		}
	}
	return out
}

// timeToInt64 将时间统一编码为 Epoch 毫秒；nil 编码为协议约定的 0。
func timeToInt64(ts *time.Time) int64 {
	if ts != nil {
		return ts.UnixNano() / int64(time.Millisecond)
	}
	return 0
}

// int64ToTime 从 Epoch 毫秒解码 UTC 时间；0 表示字段未设置。
func int64ToTime(ts int64) *time.Time {
	if ts > 0 {
		res := time.UnixMilli(ts).UTC()
		return &res
	}
	return nil
}
