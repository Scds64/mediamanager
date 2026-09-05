// STRM 302 跳转服务（对应 redirect.py）。
package strm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"mmbot/internal/pan123"
)

// 302 缓存：key=(name,size,md5,s3KeyFlag) -> (url, timestamp)
// 有界 LRU 缓存，TTL 2 分钟，最多 1000 条。
type redirectCacheEntry struct {
	url       string
	timestamp time.Time
}

var (
	redirectCache   = map[string]redirectCacheEntry{}
	redirectCacheMu sync.Mutex
	redirectOrder   []string
)

const (
	redirectCacheTTL     = 2 * time.Minute
	redirectCacheMaxSize = 1000
)

func redirectCacheKey(name string, size int64, md5, s3KeyFlag string) string {
	return fmt.Sprintf("%s\x00%d\x00%s\x00%s", name, size, md5, s3KeyFlag)
}

func redirectCacheGet(key string) (string, bool) {
	redirectCacheMu.Lock()
	defer redirectCacheMu.Unlock()
	entry, ok := redirectCache[key]
	if !ok {
		return "", false
	}
	if time.Since(entry.timestamp) > redirectCacheTTL {
		delete(redirectCache, key)
		removeOrder(key)
		return "", false
	}
	moveToEnd(key)
	return entry.url, true
}

func redirectCacheSet(key, url string) {
	redirectCacheMu.Lock()
	defer redirectCacheMu.Unlock()
	redirectCache[key] = redirectCacheEntry{url: url, timestamp: time.Now()}
	moveToEnd(key)
	for len(redirectCache) > redirectCacheMaxSize {
		if len(redirectOrder) > 0 {
			oldest := redirectOrder[0]
			delete(redirectCache, oldest)
			redirectOrder = redirectOrder[1:]
		} else {
			break
		}
	}
}

func moveToEnd(key string) {
	for i, k := range redirectOrder {
		if k == key {
			redirectOrder = append(redirectOrder[:i], redirectOrder[i+1:]...)
			break
		}
	}
	redirectOrder = append(redirectOrder, key)
}

func removeOrder(key string) {
	for i, k := range redirectOrder {
		if k == key {
			redirectOrder = append(redirectOrder[:i], redirectOrder[i+1:]...)
			break
		}
	}
}

// BuildRedirectURL 构造 STRM 文件内容（302 跳转 URL）。
func BuildRedirectURL(serverAddress, apiKey, name string, size int64, etag, s3KeyFlag string) string {
	serverAddress = strings.TrimRight(serverAddress, "/")
	return fmt.Sprintf("%s/api/strm/redirect?apikey=%s&name=%s&size=%d&md5=%s&s3_key_flag=%s",
		serverAddress,
		url.QueryEscape(apiKey),
		url.QueryEscape(name),
		size,
		url.QueryEscape(etag),
		url.QueryEscape(s3KeyFlag),
	)
}

// GetRedirectURL 获取 302 跳转目标地址（严格模式：s3_key_flag 必须非空）。
func GetRedirectURL(ctx context.Context, client *pan123.Client, name string, size int64, md5, s3KeyFlag, userAgent string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("123 客户端未初始化（token 无效或网络故障），无法获取下载链接")
	}
	if s3KeyFlag == "" {
		return "", fmt.Errorf("缺少 s3_key_flag 参数，无法获取下载链接")
	}
	key := redirectCacheKey(name, size, md5, s3KeyFlag)
	if url, ok := redirectCacheGet(key); ok {
		return url, nil
	}

	payload := map[string]any{
		"S3KeyFlag": s3KeyFlag,
		"FileName":  name,
		"Etag":      md5,
		"Size":      size,
		"FileID":    1,
	}
	headers := map[string]string{}
	if userAgent != "" {
		headers["User-Agent"] = userAgent
	}
	resp, err := client.DownloadInfoWithPayload(ctx, payload, headers)
	if err != nil {
		return "", fmt.Errorf("获取 123 下载地址失败: %v", err)
	}
	if !resp.IsSuccess() {
		return "", fmt.Errorf("123 返回错误 code=%d msg=%s", resp.Code, resp.MessageString())
	}
	var data struct {
		DownloadURL string `json:"DownloadUrl"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return "", fmt.Errorf("解析下载地址失败: %v", err)
	}
	if data.DownloadURL == "" {
		return "", fmt.Errorf("123 返回的 DownloadUrl 为空")
	}

	redirectCacheSet(key, data.DownloadURL)
	log.Printf("【302跳转服务】获取 123 下载地址成功: %s", data.DownloadURL)
	return data.DownloadURL, nil
}

// CheckResponse 检查 123 响应。
func CheckResponse(resp *pan123.Response) error {
	if !resp.IsSuccess() {
		return fmt.Errorf("123 API 错误 code=%d msg=%s", resp.Code, resp.MessageString())
	}
	return nil
}