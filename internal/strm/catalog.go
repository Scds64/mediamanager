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
	"sync"
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

		// 3. 收集本地 STRM 路径
		localDir = strings.TrimRight(strings.ReplaceAll(localDir, "\\", "/"), "/")
		roots := []string{localDir}
		var strmPaths []string
		IterStrmFiles(roots, func(p string) { strmPaths = append(strmPaths, p) })
		log.Printf("【STRM 梳理】本地 STRM 文件数：%d", len(strmPaths))

		// 4. worker pool 并发处理 + channel 汇聚 + 批量事务写 DB
		m, o, s := catalogLocalStrms(strmPaths, localDir, panDir, fullIndex, nameSizeIndex, idPathIndex, history, concurrency)
		result.Matched += m
		result.Orphan += o
		result.Skipped += s
	}

	log.Printf("【STRM 梳理】完成：映射 %d 匹配 %d orphan %d 跳过 %d",
		result.Mappings, result.Matched, result.Orphan, result.Skipped)
	return result
}

// catalogLocalStrms 并发处理一批本地 STRM 文件，返回 (matched, orphan, skipped)。
func catalogLocalStrms(strmPaths []string, localDir, panDir string,
	fullIndex map[string]*pan123.FileInfo,
	nameSizeIndex map[string][]*pan123.FileInfo,
	idPathIndex map[int64]string,
	history *transfer.TransferHistory,
	concurrency int,
) (matched, orphan, skipped int) {
	type workItem struct {
		rec     transfer.HistoryRecord
		matched bool
		skipped bool
	}

	// worker pool: 并发处理 ReadURL + ParseMeta + matchPanFile + buildHistoryRecord
	ch := make(chan string, concurrency*2)
	resCh := make(chan workItem, concurrency*2)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for strmPath := range ch {
				relPath := strings.TrimPrefix(strings.ReplaceAll(strmPath, "\\", "/"), localDir+"/")
				if relPath == "" {
					continue
				}

				info := ReadURL(strmPath)
				meta := ParseMeta(relPath)

				parentDirs := splitPath(filepath.Dir(relPath))
				filename := filepath.Base(relPath)
				transferMeta := transfer.Recognize(filename, "", parentDirs)

				panFullPath := strings.TrimRight(panDir, "/") + "/" + relPath
				fmatched, matchedPath := matchPanFile(panFullPath, info.Name, info.Size, fullIndex, nameSizeIndex, idPathIndex)

				// 覆盖保护：如果 file_id 已经存在且不是 catalog* 类型，跳过
				if fmatched != nil {
					existing := history.GetByFileID(strconv.FormatInt(fmatched.FileID, 10))
					if existing != nil && !strings.HasPrefix(existing.TransferType, "catalog") {
						resCh <- workItem{skipped: true}
						continue
					}
				}

				rec := buildHistoryRecord(meta, transferMeta, matchedPath, strmPath, fmatched)
				resCh <- workItem{rec: rec, matched: fmatched != nil}
			}
		}()
	}

	// 投递任务
	go func() {
		for _, p := range strmPaths {
			ch <- p
		}
		close(ch)
	}()

	// 等 worker 全部结束后关 resCh
	go func() {
		wg.Wait()
		close(resCh)
	}()

	// 单 goroutine 批量事务写 DB
	const batchSize = 500
	batch := make([]transfer.HistoryRecord, 0, batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := batchInsertOrReplace(history, batch); err != nil {
			log.Printf("【STRM 梳理】批量写入失败: %v", err)
		}
		batch = batch[:0]
	}
	for item := range resCh {
		if item.skipped {
			skipped++
			continue
		}
		if item.matched {
			matched++
		} else {
			orphan++
		}
		batch = append(batch, item.rec)
		if len(batch) >= batchSize {
			flush()
		}
	}
	flush()
	return
}

// batchInsertOrReplace 批量 INSERT OR REPLACE。
func batchInsertOrReplace(history *transfer.TransferHistory, batch []transfer.HistoryRecord) error {
	if len(batch) == 0 {
		return nil
	}
	// 用事务包裹批量 Exec 比单条快 10x+
	tx, err := history.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO transfer_history
		(file_id, file_name, source_pid, target_pid, target_path,
		 media_title, media_year, media_type, tmdb_id, season, episode,
		 status, error_msg, transfer_type, transfer_time, file_size, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Format(time.RFC3339)
	for _, rec := range batch {
		_, err := stmt.Exec(
			rec.FileID, rec.FileName, rec.SourcePID, rec.TargetPID, rec.TargetPath,
			rec.MediaTitle, rec.MediaYear, rec.MediaType,
			catalogNullInt64(rec.TMDBID), catalogNullInt64(rec.Season), catalogNullInt64(rec.Episode),
			rec.Status, rec.ErrorMsg, rec.TransferType, now,
			rec.FileSize, rec.Version,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
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

func catalogNullInt64(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}
