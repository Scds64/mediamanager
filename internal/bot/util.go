package bot

// 通用工具函数（对应 123bot.py 的 _format_size / get_int_env 等辅助）。

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// FormatSize 字节数转可读大小（对应 _format_size，min_unit 可指定最小单位）。
func FormatSize(size int64, minUnit string) string {
	if size < 0 {
		size = 0
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	val := float64(size)
	idx := 0
	for val >= 1024 && idx < len(units)-1 {
		val /= 1024
		idx++
	}
	// 如果低于 minUnit 则强制升级显示
	if minUnit != "" {
		mi := unitIndex(minUnit)
		if mi > idx {
			for idx < mi {
				val /= 1024
				idx++
			}
		}
	}
	if idx == 0 {
		return fmt.Sprintf("%.0f %s", val, units[idx])
	}
	return fmt.Sprintf("%.2f %s", val, units[idx])
}

func unitIndex(u string) int {
	switch strings.ToUpper(u) {
	case "B":
		return 0
	case "KB":
		return 1
	case "MB":
		return 2
	case "GB":
		return 3
	case "TB":
		return 4
	case "PB":
		return 5
	}
	return -1
}

// jsonUnmarshal 解析 json.RawMessage。
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// fullToHalf 全角数字转半角。
func fullToHalf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '０' && r <= '９' {
			b.WriteRune('0' + r - '０')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// parseAnyInt 解析字符串为整数，兼容全角数字。
func parseAnyInt(s string) (int, error) {
	s = fullToHalf(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("空输入")
	}
	return strconv.Atoi(s)
}
