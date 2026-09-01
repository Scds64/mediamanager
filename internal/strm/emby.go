// Emby/Jellyfin API 客户端（对应 emby_scanner_engine/runtime.py 的 EmbyClient）。
// 用于删除后通知 Emby 清理条目（无需全库扫描）。
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

	"mmbot/internal/httpx"
)

// EmbyClient Emby API 封装，鉴权头 X-Emby-Token。
type EmbyClient struct {
	baseURL string
	apiKey  string
	http    *httpx.Client
}

// 全局 Emby 客户端实例（供通知联动使用）。
var (
	embyRuntimeMu sync.RWMutex
	embyRuntime   *EmbyClient
)

// SetEmbyRuntime 设置全局 Emby 客户端。
func SetEmbyRuntime(c *EmbyClient) {
	embyRuntimeMu.Lock()
	embyRuntime = c
	embyRuntimeMu.Unlock()
}

// GetEmbyRuntime 获取全局 Emby 客户端。
func GetEmbyRuntime() *EmbyClient {
	embyRuntimeMu.RLock()
	defer embyRuntimeMu.RUnlock()
	return embyRuntime
}

// NewEmbyClient 创建 Emby 客户端；未配置（addr/auth 为空）返回 nil。
func NewEmbyClient(addr, apiKey string) *EmbyClient {
	if addr == "" || apiKey == "" {
		return nil
	}
	return &EmbyClient{
		baseURL: strings.TrimSuffix(addr, "/"),
		apiKey:  apiKey,
		http:    httpx.New(8 * time.Second),
	}
}

// NotifyMediaDeleted 通知 Emby/Jellyfin 文件已删除（触发条目移除，无需全库扫描）。
// 调用 POST /emby/Library/Media/Updated。
func (e *EmbyClient) NotifyMediaDeleted(paths []string) bool {
	if e == nil || len(paths) == 0 {
		return false
	}
	updates := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		updates = append(updates, map[string]string{
			"Path":       strings.ReplaceAll(p, "\\", "/"),
			"UpdateType": "Delete",
		})
	}
	body, _ := json.Marshal(map[string]any{"Updates": updates})
	headers := map[string]string{
		"X-Emby-Token": e.apiKey,
		"Content-Type": "application/json",
	}
	_, _, err := e.http.Post(context.Background(), e.baseURL+"/emby/Library/Media/Updated", headers, body)
	if err != nil {
		log.Printf("[strm] 通知 Emby 媒体删除失败: %v", err)
		return false
	}
	// 以请求成功为准（204/200 由客户端内部重试处理）
	return true
}

// PostRaw 通用 POST（供未来扩展）。
func (e *EmbyClient) PostRaw(ctx context.Context, endpoint string, v any) ([]byte, int, error) {
	body, _ := json.Marshal(v)
	headers := map[string]string{
		"X-Emby-Token": e.apiKey,
		"Content-Type": "application/json",
	}
	return e.http.Post(ctx, e.baseURL+endpoint, headers, body)
}

// GetJSON 请求 Emby GET 接口（3 次重试，失败返回错误）。
func (e *EmbyClient) GetJSON(ctx context.Context, endpoint string, params map[string]string) ([]byte, int, error) {
	if e == nil {
		return nil, 0, fmt.Errorf("emby client is nil")
	}
	url := e.baseURL + endpoint
	if len(params) > 0 {
		q := urlValues(params)
		url += "?" + q.Encode()
	}
	headers := map[string]string{"X-Emby-Token": e.apiKey}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		raw, status, err := e.http.Get(ctx, url, headers)
		if err == nil && status < 400 {
			return raw, status, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("HTTP %d", status)
		}
		time.Sleep(time.Duration(2*(attempt+1)) * time.Second)
	}
	return nil, 0, lastErr
}

func urlValues(m map[string]string) *url.Values {
	v := &url.Values{}
	for k, val := range m {
		v.Set(k, val)
	}
	return v
}
