// 日志动态接口：读取日志文件尾部最新 N 行（内存恒定，不整读文件）。
package web

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// tailReadSize 从文件末尾读取的最大字节数：尾部日志足够覆盖 300 行且内存占用固定。
const tailReadSize = 128 * 1024

// maxLogLineRunes 单行最大字符数，防止超长行（如配置 dump 的 JSON）撑爆内存与界面。
const maxLogLineRunes = 1000

// handleLogs 返回日志文件尾部最新 lines 行（默认 300），倒序（最新在前）。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	lines := 300
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			lines = n
		}
	}
	out := readLogTail(s.logPath, lines)
	writeJSON(w, http.StatusOK, map[string]any{"lines": out})
}

// readLogTail 只读文件末尾 tailReadSize 字节，切行后取最后 n 行（倒序）。
func readLogTail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return []string{}
	}
	offset := int64(0)
	if fi.Size() > tailReadSize {
		offset = fi.Size() - tailReadSize
	}
	buf := make([]byte, fi.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return []string{}
	}
	all := bytes.Split(buf, []byte{'\n'})
	// 去掉最后一段（文件尾可能没有换行符时的空段）
	if len(all) > 0 && len(all[len(all)-1]) == 0 {
		all = all[:len(all)-1]
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	out := make([]string, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		line := string(all[i])
		if line == "" {
			continue
		}
		// 截断超长行
		if r := []rune(line); len(r) > maxLogLineRunes {
			line = string(r[:maxLogLineRunes]) + "…"
		}
		out = append(out, strings.TrimRight(line, "\r"))
	}
	return out
}
