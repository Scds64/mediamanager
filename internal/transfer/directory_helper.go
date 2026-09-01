package transfer

// 目录映射配置模块（对应 directory_helper.py）。
// 管理 source_pid → library_pid 的映射配置（多目录支持）。配置文件：config/directories.json

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
)

// TransferDirectoryConf 单个目录映射配置。
type TransferDirectoryConf struct {
	Name           string `json:"name"`
	SourcePID      int    `json:"source_pid"`
	LibraryPID     int    `json:"library_pid"`
	MediaType      string `json:"media_type"`
	MediaCategory  string `json:"media_category"`
	Priority       int    `json:"priority"`
	Monitor        bool   `json:"monitor"`
	TransferType   string `json:"transfer_type"`
}

var dirFileMu sync.Mutex

// DirectoryHelper 目录映射配置管理器。
type DirectoryHelper struct {
	DirsFile string
	cache    []*TransferDirectoryConf
	cacheMTime int64
}

// NewDirectoryHelper 创建目录映射配置管理器。
func NewDirectoryHelper(dirsFile string) *DirectoryHelper {
	return &DirectoryHelper{DirsFile: dirsFile}
}

// LoadDirs 读取目录映射配置（带 mtime 缓存）。
func (d *DirectoryHelper) LoadDirs(forceReload bool) []*TransferDirectoryConf {
	info, err := os.Stat(d.DirsFile)
	if err != nil {
		log.Printf("目录映射配置文件不存在: %s", d.DirsFile)
		return nil
	}
	mtime := info.ModTime().Unix()
	if !forceReload && d.cache != nil && mtime == d.cacheMTime {
		return d.cache
	}
	data, err := os.ReadFile(d.DirsFile)
	if err != nil {
		log.Printf("读取目录映射配置失败 (%s): %v", d.DirsFile, err)
		return nil
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Printf("目录映射配置格式错误，应为列表: %s", d.DirsFile)
		return nil
	}
	var dirs []*TransferDirectoryConf
	for _, item := range raw {
		dirs = append(dirs, confFromMap(item))
	}
	// 按优先级排序
	sort.SliceStable(dirs, func(i, j int) bool { return dirs[i].Priority < dirs[j].Priority })
	d.cache = dirs
	d.cacheMTime = mtime
	return dirs
}

func confFromMap(m map[string]any) *TransferDirectoryConf {
	c := &TransferDirectoryConf{}
	if v, ok := m["name"].(string); ok {
		c.Name = v
	}
	if v, ok := numVal(m["source_pid"]); ok {
		c.SourcePID = v
	}
	if v, ok := numVal(m["library_pid"]); ok {
		c.LibraryPID = v
	}
	if v, ok := m["media_type"].(string); ok {
		c.MediaType = v
	}
	if v, ok := m["media_category"].(string); ok {
		c.MediaCategory = v
	}
	if v, ok := numVal(m["priority"]); ok {
		c.Priority = v
	}
	if v, ok := m["monitor"].(bool); ok {
		c.Monitor = v
	}
	if v, ok := m["transfer_type"].(string); ok {
		c.TransferType = v
	}
	return c
}

// SaveDirs 保存目录映射配置。
func (d *DirectoryHelper) SaveDirs(dirs []map[string]any) bool {
	dirFileMu.Lock()
	defer dirFileMu.Unlock()
	if err := os.MkdirAll(dirOf(d.DirsFile), 0o755); err != nil {
		return false
	}
	data, err := json.MarshalIndent(dirs, "", "  ")
	if err != nil {
		return false
	}
	if err := os.WriteFile(d.DirsFile, data, 0o644); err != nil {
		log.Printf("保存目录映射配置失败 (%s): %v", d.DirsFile, err)
		return false
	}
	d.cache = nil // 失效缓存
	log.Printf("已保存目录映射配置: %s", d.DirsFile)
	return true
}

// Match 按 media_type + category 匹配目录。
func (d *DirectoryHelper) Match(mediaType, category string) *TransferDirectoryConf {
	dirs := d.LoadDirs(false)
	if len(dirs) == 0 {
		return nil
	}
	var matched []*TransferDirectoryConf
	for _, dirConf := range dirs {
		if dirConf.MediaType != "" && mediaType != "" && dirConf.MediaType != mediaType {
			continue
		}
		if dirConf.MediaCategory != "" && category != "" && dirConf.MediaCategory != category {
			continue
		}
		matched = append(matched, dirConf)
	}
	if len(matched) > 0 {
		return matched[0]
	}
	return nil
}

// GetMonitorDirs 获取所有配置为 monitor=true 的目录。
func (d *DirectoryHelper) GetMonitorDirs() []*TransferDirectoryConf {
	var out []*TransferDirectoryConf
	for _, dirConf := range d.LoadDirs(false) {
		if dirConf.Monitor && dirConf.SourcePID > 0 {
			out = append(out, dirConf)
		}
	}
	return out
}

// GetDirBySource 根据源目录 PID 查找配置。
func (d *DirectoryHelper) GetDirBySource(sourcePID int) *TransferDirectoryConf {
	for _, dirConf := range d.LoadDirs(false) {
		if dirConf.SourcePID == sourcePID {
			return dirConf
		}
	}
	return nil
}

func numVal(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case json.Number:
		n, err := t.Int64()
		if err == nil {
			return int(n), true
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}
