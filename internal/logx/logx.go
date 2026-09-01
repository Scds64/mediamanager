// Package logx 提供按天 + 按大小双条件轮转的日志写入器（对应 Python log_rotate.py）。
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RotatingWriter 按天轮转，且单文件达到 maxBytes 时也立即轮转。
// 归档文件保留最近 interval（默认 24h）内的，更早的自动删除。
type RotatingWriter struct {
	mu        sync.Mutex
	filename  string
	maxBytes  int64
	file      *os.File
	lastWrite time.Time
}

// NewRotatingWriter 创建轮转日志写入器。maxBytes<=0 表示不按大小轮转。
func NewRotatingWriter(filename string, maxBytes int64) (*RotatingWriter, error) {
	// lastWrite 初始化为当前时间：避免进程启动时首条日志被误判为跨天而立即归档
	w := &RotatingWriter{filename: filename, maxBytes: maxBytes, lastWrite: time.Now()}
	if err := w.open(); err != nil {
		return nil, err
	}
	// 启动即清理超过 24 小时的归档文件（此前只在轮转时清理，进程常驻不轮转时会堆积）
	w.cleanupOld(time.Now())
	return w, nil
}

func (w *RotatingWriter) open() error {
	if err := os.MkdirAll(filepath.Dir(w.filename), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.file = f
	return nil
}

// Write 实现 io.Writer。
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if w.file == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	// 跨天轮转
	if w.lastWrite.Day() != now.Day() {
		w.rollover(now)
	}
	// 按大小轮转
	if w.maxBytes > 0 {
		if fi, err := w.file.Stat(); err == nil && fi.Size() >= w.maxBytes {
			w.rollover(now)
		}
	}
	w.lastWrite = now
	return w.file.Write(p)
}

func (w *RotatingWriter) rollover(now time.Time) {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
	// 归档名：轮转当天日期；同一天内多次轮转追加序号避免覆盖
	dfn := w.filename + "." + now.Format("2006-01-02")
	n := 0
	target := dfn
	for {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		n++
		target = fmt.Sprintf("%s.%d", dfn, n)
	}
	if _, err := os.Stat(w.filename); err == nil {
		_ = os.Rename(w.filename, target)
	}
	_ = w.open()
	w.cleanupOld(now)
}

// cleanupOld 删除超过 24 小时的归档文件。
func (w *RotatingWriter) cleanupOld(now time.Time) {
	cutoff := now.Add(-24 * time.Hour)
	prefix := w.filename + "."
	dir := filepath.Dir(w.filename)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if !strings.HasPrefix(full, prefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(full)
		}
	}
}

// Close 关闭底层文件。
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}
