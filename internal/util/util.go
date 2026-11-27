// Package util 放跨包共用的小工具：宽松 JSON 取值、日志截断等。
package util

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// AsInt 把 JSON 解出来的数字/字符串尽量转成 int。
func AsInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	case string:
		var n int
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

// AsString 把 JSON 值转成字符串；json.Number 会保留原始精度。
func AsString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		// 小米登录响应里的 nonce/userId 是 18 位整数，float64 会丢精度
		return t.String()
	case float64:
		// audio_id 有时被设备当数字发出来
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	}
	return ""
}

// Snippet 截断过长的响应体，只用于错误日志。
func Snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// ParseInt64 解析十进制 int64，失败返回 0。
func ParseInt64(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
