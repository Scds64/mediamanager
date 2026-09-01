// 媒体信息文件下载器（对应 mediainfo_downloader.py）。
package strm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mmbot/internal/httpx"
	"mmbot/internal/pan123"
)

const (
	mediaInfoUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// MediaInfoDownloader 媒体信息文件下载器。
type MediaInfoDownloader struct {
	client *pan123.Client
	http   *httpx.Client
}

// NewMediaInfoDownloader 创建下载器。
func NewMediaInfoDownloader(client *pan123.Client) *MediaInfoDownloader {
	return &MediaInfoDownloader{
		client: client,
		http:   httpx.New(30 * time.Second),
	}
}

// DownloadItem 下载项。
type DownloadItem struct {
	Payload   map[string]any
	LocalPath string
}

// isFileLeq1k 判断文件是否小于 1KB。
func isFileLeq1k(filePath string) bool {
	info, err := os.Stat(filePath)
	if err != nil {
		return true
	}
	return info.Size() <= 1024
}

// getDownloadURL 获取下载链接。
func (d *MediaInfoDownloader) getDownloadURL(ctx context.Context, item map[string]any) (string, error) {
	payload := map[string]any{}
	for k, v := range item {
		payload[k] = v
	}
	resp, err := d.client.DownloadInfoWithPayload(ctx, payload, map[string]string{"User-Agent": mediaInfoUA})
	if err != nil {
		return "", err
	}
	if !resp.IsSuccess() {
		return "", fmt.Errorf("123 返回错误 code=%d msg=%s", resp.Code, resp.MessageString())
	}
	var data struct {
		DownloadURL string `json:"DownloadUrl"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return "", err
	}
	return data.DownloadURL, nil
}

// saveMediaInfoFile 保存媒体信息文件（流式写盘，避免整文件读入内存）。
func (d *MediaInfoDownloader) saveMediaInfoFile(filePath, downloadURL string) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	resp, err := d.http.Req(context.Background(), http.MethodGet, downloadURL, map[string]string{"User-Agent": mediaInfoUA}, nil)
	if err != nil {
		return fmt.Errorf("下载文件失败: %v", err)
	}
	defer resp.Body.Close()
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(filePath)
		return fmt.Errorf("写入文件失败: %v", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	log.Printf("【媒体信息文件下载】保存文件成功: %s", filePath)
	return nil
}

// downloader 下载单个文件。
func (d *MediaInfoDownloader) downloader(ctx context.Context, item map[string]any, path string) error {
	downloadURL, err := d.getDownloadURL(ctx, item)
	if err != nil {
		return fmt.Errorf("获取下载链接失败: %v", err)
	}
	if downloadURL == "" {
		return fmt.Errorf("下载链接为空")
	}
	return d.saveMediaInfoFile(path, downloadURL)
}

// AutoDownloader 根据列表自动下载（失败重试 3 次，支持并发）。
func (d *MediaInfoDownloader) AutoDownloader(ctx context.Context, downloadsList []DownloadItem, concurrency int) (int, int, []string) {
	if concurrency < 1 {
		concurrency = 1
	}
	successCount := 0
	failCount := 0
	var failList []string
	var mu sync.Mutex

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, di := range downloadsList {
		sem <- struct{}{}
		wg.Add(1)
		go func(di DownloadItem, idx int) {
			defer func() {
				<-sem
				wg.Done()
			}()

			downloadSuccess := false
			for attempt := 0; attempt < 3; attempt++ {
				if err := d.downloader(ctx, di.Payload, di.LocalPath); err != nil {
					log.Printf("【媒体信息文件下载】%s 下载失败（第%d次）: %v", di.LocalPath, attempt+1, err)
					time.Sleep(time.Second)
					continue
				}
				if !isFileLeq1k(di.LocalPath) {
					downloadSuccess = true
					break
				}
				log.Printf("【媒体信息文件下载】%s 下载文件过小，自动重试", di.LocalPath)
				time.Sleep(time.Second)
			}
			if !downloadSuccess {
				mu.Lock()
				failCount++
				failList = append(failList, di.LocalPath)
				mu.Unlock()
			} else {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
			if (idx+1)%50 == 0 {
				log.Printf("【媒体信息文件下载】已处理 %d/%d 个", idx+1, len(downloadsList))
			}
		}(di, i)
	}
	wg.Wait()
	return successCount, failCount, failList
}

// ParseMediaext 解析扩展名列表：支持全角/半角逗号分隔。
func ParseMediaext(s string) []string {
	if s == "" {
		return nil
	}
	normalized := strings.ReplaceAll(s, "，", ",")
	parts := strings.Split(normalized, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			if !strings.HasPrefix(p, ".") {
				p = "." + p
			}
			out = append(out, p)
		}
	}
	return out
}

// HasPrefix 判断路径是否以指定前缀开头（按路径段比较）。
func HasPrefix(fullPath, prefixPath string) bool {
	full := strings.Split(strings.ReplaceAll(fullPath, "\\", "/"), "/")
	prefix := strings.Split(strings.ReplaceAll(prefixPath, "\\", "/"), "/")
	full = filterEmpty(full)
	prefix = filterEmpty(prefix)
	if len(prefix) > len(full) {
		return false
	}
	for i, p := range prefix {
		if full[i] != p {
			return false
		}
	}
	return true
}

func filterEmpty(parts []string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}