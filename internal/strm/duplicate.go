// Emby 媒体查重扫描（对应 emby_scanner_engine/runtime.py）。
// 扫描本地 STRM 库，按标题/年份/季集分组识别重复版本；删除统一走 delete.go 四件套。
package strm

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mmbot/internal/config"
)

// ---------- 版本质量标签提取 ----------

var qualityRegexps = []struct {
	re  *regexp.Regexp
	tag string
}{
	{regexp.MustCompile(`\b(2160P|4K|UHD|8K|2155P)\b`), "4K"},
	{regexp.MustCompile(`\b1080P\b`), "1080P"},
	{regexp.MustCompile(`\b720P\b`), "720P"},
	{regexp.MustCompile(`\b(480P|576P)\b`), "SD"},
	{regexp.MustCompile(`UHD\s*BLU[- ]?RAY|ULTRA\s*HD\s*BLU[- ]?RAY`), "UHD BluRay"},
	{regexp.MustCompile(`\b(BLURAY|BLU-RAY|BDREMUX|REMUX)\b`), "BluRay"},
	{regexp.MustCompile(`\b(WEB[- ]?DL|WEBRIP)\b`), "WEB-DL"},
	{regexp.MustCompile(`\bBDRIP\b`), "BDRip"},
	{regexp.MustCompile(`\b3D\b`), "3D"},
	{regexp.MustCompile(`\bHDR10\+?\b`), "HDR10+"},
	{regexp.MustCompile(`\b(DOLBY\s*VISION|DOVI|DV)\b`), "DV"},
	{regexp.MustCompile(`\bHDR\b`), "HDR"},
	{regexp.MustCompile(`\bHLG\b`), "HLG"},
	{regexp.MustCompile(`\bSDR\b`), "SDR"},
	{regexp.MustCompile(`\b(HEVC|H\.?265|X265)\b`), "H265"},
	{regexp.MustCompile(`\b(H\.?264|AVC|X264)\b`), "H264"},
	{regexp.MustCompile(`\bMPEG1\b`), "MPEG1"},
	{regexp.MustCompile(`\bMPEG2\b`), "MPEG2"},
	{regexp.MustCompile(`\bTRUEHD\b`), "TrueHD"},
	{regexp.MustCompile(`\bATMOS\b`), "Atmos"},
	{regexp.MustCompile(`\bDTS[- ]?HD\s*MA\b`), "DTS-HD MA"},
	{regexp.MustCompile(`\bDTS[- ]?HD\b`), "DTS-HD"},
	{regexp.MustCompile(`\b(DDP|DD\+|EAC3)\b`), "DDP"},
	{regexp.MustCompile(`\bAAC\b`), "AAC"},
	{regexp.MustCompile(`\bFLAC\b`), "FLAC"},
	{regexp.MustCompile(`\b(PCM|LPCM)\b`), "PCM"},
	{regexp.MustCompile(`\b10[- ]?BIT\b`), "10bit"},
}

var dtsPlainRe = regexp.MustCompile(`\bDTS\b`)

// QualityFromFilename 从 STRM 文件名提取版本质量标签（分辨率/介质/HDR/编码/音轨等）。
func QualityFromFilename(p string) string {
	if p == "" {
		return ""
	}
	base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(filepath.Base(p)))
	u := strings.ToUpper(base)
	var tags []string
	hasTag := func(tag string) bool {
		for _, t := range tags {
			if t == tag {
				return true
			}
		}
		return false
	}
	for _, qr := range qualityRegexps {
		if qr.tag == "BluRay" && hasTag("UHD BluRay") {
			continue
		}
		if qr.tag == "DTS-HD" && hasTag("DTS-HD MA") {
			continue
		}
		if !hasTag(qr.tag) && qr.re.MatchString(u) {
			tags = append(tags, qr.tag)
		}
	}
	// DTS 需排除 DTS-HD/DTS-HD MA（RE2 无负向断言，手动判断后续字符）
	if !hasTag("DTS") && !hasTag("DTS-HD") && !hasTag("DTS-HD MA") {
		for _, loc := range dtsPlainRe.FindAllStringIndex(u, -1) {
			if loc[1] < len(u) && u[loc[1]] == '-' {
				continue
			}
			tags = append(tags, "DTS")
			break
		}
	}
	return strings.Join(tags, " ")
}

// ---------- 查重扫描 ----------

// DupFile 重复分组中的单个文件。
type DupFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	Info string `json:"info"`
	DV   bool   `json:"dv"`
}

// DupGroup 重复分组。
type DupGroup struct {
	Label         string    `json:"label"`
	Files         []DupFile `json:"files"`
	RedundantSize int64     `json:"redundant_size"`
}

// ScanResult 查重扫描结果（键为中文分类名，与前端一致）。
type ScanResult map[string]any

// scanKey 查重分组键。
type scanKey struct {
	kind    string
	title   string
	year    int
	season  int
	episode int
}

