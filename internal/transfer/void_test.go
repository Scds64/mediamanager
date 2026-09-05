package transfer

import (
	"database/sql"
	"testing"
)

// 同批次内多个版本链式覆盖（A 被 B 覆盖、B 被 C 覆盖）：
// success 与分组 FileCount 最终只算存活的 1 个文件，而不是 3 个。
func TestVoidOldVersionMovieChain(t *testing.T) {
	media := &MediaInfo{Title: "抓特务", Year: 2026, Type: "movie", TMDBID: 1305672}
	meta := &MetaInfo{Name: "抓特务", Type: "movie", TMDBID: 1305672}

	stats := &TransferStats{Groups: map[groupKey]*GroupSummary{}, Moved: map[string]bool{}}

	type v struct {
		id   string
		size int64
	}
	files := []v{{"A", 17868748155}, {"B", 21267104122}, {"C", 30465381435}}

	for i, f := range files {
		if i > 0 {
			// 后一个文件去重判定覆盖：删掉前一个刚入库的文件
			old := &HistoryRecord{FileID: files[i-1].id, FileSize: files[i-1].size}
			if !stats.Moved[old.FileID] {
				t.Fatalf("file %s 应被标记为本次已入库", old.FileID)
			}
			delete(stats.Moved, old.FileID)
			(*TransferExecutor)(nil).voidOldVersion(stats, old, media, meta)
		}
		stats.Moved[f.id] = true
		stats.Success++
		accumulateToGroups(stats.Groups, TransferResult{Success: true, FileID: f.id, Media: media, Meta: meta, FileSize: f.size})
	}

	if stats.Success != 1 {
		t.Fatalf("Success = %d, want 1", stats.Success)
	}
	g := stats.Groups[getGroupKey(media, meta)]
	if g.FileCount != 1 {
		t.Fatalf("FileCount = %d, want 1", g.FileCount)
	}
	if g.TotalSize != files[2].size {
		t.Fatalf("TotalSize = %d, want %d（只剩 C）", g.TotalSize, files[2].size)
	}
}

// 电视剧：同批次内同一集被覆盖时，撤销该集计数，EpisodeSizes/Episodes 同步清理。
func TestVoidOldVersionTVEpisode(t *testing.T) {
	media := &MediaInfo{Title: "某剧", Type: "tv", TMDBID: 7}
	metaA := &MetaInfo{Name: "某剧", Type: "tv", Season: 1, Episode: 3}
	metaB := &MetaInfo{Name: "某剧", Type: "tv", Season: 1, Episode: 3}

	stats := &TransferStats{Groups: map[groupKey]*GroupSummary{}, Moved: map[string]bool{"old": true, "new": true}}
	stats.Success++
	accumulateToGroups(stats.Groups, TransferResult{Success: true, FileID: "old", Media: media, Meta: metaA, FileSize: 1000})

	old := &HistoryRecord{FileID: "old", FileSize: 1000, Episode: sql.NullInt64{Valid: true, Int64: 3}}
	delete(stats.Moved, "old")
	(*TransferExecutor)(nil).voidOldVersion(stats, old, media, metaA)
	stats.Success++
	accumulateToGroups(stats.Groups, TransferResult{Success: true, FileID: "new", Media: media, Meta: metaB, FileSize: 2000})

	if stats.Success != 1 {
		t.Fatalf("Success = %d, want 1", stats.Success)
	}
	g := stats.Groups[getGroupKey(media, metaA)]
	if g.FileCount != 1 {
		t.Fatalf("FileCount = %d, want 1", g.FileCount)
	}
	if g.TotalSize != 2000 {
		t.Fatalf("TotalSize = %d, want 2000", g.TotalSize)
	}
	if len(g.EpisodeSizes) != 1 || g.EpisodeSizes[3] != 2000 {
		t.Fatalf("EpisodeSizes = %v, want {3:2000}", g.EpisodeSizes)
	}
	if len(g.Episodes) != 1 || g.Episodes[0] != 3 {
		t.Fatalf("Episodes = %v, want [3]", g.Episodes)
	}
}
