package transfer

// 刮削模块（对应 scraper.py）。
// 生成 NFO XML + 下载海报/扇图，上传到 123 云盘目标目录。

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"

	"mmbot/internal/pan123"
)

// Scraper NFO + 图片刮削器。
type Scraper struct {
	tmdb     *TmdbClient
	client   *pan123.Client
	language string
}

// NewScraper 创建刮削器。
func NewScraper(tmdb *TmdbClient, client *pan123.Client, language string) *Scraper {
	if language == "" {
		language = "zh-CN"
	}
	return &Scraper{tmdb: tmdb, client: client, language: language}
}

// Scrape 对一个媒体生成 NFO + 图片并上传到 123 云盘 targetPID 目录。
func (s *Scraper) Scrape(ctx context.Context, media *MediaInfo, meta MetaInfo, targetPID int, fileNameStem string) bool {
	if media == nil || media.TMDBID == 0 {
		log.Printf("无 TMDB 信息，跳过刮削")
		return false
	}
	nfoName := fileNameStem + ".nfo"
	if fileNameStem == "" {
		nfoName = media.Title + ".nfo"
	}
	if media.Type == "tv" {
		nfoName = "tvshow.nfo"
	}

	// 1. 生成并上传 NFO
	if err := s.uploadText(ctx, s.buildNFO(media), nfoName, targetPID); err != nil {
		log.Printf("NFO 生成/上传失败（不影响整理）: %v", err)
	} else {
		log.Printf("NFO 已上传: %s", nfoName)
	}

	// 2. 下载并上传海报
	if s.tmdb != nil {
		if media.PosterPath != "" {
			if ok := s.uploadImage(ctx, media.PosterPath, "w500", targetPID, "poster.jpg"); ok {
				log.Printf("海报已上传: poster.jpg")
			}
		}
		if media.BackdropPath != "" {
			if ok := s.uploadImage(ctx, media.BackdropPath, "original", targetPID, "fanart.jpg"); ok {
				log.Printf("背景图已上传: fanart.jpg")
			}
		}
	}
	return true
}

// uploadImage 下载 TMDB 图片并上传。
func (s *Scraper) uploadImage(ctx context.Context, imagePath, size string, targetPID int, fileName string) bool {
	tmp, err := os.CreateTemp("", "123bot_scrape_*.jpg")
	if err != nil {
		log.Printf("创建临时文件失败: %v", err)
		return false
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	if !s.tmdb.DownloadImage(imagePath, tmpPath, size) {
		return false
	}
	if err := s.client.UploadFile(ctx, tmpPath, targetPID, fileName, 1); err != nil {
		log.Printf("%s 上传失败: %v", fileName, err)
		return false
	}
	return true
}

// uploadText 上传文本内容到 123 云盘。
func (s *Scraper) uploadText(ctx context.Context, text, fileName string, targetPID int) error {
	tmp, err := os.CreateTemp("", "123bot_nfo_*.nfo")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()
	defer os.Remove(tmpPath)
	return s.client.UploadFile(ctx, tmpPath, targetPID, fileName, 1)
}

// buildNFO 生成 NFO XML 字符串。
func (s *Scraper) buildNFO(media *MediaInfo) string {
	if media.Type == "tv" {
		return s.buildTVNFO(media)
	}
	return s.buildMovieNFO(media)
}

func (s *Scraper) buildMovieNFO(media *MediaInfo) string {
	genres := make([]string, 0)
	for _, g := range media.GenreIDs {
		genres = append(genres, escapeXML(strconv.Itoa(g)))
	}
	countries := make([]string, 0)
	for _, c := range media.ProductionCountries {
		countries = append(countries, escapeXML(c))
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
    <title>` + escapeXML(media.Title) + `</title>
    <originaltitle>` + escapeXML(media.OriginalTitle) + `</originaltitle>
    <year>` + intOrEmpty(media.Year) + `</year>
    <tmdbid>` + intOrEmpty(media.TMDBID) + `</tmdbid>
    <imdbid>` + escapeXML(media.IMDBID) + `</imdbid>
    <plot>` + escapeXML(media.Overview) + `</plot>
    <runtime>` + intOrEmpty(media.Runtime) + `</runtime>
    <rating>` + floatOrEmpty(media.VoteAverage) + `</rating>
    <genre>` + strings.Join(genres, "</genre><genre>") + `</genre>
    <country>` + strings.Join(countries, "</country><country>") + `</country>
</movie>`
}

func (s *Scraper) buildTVNFO(media *MediaInfo) string {
	genres := make([]string, 0)
	for _, g := range media.GenreIDs {
		genres = append(genres, escapeXML(strconv.Itoa(g)))
	}
	countries := make([]string, 0)
	for _, c := range media.OriginCountry {
		countries = append(countries, escapeXML(c))
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<tvshow>
    <title>` + escapeXML(media.Title) + `</title>
    <originaltitle>` + escapeXML(media.OriginalTitle) + `</originaltitle>
    <year>` + intOrEmpty(media.Year) + `</year>
    <tmdbid>` + intOrEmpty(media.TMDBID) + `</tmdbid>
    <plot>` + escapeXML(media.Overview) + `</plot>
    <runtime>` + intOrEmpty(media.Runtime) + `</runtime>
    <rating>` + floatOrEmpty(media.VoteAverage) + `</rating>
    <episode>` + intOrEmpty(media.NumberOfEpisodes) + `</episode>
    <season>` + intOrEmpty(media.NumberOfSeasons) + `</season>
    <genre>` + strings.Join(genres, "</genre><genre>") + `</genre>
    <country>` + strings.Join(countries, "</country><country>") + `</country>
</tvshow>`
}

// escapeXML 转义 XML 特殊字符。
func escapeXML(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

func intOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func floatOrEmpty(f float64) string {
	if f == 0 {
		return ""
	}
	return strconv.FormatFloat(f, 'f', 1, 64)
}