// ScanDuplicates 扫描本地 STRM 库，按(标题,年份[,季,集])分组识别重复版本。
// 两遍扫描架构：第一遍只统计分组数量，第二遍只收集可能存在重复（数量>=2）的分组详情。
func ScanDuplicates(strmPaths string) ScanResult {
	roots := LocalRoots(strmPaths)
	if len(roots) == 0 {
		return nil
	}

	// 第一遍：统计分组数量
	counts := map[scanKey]int{}
	IterStrmFiles(roots, func(p string) {
		meta := ParseMeta(p)
		if meta.Title == "" {
			return
		}
		size := ReadURL(p).Size
		if size == 0 {
			return
		}
		if meta.Kind == "tv" {
			if meta.Episode == -1 {
				return
			}
		}
		key := scanKey{kind: meta.Kind, title: meta.Title, year: meta.Year, season: meta.Season, episode: meta.Episode}
		counts[key]++
	})

	// 第二遍：收集可能存在重复的分组详情
	groups := map[scanKey][]DupFile{}
	IterStrmFiles(roots, func(p string) {
		meta := ParseMeta(p)
		if meta.Title == "" {
			return
		}
		info := ReadURL(p)
		if info.Size == 0 {
			return
		}
		if meta.Kind == "tv" && meta.Episode == -1 {
			return
		}
		key := scanKey{kind: meta.Kind, title: meta.Title, year: meta.Year, season: meta.Season, episode: meta.Episode}
		if counts[key] < 2 {
			return
		}
		qt := QualityFromFilename(p)
		groups[key] = append(groups[key], DupFile{
			ID:   p,
			Name: meta.Label,
			Path: p,
			Size: info.Size,
			Info: qt,
			DV:   strings.Contains(qt, "DV"),
		})
	})

	result := ScanResult{}
	for key, files := range groups {
		if len(files) < 2 {
			continue
		}
		seenPaths := map[string]bool{}
		unique := 0
		for _, f := range files {
			if !seenPaths[f.Path] {
				seenPaths[f.Path] = true
				unique++
			}
		}
		if unique < 2 {
			continue
		}
		libName := "电视剧"
		if key.kind == "movie" {
			libName = "电影"
		}
		var total, maxSize int64
		for _, f := range files {
			total += f.Size
			if f.Size > maxSize {
				maxSize = f.Size
			}
		}
		cur, _ := result[libName].([]DupGroup)
		result[libName] = append(cur, DupGroup{
			Label:         files[0].Name,
			Files:         files,
			RedundantSize: total - maxSize,
		})
	}

	return result
}

// ---------- 锁文件 ----------

const scanLockFile = "emby_scan.lock"

// ScanLockFilePath 返回锁文件路径。
func ScanLockFilePath() string { return scanLockFile }

// IsScanBusy 检查锁文件是否存在（进程是否正在扫描）。
func IsScanBusy() bool {
	_, err := os.Stat(scanLockFile)
	return err == nil
}

// ---------- 结果文件 ----------

// scanResultFile 查重结果持久化文件（data/ 挂载卷下，容器重建不丢失）。
const scanResultFile = "data/emby_scan_result.json"

// ScanResultFile 返回结果文件路径。
func ScanResultFilePath() string { return scanResultFile }

