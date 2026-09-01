// Package elevenfive 实现 115 网盘分享链接导出（对应 Python elevenfive.py）。
package elevenfive

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mmbot/internal/httpx"
)

// Client 115 分享客户端。
type Client struct {
	http    *httpx.Client
	Timeout time.Duration
}

// New 创建 115 客户端。
func New() *Client {
	c := httpx.New(30 * time.Second)
	c.SetProxy("") // 115 走直连
	return &Client{http: c, Timeout: 30 * time.Second}
}

var shareRe = regexp.MustCompile(`(?i)https?://(115|115cdn|anxia)\.com/s/(\w+)\?password=(\w+)`)

// ExtractShareInfo 从分享链接提取 share_code 与 receive_code。
func (c *Client) ExtractShareInfo(shareURL string) (string, string, error) {
	m := shareRe.FindStringSubmatch(shareURL)
	if m == nil {
		return "", "", fmt.Errorf("无效的115分享链接: %s", shareURL)
	}
	return m[2], m[3], nil
}

// ShareFile 115 分享中的文件/目录。
type ShareFile struct {
	Name string
	Size int64
	SHA1 string
	MD5  string
	ID   string
	Path string
	CID  string
	Dir  bool
}

// RequestDataList 获取指定目录下的文件/文件夹列表。
func (c *Client) RequestDataList(ctx context.Context, shareCode, receiveCode, cid string) (map[string]any, []map[string]any, error) {
	urls := []string{
		fmt.Sprintf("https://webapi.115.com/share/snap?share_code=%s&offset=0&limit=20&receive_code=%s&cid=%s", shareCode, receiveCode, cid),
		fmt.Sprintf("http://webapi.115.com/share/snap?share_code=%s&offset=0&limit=20&receive_code=%s&cid=%s", shareCode, receiveCode, cid),
	}
	for _, url := range urls {
		shareInfo := map[string]any{}
		var list []map[string]any
		raw, status, err := c.http.Get(ctx, url, nil)
		if err != nil {
			log.Printf("[115] 请求 %s 失败: %v", url, err)
			continue
		}
		if status != 200 {
			log.Printf("[115] 请求 %s HTTP %d", url, status)
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}
		if st, _ := resp["state"].(bool); !st {
			log.Printf("[115] 获取分享信息失败: %v", resp["error"])
			continue
		}
		data, _ := resp["data"].(map[string]any)
		if data == nil {
			continue
		}
		if si, ok := data["shareinfo"].(map[string]any); ok {
			shareInfo = si
		}
		count, _ := data["count"].(float64)
		if l, ok := data["list"].([]any); ok {
			for _, item := range l {
				if m, ok := item.(map[string]any); ok {
					list = append(list, m)
				}
			}
		}
		// 分页拉全
		for int64(len(list)) < int64(count) {
			offset := len(list)
			nextURL := fmt.Sprintf("%s&offset=%d", url, offset)
			raw2, _, err2 := c.http.Get(ctx, nextURL, nil)
			if err2 != nil {
				break
			}
			var resp2 map[string]any
			if err2 := json.Unmarshal(raw2, &resp2); err2 != nil {
				break
			}
			data2, _ := resp2["data"].(map[string]any)
			if l, ok := data2["list"].([]any); ok {
				for _, item := range l {
					if m, ok := item.(map[string]any); ok {
						list = append(list, m)
					}
				}
			}
		}
		log.Printf("[115] 成功获取分享信息 (cid=%s)，共 %d 项", cidOrRoot(cid), len(list))
		return shareInfo, list, nil
	}
	log.Printf("[115] 所有API地址都连接失败")
	return map[string]any{}, nil, nil
}

func cidOrRoot(cid string) string {
	if cid == "" {
		return "根目录"
	}
	return cid
}

// RecursiveGetFiles 递归获取分享中的所有文件信息。
func (c *Client) RecursiveGetFiles(ctx context.Context, shareCode, receiveCode, parentCID, parentPath string) ([]*ShareFile, []*ShareFile) {
	var files, dirs []*ShareFile
	_, dataList, err := c.RequestDataList(ctx, shareCode, receiveCode, parentCID)
	if err != nil || len(dataList) == 0 {
		return files, dirs
	}
	for _, item := range dataList {
		name, _ := item["n"].(string)
		size, _ := item["s"].(float64)
		itemCID, _ := item["cid"].(string)
		itemFID, _ := item["fid"].(string)
		currentPath := name
		if parentPath != "" {
			currentPath = parentPath + "/" + name
		}
		hasFid := itemFID != ""
		hasCid := itemCID != ""
		isdir, _ := item["isdir"].(float64)
		isDir := isdir == 1 || (hasCid && !hasFid)
		if isDir {
			dirs = append(dirs, &ShareFile{Name: name, ID: itemCID, Path: currentPath, Dir: true})
			subFiles, subDirs := c.RecursiveGetFiles(ctx, shareCode, receiveCode, itemCID, currentPath)
			files = append(files, subFiles...)
			dirs = append(dirs, subDirs...)
		} else {
			sha1, _ := item["sha"].(string)
			md5, _ := item["md5"].(string)
			if sha1 == "" {
				log.Printf("[115] 文件 %s 没有SHA1值，跳过", currentPath)
				continue
			}
			id := itemFID
			if id == "" {
				id = itemCID
			}
			files = append(files, &ShareFile{
				Name: name, Size: int64(size), SHA1: sha1, MD5: md5,
				ID: id, Path: currentPath, CID: parentCID,
			})
		}
	}
	return files, dirs
}

// ExportShareInfo 从分享链接导出 SHA1 秒传数据（JSON）。
func (c *Client) ExportShareInfo(shareURL string) map[string]any {
	var jsonData map[string]any
	shareCode, receiveCode, err := c.ExtractShareInfo(shareURL)
	if err != nil {
		log.Printf("[115] 提取分享码失败: %v", err)
		return nil
	}
	files, _ := c.RecursiveGetFiles(context.Background(), shareCode, receiveCode, "", "")
	log.Printf("[115] 已收集 %d 个文件信息", len(files))
	if len(files) == 0 {
		return nil
	}
	jsonData = map[string]any{
		"usesBase62EtagsInExport": false,
		"files":                   []any{},
		"is_sha1":                 true,
	}
	var fileList []any
	for _, f := range files {
		if f.SHA1 == "" {
			continue
		}
		fileList = append(fileList, map[string]any{
			"size": f.Size,
			"path": f.Path,
			"etag": f.SHA1,
			"sha1": f.SHA1,
		})
	}
	if len(fileList) == 0 {
		return nil
	}
	jsonData["files"] = fileList
	log.Printf("[115] 成功生成SHA1秒传数据，共 %d 个文件", len(fileList))
	return jsonData
}

// ToJSON 将导出数据序列化为 JSON 字符串（含路径转义）。
func ToJSON(data map[string]any) (string, error) {
	b, err := json.Marshal(data)
	return string(b), err
}

// StringOrInt 辅助：115 返回的 id/size 可能是字符串或数字。
func StringOrInt(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}
