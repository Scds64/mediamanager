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

// handleLogs 返回日志：带 keyword 时全文件搜索该关键词（大小写不敏感，最多返回 maxSearchResults 条，倒序）；
// 不带 keyword 时返回文件尾部最新 lines 行（默认 300）。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if kw := strings.TrimSpace(r.URL.Query().Get("keyword")); kw != "" {
		out := searchLogFile(s.logPath, kw)
		writeJSON(w, http.StatusOK, map[string]any{"lines": out})
		return
	}
	lines := 300
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			lines = n
		}
	}
	out := readLogTail(s.logPath, lines)
	writeJSON(w, http.StatusOK, map[string]any{"lines": out})
}

// maxSearchResults 关键词搜索最多返回的匹配行数，倒序（最新在前）。
const maxSearchResults = 500

// searchLogFile 全文件按关键词过滤日志行（大小写不敏感）。逐段读取以控制内存，不整读整个大文件。
func searchLogFile(path, kw string) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return []string{}
	}
	needle := strings.ToLower(kw)
	var scanBuf []byte
	if fi.Size() > tailReadSize {
		// 用双倍 tailReadSize 作为搜索窗口，兼顾覆盖范围与内存上限
		offset := fi.Size() - tailReadSize*2
		if offset < 0 {
			offset = 0
		}
		scanBuf = make([]byte, fi.Size()-offset)
		if _, err := f.ReadAt(scanBuf, offset); err != nil && err != io.EOF {
			return []string{}
		}
	} else {
		scanBuf = make([]byte, fi.Size())
		if _, err := f.ReadAt(scanBuf, 0); err != nil && err != io.EOF {
			return []string{}
		}
	}
	parts := bytes.Split(scanBuf, []byte{'\n'})
	// 去掉尾空段
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	out := make([]string, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- { // 倒序：最新在前
		line := strings.TrimRight(string(parts[i]), "\r")
		if line == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		if r := []rune(line); len(r) > maxLogLineRunes {
			line = string(r[:maxLogLineRunes]) + "…"
		}
		out = append(out, line)
		if len(out) >= maxSearchResults {
			break
		}
	}
	return out
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
