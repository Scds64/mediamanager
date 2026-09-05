package bot

// 123 分享链接转存（对应 123bot.py 的 transfer_shared_link_optimize）。

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
)

// shareItem 分享资源条目。
type shareItem struct {
	FileID      int64
	Name        string
	Etag        string
	Size        int64
	Type        int
	ParentDirID int64
}

// transferSharedLinkOptimize 转存 123 分享链接到目标目录，成功返回 true。
func (b *Bot) transferSharedLinkOptimize(targetURL string, targetPID int) bool {
	client := b.initClient()
	if client == nil {
		log.Printf("[转存] 123 客户端未初始化，跳过转存: %s", targetURL)
		return false
	}
	shareKey, sharePwd := extractShareKeyAndPwd(targetURL)
	if shareKey == "" {
		b.SubmitSend(func() { b.SendMessage(fmt.Sprintf("无效的分享链接: %s", targetURL)) })
		return false
	}

	ctx := context.Background()
	var allItems []shareItem

	// 递归抓取分享结构
	var recursiveFetch func(parentFileID int64) error
	recursiveFetch = func(parentFileID int64) error {
		page := 1
		for {
			resp, err := client.ShareGet(ctx, shareKey, sharePwd, parentFileID, page)
			if err != nil {
				return err
			}
			list := resp.Data.InfoList
			for _, item := range list {
				allItems = append(allItems, shareItem{
					FileID:      item.FileID,
					Name:        item.FileName,
					Etag:        item.Etag,
					Size:        item.Size,
					Type:        item.Type,
					ParentDirID: parentFileID,
				})
				if item.Type == 1 {
					if err := recursiveFetch(item.FileID); err != nil {
						return err
					}
				}
			}
			if len(list) < 100 {
				break
			}
			page++
		}
		return nil
	}

	if err := recursiveFetch(0); err != nil {
		log.Printf("[转存] 获取资源结构失败: %v", err)
		b.SubmitSend(func() { b.SendMessage(fmt.Sprintf("获取资源结构失败: %v", err)) })
		return false
	}

	var dirs []shareItem
	var files []shareItem
	for _, it := range allItems {
		if it.Type == 1 {
			dirs = append(dirs, it)
		} else {
			files = append(files, it)
		}
	}
	log.Printf("共发现%d个文件和%d个目录，准备转存（顶层目录: %d）", len(files), len(dirs), targetPID)
	for _, d := range dirs {
		log.Printf("目录信息: %+v", d)
	}
	if len(files) == 0 {
		b.SubmitSend(func() { b.SendMessage("分享中没有可转存的文件") })
		return false
	}

	dirIDMap := map[int64]int64{0: int64(targetPID)}

	// 递归创建目录
	var createDirs func(parentDirID int64) error
	createDirs = func(parentDirID int64) error {
		var children []shareItem
		for _, d := range dirs {
			if d.ParentDirID == parentDirID {
				children = append(children, d)
			}
		}
		for _, d := range children {
			newDirID, err := mkdirWithRetry(ctx, client, d.Name, dirIDMap[parentDirID])
			if err != nil {
				log.Printf("创建目录 %s 失败: %v", d.Name, err)
				return err
			}
			dirIDMap[d.FileID] = newDirID
			log.Printf("创建目录: %s -> ID: %d", d.Name, newDirID)
			if err := createDirs(d.FileID); err != nil {
				return err
			}
		}
		return nil
	}

	if err := createDirs(0); err != nil {
		log.Printf("[转存] 创建目录结构失败: %v", err)
		b.SubmitSend(func() { b.SendMessage(fmt.Sprintf("创建目录结构失败: %v", err)) })
		return false
	}

	fileList := make([]map[string]any, 0, len(files))
	for _, item := range files {
		target := dirIDMap[item.ParentDirID]
		if target == 0 {
			target = int64(targetPID)
		}
		fileList = append(fileList, map[string]any{
			"file_id":        item.FileID,
			"file_name":      item.Name,
			"etag":           item.Etag,
			"size":           item.Size,
			"parent_file_id": target,
			"drive_id":       0,
		})
		log.Printf("文件信息: %+v", item)
	}
	log.Printf("准备转存文件列表（顶层目录: %d）", targetPID)

	resp, err := client.ShareCopy(ctx, shareKey, sharePwd, fileList)
	if err != nil {
		msg := fmt.Sprintf("%s 转存失败: %v", targetURL, err)
		log.Printf("%s", msg)
		b.SubmitSend(func() { b.SendMessage(truncateStr(msg, 4000)) })
		return false
	}
	if !resp.IsSuccess() {
		msg := fmt.Sprintf("%s 转存失败: %s", targetURL, resp.MessageString())
		log.Printf("%s", msg)
		b.SubmitSend(func() { b.SendMessage(truncateStr(msg, 4000)) })
		return false
	}
	log.Printf("%s 转存成功", targetURL)
	return true
}

var (
	rePwdSep = regexp.MustCompile(`提取码[:：]`)
	rePwdIn  = regexp.MustCompile(`(?i)提取码\s*[:：]\s*(\w+)`)
)

// extractShareKeyAndPwd 从 123 分享链接解析 share_key 与 share_pwd。
func extractShareKeyAndPwd(targetURL string) (string, string) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return "", ""
	}
	shareKey := ""
	if idx := strings.Index(parsed.Path, "/s/"); idx >= 0 {
		tempKey := strings.Split(parsed.Path[idx+3:], "/")[0]
		if loc := rePwdSep.FindStringIndex(tempKey); loc != nil {
			tempKey = tempKey[:loc[0]]
		}
		shareKey = strings.TrimSpace(tempKey)
	} else if idx := strings.Index(parsed.Path, "/123pan/"); idx >= 0 {
		tempKey := strings.Split(parsed.Path[idx+8:], "/")[0]
		if loc := rePwdSep.FindStringIndex(tempKey); loc != nil {
			tempKey = tempKey[:loc[0]]
		}
		shareKey = strings.TrimSpace(tempKey)
	}
	if shareKey == "" {
		return "", ""
	}

	sharePwd := ""
	if q := parsed.Query(); q.Get("pwd") != "" {
		sharePwd = q.Get("pwd")
	} else if m := rePwdIn.FindStringSubmatch(parsed.Path); m != nil {
		sharePwd = m[1]
	} else if m := rePwdIn.FindStringSubmatch(targetURL); m != nil {
		sharePwd = m[1]
	}
	return shareKey, sharePwd
}
