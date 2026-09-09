package strm

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"mmbot/internal/pan123"
	"mmbot/internal/transfer"
)

// TestCatalogMatchFlow 模拟完整的 catalog 匹配流程（无真实 123 client）。
// 验证三层匹配 + Recognize 提取 + history record 构建。
func TestCatalogMatchFlow(t *testing.T) {
	// 1. 建临时本地 STRM 目录结构
	tmpDir, _ := os.MkdirTemp("", "strm-test-*")
	defer os.RemoveAll(tmpDir)

	// 场景 A: 有 tmdb_id 的标准路径
	dirA := filepath.Join(tmpDir, "瑞奇宝宝 (2015) [tmdb=78579]", "Season 1")
	os.MkdirAll(dirA, 0o755)
	strmA := filepath.Join(dirA, "瑞奇宝宝 第10集.strm")
	os.WriteFile(strmA, []byte("https://example.com/?name=瑞奇宝宝 第10集.mp4&size=8589934592&md5=abc123"), 0o644)

	// 场景 B: 纯数字文件名，无 tmdb_id
	dirB := filepath.Join(tmpDir, "庆余年 (2019)", "Season 1")
	os.MkdirAll(dirB, 0o755)
	strmB := filepath.Join(dirB, "10.strm")
	os.WriteFile(strmB, []byte("https://example.com/?name=庆余年.10.mkv&size=4294967296&md5=def456"), 0o644)

	// 场景 C: orphan STRM（网盘里没有）
	dirC := filepath.Join(tmpDir, "已删除剧", "Season 1")
	os.MkdirAll(dirC, 0o755)
	strmC := filepath.Join(dirC, "消失的那集.strm")
	os.WriteFile(strmC, []byte("https://example.com/?name=消失的那集.mp4&size=1000000000&md5=zzz"), 0o644)

	// 2. 模拟网盘索引（只有 A 和 B 存在，C 不存在）
	panRoot := "/网盘根/电视剧"
	fiA := &pan123.FileInfo{FileID: 111, FileName: "瑞奇宝宝 第10集.mp4", Size: 8589934592, Etag: "abc123"}
	fiB := &pan123.FileInfo{FileID: 222, FileName: "庆余年.10.mkv", Size: 4294967296, Etag: "def456"}

	fullIndex := map[string]*pan123.FileInfo{
		panRoot + "/瑞奇宝宝 (2015) [tmdb=78579]/Season 1/瑞奇宝宝 第10集.mp4": fiA,
		panRoot + "/庆余年 (2019)/Season 1/庆余年.10.mkv":                  fiB,
	}
	nameSizeIndex := map[string][]*pan123.FileInfo{
		"瑞奇宝宝 第10集.mp4\x008589934592": {fiA},
		"庆余年.10.mkv\x004294967296":    {fiB},
	}
	idPathIndex := map[int64]string{
		fiA.FileID: panRoot + "/瑞奇宝宝 (2015) [tmdb=78579]/Season 1/瑞奇宝宝 第10集.mp4",
		fiB.FileID: panRoot + "/庆余年 (2019)/Season 1/庆余年.10.mkv",
	}

	// 3. 逐文件走 catalog 流程
	tests := []struct {
		name            string
		strmPath        string
		expectMatched   bool
		expectTMDBD     int
		expectSeason    int
		expectEpisode   int
		expectMediaType string
	}{
		{
			name: "瑞奇宝宝 第10集 (带 tmdb)", strmPath: strmA,
			expectMatched: true, expectTMDBD: 78579, expectSeason: 1, expectEpisode: 10, expectMediaType: "tv",
		},
		{
			name: "庆余年 10.strm (纯数字文件名)", strmPath: strmB,
			expectMatched: true, expectTMDBD: 0, expectSeason: 1, expectEpisode: 10, expectMediaType: "tv",
		},
		{
			name: "orphan 消失的那集", strmPath: strmC,
			expectMatched: false, expectTMDBD: 0, expectSeason: 1, expectEpisode: 0, expectMediaType: "tv",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			relPath := filepath.ToSlash(tc.strmPath[len(tmpDir)+1:])

			info := ReadURL(tc.strmPath)
			meta := ParseMeta(relPath)

			parentDirs := splitPath(filepath.Dir(relPath))
			filename := filepath.Base(relPath)
			transferMeta := transfer.Recognize(filename, "", parentDirs)

			panFullPath := panRoot + "/" + relPath
			matched, _ := matchPanFile(panFullPath, info.Name, info.Size, fullIndex, nameSizeIndex, idPathIndex)

			if tc.expectMatched && matched == nil {
				t.Fatalf("预期匹配成功但匹配失败: panFullPath=%s fileName=%s size=%d", panFullPath, info.Name, info.Size)
			}
			if !tc.expectMatched && matched != nil {
				t.Fatalf("预期 orphan 但匹配到了: %+v", matched)
			}

			if transferMeta.TMDBID != tc.expectTMDBD {
				t.Errorf("TMDBID: expect=%d got=%d", tc.expectTMDBD, transferMeta.TMDBID)
			}

			// 验证 buildHistoryRecord（包含 ParseMeta fallback 的最终值）
			rec := buildHistoryRecord(meta, transferMeta, panFullPath, tc.strmPath, info.Size, matched)
			if rec.FileSize != info.Size {
				t.Errorf("FileSize: expect STRM size=%d got=%d", info.Size, rec.FileSize)
			}

			// Season/Episode 检查最终 rec 里的值
			var gotSeason, gotEpisode int
			if rec.Season.Valid {
				gotSeason = int(rec.Season.Int64)
			}
			if rec.Episode.Valid {
				gotEpisode = int(rec.Episode.Int64)
			}
			if gotSeason != tc.expectSeason {
				t.Errorf("Season(final): expect=%d got=%d", tc.expectSeason, gotSeason)
			}
			if gotEpisode != tc.expectEpisode {
				t.Errorf("Episode(final): expect=%d got=%d", tc.expectEpisode, gotEpisode)
			}

			if tc.expectMatched {
				if rec.FileID != strconv.FormatInt(matched.FileID, 10) {
					t.Errorf("FileID: expect=%d got=%s", matched.FileID, rec.FileID)
				}
				if rec.TransferType != "catalog" {
					t.Errorf("TransferType: expect=catalog got=%s", rec.TransferType)
				}
			} else {
				if len(rec.FileID) < 7 || rec.FileID[:7] != "orphan_" {
					t.Errorf("FileID 前缀: expect=orphan_ got=%s", rec.FileID)
				}
				if rec.TransferType != "catalog_orphan" {
					t.Errorf("TransferType: expect=catalog_orphan got=%s", rec.TransferType)
				}
			}
			if rec.Status != "success" {
				t.Errorf("Status: expect=success got=%s", rec.Status)
			}
			if rec.MediaType != tc.expectMediaType {
				t.Errorf("MediaType: expect=%s got=%s", tc.expectMediaType, rec.MediaType)
			}
		})
	}
}

