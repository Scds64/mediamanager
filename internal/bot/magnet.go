package bot

// 磁力链接处理（对应 123bot.py 的 add_magnet_links）。

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// MagnetResult 磁力添加结果。
type MagnetResult struct {
	Status  string // success / error
	Message string
	Data    []MagnetItem
}

// MagnetItem 单个磁力提交结果。
type MagnetItem struct {
	Link     string
	Response map[string]any
}

// addMagnetLinks 识别文本中的多个磁力链接并添加到离线下载。
func (b *Bot) addMagnetLinks(msg *tgbotapi.Message, text string, uploadDir string) *MagnetResult {
	magnetLinks := extractMagnetLinks(text)
	if len(magnetLinks) == 0 {
		return &MagnetResult{Status: "error", Message: "未找到磁力链接"}
	}
	log.Printf("找到磁力链接: %v", magnetLinks)
	b.SubmitSend(func() {
		b.SendReply(msg, fmt.Sprintf("找到%d条磁力链\n%v\n正在添加，请耐心等待", len(magnetLinks), magnetLinks))
	})

	client := b.initClient()
	addedCount := 0
	responses := make([]MagnetItem, 0, len(magnetLinks))
	for _, link := range magnetLinks {
		resp, err := client.SubmitMagnet(context.Background(), link, uploadDir)
		if err != nil {
			log.Printf("添加磁力链接失败 %s: %v", link, err)
			responses = append(responses, MagnetItem{Link: link, Response: map[string]any{
				"code":    -1,
				"message": err.Error(),
			}})
			continue
		}
		responses = append(responses, MagnetItem{Link: link, Response: resp})
		addedCount++
		time.Sleep(500 * time.Millisecond)
	}
	if len(responses) > 0 {
		return &MagnetResult{Status: "success", Data: responses}
	}
	return &MagnetResult{Status: "error", Message: "添加磁力链接失败"}
}

// magnetOK 判断单个磁力响应是否成功（code==0）。
func magnetOK(resp map[string]any) bool {
	if resp == nil {
		return false
	}
	code, _ := resp["code"].(float64)
	return code == 0
}

// magnetMessage 取响应 message 字段。
func magnetMessage(resp map[string]any) string {
	if resp == nil {
		return "未知错误"
	}
	if m, ok := resp["message"].(string); ok {
		return m
	}
	if m, ok := resp["message"].(float64); ok {
		return fmt.Sprintf("%v", m)
	}
	return strings.TrimSpace(strings.Trim(fmt.Sprintf("%v", resp["message"]), "{}"))
}
