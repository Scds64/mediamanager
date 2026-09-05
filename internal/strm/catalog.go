// STRM 梳理入册：把本地已有 STRM 对应的网盘文件 + orphan STRM 写入整理历史表，
// 让后续新资源整理时能通过 FindSameEpisode/Movie 去重命中旧 STRM。
package strm

import (
	"context"
	"crypto/md5"
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mmbot/internal/pan123"
	"mmbot/internal/transfer"
)

// CatalogResult catalog 执行结果。
type CatalogResult struct {
	Mappings int `json:"mappings"`
	Matched  int `json:"matched"`
	Orphan   int `json:"orphan"`
	Skipped  int `json:"skipped"`
	Error    string `json:"error,omitempty"`
}

// CatalogStrmToHistory 遍历所有 STRM 映射，匹配网盘文件 + 补齐 orphan，写入 transfer_history。
// 幂等：History.Add 是 INSERT OR REPLACE，重复 catalog 只会覆盖。
func CatalogStrmToHistory(ctx context.Context, client *pan123.Client, strmPaths string, history *transfer.TransferHistory, concurrency int) CatalogResult {
	result := CatalogResult{}

	if history == nil {
		result.Error = "history 未初始化"
		return result
	}
	if client == nil {
		result.Error = "123 client 未初始化"
		return result
	}
	if concurrency < 1 {
		concurrency = 1
	}

	mappings := ParseMappings(strmPaths)
	if len(mappings) == 0 {
		result.Error = "未配置 STRM 映射（ENV_STRM_PATHS）"
		return result
	}

	result.Mappings = len(mappings)
	log.Printf("【STRM 梳理】开始，共 %d 个映射，并发 %d", len(mappings), concurrency)

	for mi, mapping := range mappings {
		localDir, panDir := mapping[0], mapping[1]
		log.Printf("【STRM 梳理】映射 %d/%d: %s ↔ %s", mi+1, len(mappings), localDir, panDir)

		// 1. 拿网盘根目录 ID
		panRootID := GetDirIDByPath(ctx, client, panDir)
		if panRootID == 0 {
			log.Printf("【STRM 梳理】网盘目录定位失败，跳过该映射: %s", panDir)
			result.Skipped++
			continue
		}

		// 2. 遍历网盘，建三层索引
		fullIndex, nameSizeIndex, idPathIndex, panTotal := buildPanIndex(ctx, client, panRootID, panDir, concurrency)
		log.Printf("【STRM 梳理】网盘索引构建完成：%d 个文件", panTotal)

		// 3. 遍历本地 STRM，逐个匹配
		localDir = strings.TrimRight(strings.ReplaceAll(localDir, "\\", "/"), "/")
		roots := []string{localDir}
		IterStrmFiles(roots, func(strmPath string) {
			relPath := strings.TrimPrefix(strings.ReplaceAll(strmPath, "\\", "/"), localDir+"/")
			if relPath == "" {
				return
			}

			info := ReadURL(strmPath)
			meta := ParseMeta(relPath)

			// Recognize 解析 parentDirs（从顶层到直接父目录）
			parentDirs := splitPath(filepath.Dir(relPath))
			filename := filepath.Base(relPath)
			transferMeta := transfer.Recognize(filename, "", parentDirs)

			// 匹配网盘 FileInfo
			panFullPath := strings.TrimRight(panDir, "/") + "/" + relPath
			matched, matchedPath := matchPanFile(panFullPath, info.Name, info.Size, fullIndex, nameSizeIndex, idPathIndex)

			// 覆盖保护：如果 file_id 已经存在且不是 catalog 类型（比如 move 正常整理的），跳过
			if matched != nil {
				existing := history.GetByFileID(strconv.FormatInt(matched.FileID, 10))
				if existing != nil && !strings.HasPrefix(existing.TransferType, "catalog") {
					result.Skipped++
					return
				}
			}

			// 写 history
			rec := buildHistoryRecord(meta, transferMeta, matchedPath, strmPath, matched)
			if err := history.Add(rec); err != nil {
				log.Printf("【STRM 梳理】写入 history 失败: %v", err)
				return
			}

			if matched != nil {
				result.Matched++
			} else {
				result.Orphan++
			}
		})
	}

	log.Printf("【STRM 梳理】完成：映射 %d 匹配 %d orphan %d 跳过 %d",
		result.Mappings, result.Matched, result.Orphan, result.Skipped)
	return result
}

// ---------- 网盘索引构建 ----------

func buildPanIndex(ctx context.Context, client *pan123.Client, rootID int64, panRoot string, concurrency int) (
	fullIndex map[string]*pan123.FileInfo,
	nameSizeIndex map[string][]*pan123.FileInfo,
	idPathIndex map[int64]string,
	total int,
) {
	fullIndex = map[string]*pan123.FileInfo{}
	nameSizeIndex = map[string][]*pan123.FileInfo{}
	idPathIndex = map[int64]string{}

	items, err := client.IterDirConcurrent(ctx, rootID, -1, true, time.Second, concurrency)
	if err != nil {
		log.Printf("【STRM 梳理】遍历网盘目录失败: %v", err)
		return
	}

	for item := range items {
		if item.IsDir || item.Raw == nil {
			continue
		}
		fullPath := strings.TrimRight(panRoot, "/") + "/" + item.RelPath
		fullIndex[fullPath] = item.Raw
		nameSizeKey := item.Raw.FileName + "\x00" + strconv.FormatInt(item.Raw.Size, 10)
		nameSizeIndex[nameSizeKey] = append(nameSizeIndex[nameSizeKey], item.Raw)
		idPathIndex[item.Raw.FileID] = fullPath
		total++
	}
	return
}