// TestFindSameFallback 验证 FindSameEpisode/Movie 的 tmdb_id→title fallback。
func TestFindSameFallback(t *testing.T) {
	tmpDB := filepath.Join(os.TempDir(), "test_catalog_fallback.db")
	os.Remove(tmpDB)
	defer os.Remove(tmpDB)

	h, err := transfer.NewTransferHistory(tmpDB)
	if err != nil {
		t.Fatalf("NewTransferHistory: %v", err)
	}
	defer h.Close()

	// 写入一条 catalog 记录（tmdb_id=0，靠 title）
	h.Add(transfer.HistoryRecord{
		FileID: "111", MediaTitle: "瑞奇宝宝", MediaYear: "2015",
		MediaType: "tv",
		Season:    sql.NullInt64{Int64: 1, Valid: true},
		Episode:   sql.NullInt64{Int64: 10, Valid: true},
		FileSize:  8589934592, Status: "success", TransferType: "catalog",
	})

	// 场景 1: tmdb_id=78579 查不到（catalog 里是 0），但 title fallback 命中
	results := h.FindSameEpisode(78579, 1, 10, "瑞奇宝宝")
	if len(results) != 1 {
		t.Fatalf("FindSameEpisode fallback: expect 1 got %d", len(results))
	}
	if results[0].FileID != "111" {
		t.Errorf("FileID: expect=111 got=%s", results[0].FileID)
	}

	// 场景 2: tmdb_id=0 + title 直接命中
	results2 := h.FindSameEpisode(0, 1, 10, "瑞奇宝宝")
	if len(results2) != 1 {
		t.Fatalf("FindSameEpisode direct title: expect 1 got %d", len(results2))
	}

	// 场景 3: 什么都不匹配
	results3 := h.FindSameEpisode(99999, 1, 10, "不存在")
	if len(results3) != 0 {
		t.Errorf("FindSameEpisode miss: expect 0 got %d", len(results3))
	}

	// Test FindSameMovie
	h.Add(transfer.HistoryRecord{
		FileID: "222", MediaTitle: "星际穿越", MediaYear: "2014",
		MediaType: "movie", FileSize: 1500000000, Status: "success", TransferType: "catalog",
	})
	movieResults := h.FindSameMovie(99999, "星际穿越", "2014")
	if len(movieResults) != 1 {
		t.Fatalf("FindSameMovie fallback: expect 1 got %d", len(movieResults))
	}
}

func TestCatalogTimerSchedule(t *testing.T) {
	r := &StrmRuntime{
		Enabled:        true,
		CatalogEnabled: true,
		CatalogCron:    "* * * * *",
	}
	r.rescheduleNow()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nextCatalog.IsZero() {
		t.Fatal("nextCatalog 应该被计算出来但为零值")
	}
	diff := time.Until(r.nextCatalog)
	if diff < 0 || diff > time.Minute {
		t.Errorf("nextCatalog 应该在 1 分钟内，实际差 %v", diff)
	}
	t.Logf("nextCatalog: %v (diff: %v)", r.nextCatalog, diff)
}

func TestCatalogTimerDisabledNoSchedule(t *testing.T) {
	r := &StrmRuntime{
		Enabled:        true,
		CatalogEnabled: false,
		CatalogCron:    "* * * * *",
	}
	r.rescheduleNow()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.nextCatalog.IsZero() {
		t.Errorf("CatalogEnabled=false 时 nextCatalog 应该为零值，实际: %v", r.nextCatalog)
	}
}

func TestCatalogTimerBadCron(t *testing.T) {
	r := &StrmRuntime{
		Enabled:        true,
		CatalogEnabled: true,
		CatalogCron:    "bad cron",
	}
	r.rescheduleNow()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.nextCatalog.IsZero() {
		t.Errorf("cron 无效时 nextCatalog 应该为零值，实际: %v", r.nextCatalog)
	}
}
