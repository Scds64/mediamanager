package strm

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mmbot/internal/pan123"
	"mmbot/internal/transfer"
)

func TestCatalogE2E(t *testing.T) {
	panRoot := "/media"

	// 构造完整的网盘文件列表（模拟真实场景）
	type panFile struct {
		relPath string
		fileID  int64
		name    string
		size    int64
	}
	panFiles := []panFile{
		// 电影 - 目录结构正确
		{"电影/动画电影/冰雪奇缘 (2013) {tmdbid=109445}/冰雪奇缘.Frozen.2013.BluRay.2160p.H265.TrueHD.mkv", 1111, "冰雪奇缘.Frozen.2013.BluRay.2160p.H265.TrueHD.mkv", 800_000_000},
		{"电影/外语电影/星球大战4：新希望 (1977) {tmdbid=11}/星球大战4：新希望.Star Wars.1977.UHD BluRay HDR10.mkv", 2222, "星球大战4：新希望.Star Wars.1977.UHD BluRay HDR10.mkv", 1_200_000_000},
		// 电视剧 - Season 子目录，文件名带中文集号
		{"电视剧/庆余年 (2019) [tmdb=62947]/Season 1/庆余年 第10集.mkv", 3333, "庆余年 第10集.mkv", 500_000_000},
		// 电视剧 - 纯数字文件名
		{"电视剧/庆余年 (2019) [tmdb=62947]/Season 1/11.mkv", 3334, "11.mkv", 510_000_000},
		// 电视剧 - 海贼王 SC（中间有脏目录）
		{"电视剧/海贼王 (1999) [tmdb=16377]/海贼王 SC/Season 1/01.mkv", 4444, "01.mkv", 400_000_000},
		// 电视剧 - 长安十二时辰 文件名带脏信息
		{"电视剧/长安十二时辰 (2019) [tmdb=90768]/Season 1/长安十二时辰 The Longest Day In Chang'an E01 V2 HDCTV.mkv", 5555, "长安十二时辰 The Longest Day In Chang'an E01 V2 HDCTV.mkv", 600_000_000},
	}

	// 构造本地 STRM 列表（相对 localDir）
	type strmFile struct {
		relPath             string
		name                string // URL 里解析出来的 name
		size                int64  // URL 里解析出来的 size
		expectTitle         string
		expectType          string
		expectYear          int
		expectS             int
		expectE             int
		expectTMDB          int
		expectMatched       bool
		expectTargetPathExt string // 期望 target_path 的扩展名
	}
	strmFiles := []strmFile{
		{
			relPath: "电影/动画电影/冰雪奇缘 (2013) {tmdbid=109445}/冰雪奇缘.Frozen.2013.BluRay.2160p.H265.TrueHD.strm",
			name:    "冰雪奇缘.Frozen.2013.BluRay.2160p.H265.TrueHD", size: 800_000_000,
			expectTitle: "冰雪奇缘", expectType: "movie", expectYear: 2013,
			expectTMDB: 109445, expectMatched: true, expectTargetPathExt: ".mkv",
		},
		{
			relPath: "电影/外语电影/星球大战4：新希望 (1977) {tmdbid=11}/星球大战4：新希望.Star Wars.1977.UHD BluRay HDR10.strm",
			name:    "星球大战4：新希望.Star Wars.1977.UHD BluRay HDR10", size: 1_200_000_000,
			expectTitle: "星球大战4：新希望", expectType: "movie", expectYear: 1977,
			expectTMDB: 11, expectMatched: true, expectTargetPathExt: ".mkv",
		},
		{
			relPath: "电视剧/庆余年 (2019) [tmdb=62947]/Season 1/庆余年 第10集.strm",
			name:    "庆余年 第10集", size: 500_000_000,
			expectTitle: "庆余年", expectType: "tv", expectYear: 2019,
			expectS: 1, expectE: 10, expectTMDB: 62947, expectMatched: true, expectTargetPathExt: ".mkv",
		},
		{
			relPath: "电视剧/庆余年 (2019) [tmdb=62947]/Season 1/11.strm",
			name:    "11", size: 510_000_000,
			expectTitle: "庆余年", expectType: "tv", expectYear: 2019,
			expectS: 1, expectE: 11, expectTMDB: 62947, expectMatched: true, expectTargetPathExt: ".mkv",
		},
		{
			relPath: "电视剧/海贼王 (1999) [tmdb=16377]/海贼王 SC/Season 1/01.strm",
			name:    "01", size: 400_000_000,
			expectTitle: "海贼王", expectType: "tv", expectYear: 1999,
			expectS: 1, expectE: 1, expectTMDB: 16377, expectMatched: true, expectTargetPathExt: ".mkv",
		},
		{
			relPath: "电视剧/长安十二时辰 (2019) [tmdb=90768]/Season 1/长安十二时辰 The Longest Day In Chang'an E01 V2 HDCTV.strm",
			name:    "长安十二时辰 The Longest Day In Chang'an E01 V2 HDCTV", size: 600_000_000,
			expectTitle: "长安十二时辰", expectType: "tv", expectYear: 2019,
			expectS: 1, expectE: 1, expectTMDB: 90768, expectMatched: true, expectTargetPathExt: ".mkv",
		},
		{
			relPath: "电视剧/已删除剧 (2020)/Season 1/第01集.strm",
			name:    "第01集", size: 300_000_000,
			expectTitle: "已删除剧", expectType: "tv", expectYear: 2020,
			expectS: 1, expectE: 1, expectMatched: false,
		},
	}

	// 建索引
	fullIndex := map[string]*pan123.FileInfo{}
	nameSizeIndex := map[string][]*pan123.FileInfo{}
	idPathIndex := map[int64]string{}
	for _, f := range panFiles {
		fi := &pan123.FileInfo{
			FileID:   f.fileID,
			FileName: f.name,
			Size:     f.size,
		}
		fullPath := strings.TrimRight(panRoot, "/") + "/" + f.relPath
		fullIndex[fullPath] = fi
		nameSizeKey := f.name + "\x00" + strconv.FormatInt(f.size, 10)
		nameSizeIndex[nameSizeKey] = append(nameSizeIndex[nameSizeKey], fi)
		idPathIndex[f.fileID] = fullPath
	}

	for _, tc := range strmFiles {
		t.Run(tc.relPath, func(t *testing.T) {
			meta := ParseMeta(tc.relPath)
			parentDirs := splitPath(filepath.Dir(tc.relPath))
			filename := filepath.Base(tc.relPath)
			transferMeta := transfer.Recognize(filename, "", parentDirs)

			panFullPath := strings.TrimRight(panRoot, "/") + "/" + tc.relPath
			matched, matchedPath := matchPanFile(panFullPath, tc.name, tc.size, fullIndex, nameSizeIndex, idPathIndex)

			// 1. 验证标题干净
			if meta.Title != tc.expectTitle {
				t.Errorf("Title: expect=%q got=%q", tc.expectTitle, meta.Title)
			}
			// 标题不应该带脏词
			dirtyWords := []string{"strm", "MA", "HD", "HDR", "4K", "1080", "HEVC", "DTS", "Dolby", "BluRay", "REMUX"}
			for _, dw := range dirtyWords {
				if strings.Contains(strings.ToLower(meta.Title), strings.ToLower(dw)) {
					t.Errorf("Title contains dirty word %q: %q", dw, meta.Title)
				}
			}

			// 2. 验证 Kind/Year
			if meta.Kind != tc.expectType {
				t.Errorf("Kind: expect=%q got=%q", tc.expectType, meta.Kind)
			}
			if tc.expectYear > 0 && meta.Year != tc.expectYear {
				t.Errorf("Year: expect=%d got=%d", tc.expectYear, meta.Year)
			}

			// 3. 验证 Season/Episode
			if tc.expectS > 0 && meta.Season != tc.expectS {
				t.Errorf("Season: expect=%d got=%d", tc.expectS, meta.Season)
			}
			if tc.expectE > 0 && meta.Episode != tc.expectE {
				t.Errorf("Episode: expect=%d got=%d", tc.expectE, meta.Episode)
			}

			// 4. 验证 TMDBID 提取
			if tc.expectTMDB > 0 && transferMeta.TMDBID != tc.expectTMDB {
				t.Errorf("TMDBID: expect=%d got=%d", tc.expectTMDB, transferMeta.TMDBID)
			}

			// 5. 验证匹配结果
			if tc.expectMatched {
				if matched == nil {
					t.Fatalf("期望匹配成功但匹配失败")
				}
				// 6. 验证 matchedPath 是 .mkv 真实文件
				if matchedPath == "" {
					t.Errorf("matchedPath 为空")
				} else if !strings.HasSuffix(matchedPath, tc.expectTargetPathExt) {
					t.Errorf("matchedPath 扩展名不对: %s (期望 %s)", matchedPath, tc.expectTargetPathExt)
				} else if !strings.Contains(matchedPath, ".strm") {
					// 确认不是 strm 路径
				} else {
					t.Errorf("matchedPath 错误地指向 .strm 文件: %s", matchedPath)
				}
			} else {
				if matched != nil {
					t.Errorf("期望 orphan 但匹配成功了: fileID=%d", matched.FileID)
				}
			}
		})
	}
}
