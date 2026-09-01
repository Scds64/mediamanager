package bot

// 消息发送辅助（对应 123bot.py 的 send_message/send_reply/send_reply_delete 及线程池）。
// SubmitSend 将所有发送动作放入独立 goroutine，避免阻塞消息循环（对应 reply_thread_pool.submit）。

import (
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// SubmitSend 异步执行发送动作（带 panic 恢复，对应 reply_thread_pool.submit）。
func (b *Bot) SubmitSend(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Bot] 发送消息异常: %v", r)
			}
		}()
		fn()
	}()
}

// SendMessage 向管理员发送文本（自动 HTML 解析）。
func (b *Bot) SendMessage(text string) {
	if b.tg == nil {
		return
	}
	parseMode := ""
	if containsHTML(text) {
		parseMode = "HTML"
	}
	cfg := tgbotapi.NewMessage(b.adminID, text)
	cfg.ParseMode = parseMode
	if _, err := b.tg.Send(cfg); err != nil {
		log.Printf("[Bot] 发送消息失败: %v", err)
	}
}

// SendMessageWithID 向指定 chat 发送文本。
func (b *Bot) SendMessageWithID(chatID int64, text string) {
	if b.tg == nil {
		return
	}
	parseMode := ""
	if containsHTML(text) {
		parseMode = "HTML"
	}
	cfg := tgbotapi.NewMessage(chatID, text)
	cfg.ParseMode = parseMode
	if _, err := b.tg.Send(cfg); err != nil {
		log.Printf("[Bot] 发送消息(%d)失败: %v", chatID, err)
	}
}

// SendReply 回复指定消息。
func (b *Bot) SendReply(msg *tgbotapi.Message, text string) {
	if b.tg == nil || msg == nil {
		return
	}
	cfg := tgbotapi.NewMessage(msg.Chat.ID, text)
	cfg.ReplyToMessageID = msg.MessageID
	if containsHTML(text) {
		cfg.ParseMode = "HTML"
	}
	if _, err := b.tg.Send(cfg); err != nil {
		log.Printf("[Bot] 回复失败: %v", err)
	}
}

// SendReplyDelete 回复消息，删除延迟后自动删除（带 10 秒节流，对应 send_reply_delete）。
func (b *Bot) SendReplyDelete(msg *tgbotapi.Message, text string, deleteDelay int) {
	if b.tg == nil || msg == nil {
		return
	}
	b.lastSendMu.Lock()
	if time.Since(b.lastSend) < 10*time.Second {
		b.lastSendMu.Unlock()
		return
	}
	b.lastSend = time.Now()
	b.lastSendMu.Unlock()

	// 限制文本长度，保留开头和末尾的 200 字符
	const maxLength = 400
	if len(text) > maxLength {
		text = text[:200] + "\n     ......\n" + text[len(text)-200:]
	}

	cfg := tgbotapi.NewMessage(msg.Chat.ID, text)
	cfg.ReplyToMessageID = msg.MessageID
	if containsHTML(text) {
		cfg.ParseMode = "HTML"
	}
	sent, err := b.tg.Send(cfg)
	if err != nil {
		log.Printf("[Bot] 发送回复失败: %v", err)
		return
	}
	if deleteDelay > 0 {
		time.Sleep(time.Duration(deleteDelay) * time.Second)
		del := tgbotapi.NewDeleteMessage(sent.Chat.ID, sent.MessageID)
		if _, err := b.tg.Request(del); err != nil {
			log.Printf("[Bot] 删除消息失败: %v", err)
		}
	}
}

// SendPhoto 发送图片（URL 直传）并附带 caption。
func (b *Bot) SendPhoto(chatID int64, photoURL, caption string) {
	if b.tg == nil {
		return
	}
	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileURL(photoURL))
	photo.Caption = caption
	if containsHTML(caption) {
		photo.ParseMode = "HTML"
	}
	if _, err := b.tg.Send(photo); err != nil {
		log.Printf("[Bot] 发送图片失败: %v", err)
	}
}

// SendDocument 发送文档（JSON 秒传文件等）。
func (b *Bot) SendDocument(chatID int64, fileName string, data []byte, caption string) {
	if b.tg == nil {
		return
	}
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: fileName, Bytes: data})
	doc.Caption = caption
	if containsHTML(caption) {
		doc.ParseMode = "HTML"
	}
	if _, err := b.tg.Send(doc); err != nil {
		log.Printf("[Bot] 发送文档失败: %v", err)
	}
}

// SendSharePhoto 发送图片到分享频道（若配置了分享 bot）。
func (b *Bot) SendSharePhoto(photoURL, caption string) {
	if b.shareBot == nil || b.shareChatID == 0 {
		return
	}
	photo := tgbotapi.NewPhoto(b.shareChatID, tgbotapi.FileURL(photoURL))
	photo.Caption = caption
	if containsHTML(caption) {
		photo.ParseMode = "HTML"
	}
	if _, err := b.shareBot.Send(photo); err != nil {
		log.Printf("[Bot] 发送分享频道图片失败: %v", err)
	}
}

// SendShareDocument 发送文档到分享频道（若配置了分享 bot）。
func (b *Bot) SendShareDocument(fileName string, data []byte, caption string) {
	if b.shareBot == nil || b.shareChatID == 0 {
		return
	}
	doc := tgbotapi.NewDocument(b.shareChatID, tgbotapi.FileBytes{Name: fileName, Bytes: data})
	doc.Caption = caption
	if containsHTML(caption) {
		doc.ParseMode = "HTML"
	}
	if _, err := b.shareBot.Send(doc); err != nil {
		log.Printf("[Bot] 发送分享频道文档失败: %v", err)
	}
}

// containsHTML 判断文本是否需要 HTML 解析（与 Python 一致：含 <a 链接标签时）。
func containsHTML(text string) bool {
	return strings.Contains(text, "<a ") || strings.Contains(text, "<a href")
}
