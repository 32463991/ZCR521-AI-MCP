package ops

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func argAny(args map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, exists := args[key]; exists && value != nil {
			return value, true
		}
	}
	return nil, false
}

func argString(args map[string]any, keys ...string) (string, error) {
	value, ok := argAny(args, keys...)
	if !ok {
		return "", fmt.Errorf("缺少参数 %s", strings.Join(keys, "/"))
	}
	switch typed := value.(type) {
	case string:
		return typed, nil
	case json.Number:
		return typed.String(), nil
	default:
		return "", fmt.Errorf("参数 %s 必须是字符串", keys[0])
	}
}

func argOptionalString(args map[string]any, fallback string, keys ...string) (string, error) {
	if _, ok := argAny(args, keys...); !ok {
		return fallback, nil
	}
	return argString(args, keys...)
}

func argBool(args map[string]any, key string, fallback bool) (bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return fallback, nil
	}
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		parsed, err := strconv.ParseBool(typed)
		if err != nil {
			return false, fmt.Errorf("参数 %s 必须是布尔值", key)
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("参数 %s 必须是布尔值", key)
	}
}

func argInt64(args map[string]any, fallback int64, keys ...string) (int64, error) {
	value, ok := argAny(args, keys...)
	if !ok {
		return fallback, nil
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		if math.Trunc(typed) != typed {
			return 0, fmt.Errorf("参数 %s 必须是整数", keys[0])
		}
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("参数 %s 必须是整数", keys[0])
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("参数 %s 必须是整数", keys[0])
	}
}

func argStringSlice(args map[string]any, keys ...string) ([]string, error) {
	value, ok := argAny(args, keys...)
	if !ok {
		return nil, fmt.Errorf("缺少参数 %s", strings.Join(keys, "/"))
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		out := make([]string, 0, len(typed))
		for i, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("参数 %s[%d] 必须是字符串", keys[0], i)
			}
			out = append(out, text)
		}
		return out, nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return []string{}, nil
		}
		return []string{typed}, nil
	default:
		return nil, fmt.Errorf("参数 %s 必须是字符串数组", keys[0])
	}
}

func argStringMap(args map[string]any, key string) (map[string]string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string)
	switch typed := value.(type) {
	case map[string]string:
		for name, item := range typed {
			out[name] = item
		}
	case map[string]any:
		for name, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("参数 %s.%s 必须是字符串", key, name)
			}
			out[name] = text
		}
	default:
		return nil, fmt.Errorf("参数 %s 必须是对象", key)
	}
	return out, nil
}

func argDuration(args map[string]any, fallback time.Duration) (time.Duration, error) {
	if value, ok := args["timeoutMs"]; ok {
		clone := map[string]any{"timeoutMs": value}
		ms, err := argInt64(clone, 0, "timeoutMs")
		if err != nil {
			return 0, err
		}
		if ms <= 0 {
			return 0, fmt.Errorf("timeoutMs 必须大于 0")
		}
		return time.Duration(ms) * time.Millisecond, nil
	}
	if value, ok := args["timeout"]; ok {
		switch typed := value.(type) {
		case string:
			duration, err := time.ParseDuration(typed)
			if err != nil || duration <= 0 {
				return 0, fmt.Errorf("timeout 必须是正数时长，例如 30s")
			}
			return duration, nil
		default:
			clone := map[string]any{"timeout": value}
			seconds, err := argInt64(clone, 0, "timeout")
			if err != nil || seconds <= 0 {
				return 0, fmt.Errorf("timeout 必须是正数秒数")
			}
			return time.Duration(seconds) * time.Second, nil
		}
	}
	return fallback, nil
}
