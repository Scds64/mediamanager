package web

// 内存趋势历史：服务端持久化，跨设备共享（对应 server.py 的 _mem_history 部分）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	memHistoryFile  = "data/mem_history.json"
	memHistoryMax   = 90 // 60 秒窗口 + 滑出余量（秒）；按 1 秒/点采样约存 90 点
	memSaveInterval = 30 * time.Second
)

func (s *Server) loadMemHistory() {
	data, err := os.ReadFile(memHistoryFile)
	if err != nil {
		return
	}
	var arr [][]float64
	if err := json.Unmarshal(data, &arr); err != nil {
		return
	}
	s.memMu.Lock()
	defer s.memMu.Unlock()
	for _, it := range arr {
		if len(it) == 2 && it[0] > 0 && it[1] > 0 {
			s.memHistory = append(s.memHistory, [2]float64{it[0], it[1]})
		}
	}
	if len(s.memHistory) > memHistoryMax {
		s.memHistory = s.memHistory[len(s.memHistory)-memHistoryMax:]
	}
}

func (s *Server) saveMemHistory(force bool) {
	now := time.Now()
	if !force && now.Sub(s.memLastSave) < memSaveInterval {
		return
	}
	s.memLastSave = now
	s.memMu.Lock()
	hist := append([][2]float64(nil), s.memHistory...)
	s.memMu.Unlock()
	raw, err := json.Marshal(hist)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(memHistoryFile), 0o755)
	_ = os.WriteFile(memHistoryFile, raw, 0o644)
}

func (s *Server) recordMemSample(memMB float64) {
	now := float64(time.Now().UnixNano()) / 1e9
	s.memMu.Lock()
	cutoff := now - memHistoryMax - 10
	kept := make([][2]float64, 0, len(s.memHistory)+1)
	for _, p := range s.memHistory {
		if now-p[0] <= cutoff {
			kept = append(kept, p)
		}
	}
	kept = append(kept, [2]float64{now, memMB})
	if len(kept) > memHistoryMax {
		kept = kept[len(kept)-memHistoryMax:]
	}
	s.memHistory = kept
	s.memMu.Unlock()
	s.saveMemHistory(false)
}

func (s *Server) memSamplerLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.memStop:
			return
		case <-ticker.C:
			s.recordMemSample(readMemMB())
		}
	}
}

// readMemMB 读取内存使用量：
//   - 容器内优先读取 cgroup（对齐 docker stats）
//   - 非容器环境读取进程自身 RSS（/proc/self/status）
//   - Windows 回退到 runtime.ReadMemStats
func readMemMB() float64 {
	if v, ok := readCgroupMem(); ok {
		return v
	}
	if runtime.GOOS == "linux" {
		if v, ok := procSelfRSSMB(); ok {
			return v
		}
	}
	return windowsMemUsedMB()
}

func readCgroupMem() (float64, bool) {
	candidates := [][2]string{
		{"/sys/fs/cgroup/memory.current", "/sys/fs/cgroup/memory.stat"},
		{"/sys/fs/cgroup/memory/memory.usage_in_bytes", "/sys/fs/cgroup/memory/memory.stat"},
	}
	for _, pair := range candidates {
		data, err := os.ReadFile(pair[0])
		if err != nil {
			continue
		}
		var used int64
		if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &used); err != nil {
			continue
		}
		if statData, err := os.ReadFile(pair[1]); err == nil {
			for _, line := range strings.Split(string(statData), "\n") {
				if !strings.HasPrefix(line, "inactive_file ") {
					continue
				}
				var v int64
				if _, err := fmt.Sscanf(line, "inactive_file %d", &v); err == nil {
					used -= v
				}
			}
		}
		return round1(float64(used) / 1024 / 1024), true
	}
	return 0, false
}

func procSelfRSSMB() (float64, bool) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			var v int64
			if _, err := fmt.Sscanf(line, "VmRSS: %d kB", &v); err == nil {
				return round1(float64(v) / 1024), true
			}
		}
	}
	return 0, false
}

func windowsMemUsedMB() float64 {
	// 无 psutil 依赖：用 runtime 内存估算（容器场景以 cgroup 为准）
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return round1(float64(m.Sys) / 1024 / 1024)
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
