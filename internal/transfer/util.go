package transfer

// 包内通用工具函数。

import (
	"strconv"
)

// fmtSscan 解析字符串为整数（用于 JSON 数字字段兼容）。
func fmtSscan(s string, n *int) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*n = v
	return nil
}

func parseVersionScoreValue(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}
