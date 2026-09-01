package bot

import "testing"

// 回归：base62 etag 超出 64 位 int 时不能溢出成全零（2026-09-01 线上事故样本）。
func TestOptimizedEtagToHexOverflow(t *testing.T) {
	cases := []struct {
		etag string
	}{
		{"GjvR7wk9FQT4UwfLoCF0"},
		{"7xkgV3G5Fyte1RQJooJKD"},
		{"5dFmYcUXWns9Cw3Z6bL6eR0vGyIuMhPk3t"},
	}
	for _, c := range cases {
		got := optimizedEtagToHex(c.etag, true)
		if len(got) != 32 {
			t.Errorf("%s: 期望32位hex，得到 %d 位: %s", c.etag, len(got), got)
		}
		if !isHexString(got) {
			t.Errorf("%s: 结果不是hex: %s", c.etag, got)
		}
		if got == "00000000000000000000000000000000" {
			t.Errorf("%s: 结果全零，转换失败", c.etag)
		}
	}
}

func TestOptimizedEtagToHexPassthrough(t *testing.T) {
	// 已是 32 位 hex 直接返回（小写化）；非 V2 原样返回
	if got := optimizedEtagToHex("ABC123456789abcdefABCDEF12345678", true); got != "abc123456789abcdefabcdef12345678" {
		t.Errorf("32位hex应小写直通，得到: %s", got)
	}
	if got := optimizedEtagToHex("GjvR7wk9FQT4UwfLoCF0", false); got != "GjvR7wk9FQT4UwfLoCF0" {
		t.Errorf("非V2应原样返回，得到: %s", got)
	}
	if got := optimizedEtagToHex("bad!etag", true); got != "bad!etag" {
		t.Errorf("含无效字符应原样返回，得到: %s", got)
	}
}