// LoadScanResult 从文件读取查重结果。
// 结果文件由子进程（-scan-duplicates）写出，读取时必须按分类还原为强类型，
// 否则 json 会把分组数组解成 []interface{}，导致删除/清理时的类型断言全部失败。
func LoadScanResult() (ScanResult, string) {
	file, err := os.Open(scanResultFile)
	if err != nil {
		return nil, ""
	}
	defer file.Close()
	dec := json.NewDecoder(file)
	startToken, err := dec.Token()
	if err != nil {
		return nil, ""
	}
	start, ok := startToken.(json.Delim)
	if !ok || start != '{' {
		return nil, ""
	}
	result := ScanResult{}
	var lastScan string
	for dec.More() {
		fieldToken, err := dec.Token()
		if err != nil {
			return nil, ""
		}
		field, ok := fieldToken.(string)
		if !ok {
			return nil, ""
		}
		switch field {
		case "last_scan":
			if err := dec.Decode(&lastScan); err != nil {
				return nil, ""
			}
		case "scan_result":
			if err := decodeScanResult(dec, result); err != nil {
				return nil, ""
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, ""
	}
	return result, lastScan
}

func decodeScanResult(dec *json.Decoder, result ScanResult) error {
	if _, err := dec.Token(); err != nil {
		return err
	}
	for dec.More() {
		fieldToken, err := dec.Token()
		if err != nil {
			return err
		}
		field, ok := fieldToken.(string)
		if !ok {
			return fmt.Errorf("查重结果分类名称无效")
		}
		if field == "stats" {
			var ignored map[string]any
			if err := dec.Decode(&ignored); err != nil {
				return err
			}
			continue
		}
		var groups []DupGroup
		if err := dec.Decode(&groups); err != nil {
			return err
		}
		result[field] = groups
	}
	_, err := dec.Token()
	return err
}

// SaveScanResult 持久化查重结果。
func SaveScanResult(result ScanResult) {
	lastScan := time.Now().Format("2006-01-02 15:04:05")
	payload := map[string]any{"scan_result": result, "last_scan": lastScan}
	raw, _ := json.MarshalIndent(payload, "", "  ")
	if err := os.MkdirAll(filepath.Dir(scanResultFile), 0o755); err != nil {
		log.Printf("创建结果目录失败: %v", err)
	}
	if err := os.WriteFile(scanResultFile, raw, 0o644); err != nil {
		log.Printf("保存查重结果失败: %v", err)
	}
}

// ---------- 子进程扫描（CLI 入口） ----------

// RunScanCLI 在子进程中执行查重扫描：读取 ENV_STRM_PATHS，执行扫描，写结果文件，清理锁文件，退出。
// 由 mmbot -scan-duplicates 调用。
func RunScanCLI(envFilePath string) {
	// 创建锁文件
	if err := os.WriteFile(scanLockFile, []byte{}, 0o644); err != nil {
		log.Printf("创建锁文件失败: %v", err)
		os.Exit(1)
	}
	defer os.Remove(scanLockFile)

	// 读取配置
	env, err := config.Load(envFilePath)
	if err != nil {
		log.Printf("加载配置文件失败: %v", err)
		os.Exit(1)
	}
	strmPaths := env.Get("ENV_STRM_PATHS", "")
	if strmPaths == "" {
		log.Printf("未配置 STRM 映射（ENV_STRM_PATHS），无法扫描本地 STRM 库")
		os.Exit(1)
	}

	log.Printf("查重扫描开始...")
	result := ScanDuplicates(strmPaths)
	SaveScanResult(result)
	log.Printf("查重扫描完成")
}

// ---------- 主进程接口 ----------

// RunScan 启动一次性子进程执行查重扫描。
// 返回 error 字符串；空串表示已启动。
func RunScan(selfExePath string) string {
	// 检查锁文件
	if IsScanBusy() {
		return "查重扫描正在执行中，请稍后再试"
	}
	// 通过子进程执行扫描，不继承主进程的 goroutine 和内存
	cmd := exec.Command(selfExePath, "-scan-duplicates")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("启动查重扫描子进程失败: %v", err)
	}
	// Wait goroutine 回收子进程，防止其退出后成为僵尸（mmbot 是容器 PID 1，无人替它收尸）
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("查重扫描子进程结束: %v", err)
		}
	}()
	return ""
}

// ---------- 全局运行时（仅用于读取结果，不再管理扫描生命周期） ----------

// ScanRuntime 媒体查重运行时（全局单例，配置实时读取）。
type ScanRuntime struct {
	Configured bool
	Busy       bool
	LastScan   string
	ScanResult ScanResult
	ScanError  string

	mu sync.Mutex
}

// RemoveItems 删除后清空重复分组，仅保留媒体统计。
func (r *ScanRuntime) RemoveItems() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ScanResult == nil {
		return
	}
	for lib, v := range r.ScanResult {
		if _, ok := v.([]DupGroup); ok {
			delete(r.ScanResult, lib)
		}
	}
	r.SaveScanResult()
}

// SaveScanResult 持久化查重结果。
func (r *ScanRuntime) SaveScanResult() {
	if r.ScanResult == nil {
		return
	}
	SaveScanResult(r.ScanResult)
}

// ---------- 全局实例 ----------

var (
	scanRtMu sync.RWMutex
	scanRt   *ScanRuntime
)

// EnsureScanRuntime 获取/刷新全局查重运行时（每次调用重读配置）。
func EnsureScanRuntime(getEnv func(string, string) string) *ScanRuntime {
	scanRtMu.Lock()
	defer scanRtMu.Unlock()
	if scanRt == nil {
		scanRt = &ScanRuntime{}
		scanRt.reloadResult()
	}
	serverURL, apiKey, _ := scanRt.GetConfig(getEnv)
	scanRt.Configured = serverURL != "" && apiKey != ""
	return scanRt
}

// reloadResult 从文件刷新扫描结果。
func (r *ScanRuntime) reloadResult() {
	r.ScanResult, r.LastScan = LoadScanResult()
	r.Busy = IsScanBusy()
}

// ReloadScanResult 重新从文件加载扫描结果。
func (r *ScanRuntime) ReloadScanResult() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadResult()
}

// ClearScanResult 释放主进程中暂存的查重明细；删除操作会按需从结果文件重新加载。
func (r *ScanRuntime) ClearScanResult() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ScanResult = nil
	r.mu.Unlock()
}

// GetScanRuntime 获取全局查重运行时。
func GetScanRuntime() *ScanRuntime {
	scanRtMu.RLock()
	defer scanRtMu.RUnlock()
	return scanRt
}

// GetConfig 实时读取配置（复用 MediaWarp 的 Emby 反代配置 + STRM 映射）。
func (r *ScanRuntime) GetConfig(getEnv func(string, string) string) (serverURL, apiKey, strmPaths string) {
	if getEnv == nil {
		getEnv = func(k, d string) string { return d }
	}
	return getEnv("ENV_MWARP_MEDIASERVER_ADDR", ""),
		getEnv("ENV_MWARP_MEDIASERVER_AUTH", ""),
		getEnv("ENV_STRM_PATHS", "")
}