// ---------- 匹配逻辑 ----------

var catalogMediaExts = []string{".mp4", ".mkv", ".ts", ".iso", ".rmvb", ".avi", ".mov", ".mpeg", ".mpg", ".wmv", ".3gp", ".asf", ".m4v", ".flv", ".m2ts", ".tp", ".f4v"}

// matchPanFile 三层匹配：强匹配（完整路径+扩展名遍历）→ 弱匹配+目录前缀校验 → nil (orphan)
// 返回匹配到的 FileInfo 和它在网盘上的真实完整路径。
func matchPanFile(panFullPath, fileName string, size int64,
	fullIndex map[string]*pan123.FileInfo,
	nameSizeIndex map[string][]*pan123.FileInfo,
	idPathIndex map[int64]string,
) (*pan123.FileInfo, string) {
	// 层 1: 强匹配 —— 把 panFullPath 的 .strm 后缀换成各种视频扩展名，在 fullIndex 里查
	stem := strings.TrimSuffix(panFullPath, filepath.Ext(panFullPath))
	for _, ext := range catalogMediaExts {
		if fi, ok := fullIndex[stem+ext]; ok {
			return fi, stem + ext
		}
	}

	// 层 2: 弱匹配 —— (fileName, size) 查候选，再校验候选父目录是否匹配
	if fileName == "" || size == 0 {
		return nil, ""
	}
	expectedDir := filepath.Dir(stem)

	// 候选 key 变体：带扩展名 vs 不带扩展名
	candidateKeys := []string{
		fileName + "\x00" + strconv.FormatInt(size, 10),
		strings.TrimSuffix(fileName, filepath.Ext(fileName)) + "\x00" + strconv.FormatInt(size, 10),
	}

	for _, key := range candidateKeys {
		candidates, ok := nameSizeIndex[key]
		if !ok {
			continue
		}
		// 只有 1 个候选，直接返回
		if len(candidates) == 1 {
			fullPath, _ := idPathIndex[candidates[0].FileID]
			return candidates[0], fullPath
		}
		// 多个候选：用 idPathIndex 反查真实路径，校验父目录
		for _, c := range candidates {
			if cFull, ok := idPathIndex[c.FileID]; ok {
				if filepath.Dir(cFull) == expectedDir {
					return c, cFull
				}
			}
		}
		// 都不匹配，退而求其次返回第一个
		fullPath, _ := idPathIndex[candidates[0].FileID]
		return candidates[0], fullPath
	}
	return nil, ""
}

// ---------- 构造 history 记录 ----------

func buildHistoryRecord(strmMeta StrmMeta, transferMeta transfer.MetaInfo, matchedPath, localStrmPath string, matched *pan123.FileInfo) transfer.HistoryRecord {
	rec := transfer.HistoryRecord{}

	if matched != nil {
		rec.FileID = strconv.FormatInt(matched.FileID, 10)
		rec.FileName = matched.FileName
		// target_path 必须是匹配上的真实网盘文件路径，不是 STRM 路径
		if matchedPath != "" {
			rec.TargetPath = matchedPath
		} else {
			rec.TargetPath = localStrmPath
		}
		rec.FileSize = matched.Size
	} else {
		// orphan: 用 md5(本地STRM路径) 作为唯一标识
		sum := md5.Sum([]byte(localStrmPath))
		rec.FileID = "orphan_" + fmt.Sprintf("%x", sum)
		rec.FileName = strmMeta.Title
		rec.TargetPath = localStrmPath
	}

	// 媒体信息：全部 ParseMeta 优先（从完整相对路径解析，专门为 STRM 设计）
	// Recognize 只用来拿 TMDBID（它有 tmdbIDRe 正则）
	rec.MediaTitle = strmMeta.Title

	if strmMeta.Year > 0 {
		rec.MediaYear = strconv.Itoa(strmMeta.Year)
	}

	mtype := firstNonEmpty(strmMeta.Kind, "movie")
	rec.MediaType = mtype

	if transferMeta.TMDBID > 0 {
		rec.TMDBID = sql.NullInt64{Int64: int64(transferMeta.TMDBID), Valid: true}
	}
	if strmMeta.Season > 0 {
		rec.Season = sql.NullInt64{Int64: int64(strmMeta.Season), Valid: true}
	}
	if strmMeta.Episode > 0 {
		rec.Episode = sql.NullInt64{Int64: int64(strmMeta.Episode), Valid: true}
	}

	rec.Status = "success"
	if matched != nil {
		rec.TransferType = "catalog"
	} else {
		rec.TransferType = "catalog_orphan"
	}
	return rec
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
