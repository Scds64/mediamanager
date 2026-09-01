package bot

// /delete 命令：按标题搜索本地 STRM 库，选择后联动删除（网盘回收站+本地STRM+Emby+整理历史）。
// 对应 123bot.py 的 handle_delete_command / perform_delete_search / execute_media_delete。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"mmbot/internal/strm"
	"mmbot/internal/transfer"
)

// handleDeleteCommand /delete 关键词：搜索本地 STRM 库，选择后联动删除。
func (b *Bot) handleDeleteCommand(msg *tgbotapi.Message) {
	if !b.isAdmin(msg) {
		return
	}
	parts := strings.SplitN(msg.Text, " ", 2)
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		b.SubmitSend(func() { b.SendReply(msg, "请提供要删除的媒体名称，例如：/delete 武林外传") })
		return
	}
	keyword := strings.TrimSpace(parts[1])
	go b.performDeleteSearch(keyword, msg.From.ID, msg.Chat.ID)
}

// performDeleteSearch 搜索本地 STRM 并展示候选列表，等待用户回复编号删除。
func (b *Bot) performDeleteSearch(keyword string, userID, chatID int64) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Bot] 搜索删除目标失败: %v", r)
			b.SendMessageWithID(chatID, fmt.Sprintf("搜索失败: %v", r))
		}
	}()
	groups := strm.SearchGroups(keyword, b.env.Get("ENV_STRM_PATHS", ""))
	if len(groups) == 0 {
		b.SendMessageWithID(chatID, fmt.Sprintf("未找到标题包含 '%s' 的 STRM 资源", keyword))
		return
	}
	lines := make([]string, 0, len(groups)+2)
	for i, g := range groups {
		icon := "📺"
		if g.Kind == "movie" {
			icon = "🎬"
		}
		gb := float64(g.TotalSize) / (1024 * 1024 * 1024)
		var sizeStr string
		if gb >= 0.1 {
			sizeStr = fmt.Sprintf("%.1fGB", gb)
		} else {
			sizeStr = fmt.Sprintf("%.0fMB", float64(g.TotalSize)/(1024*1024))
		}
		year := ""
		if g.Year > 0 {
			year = fmt.Sprintf("(%d)", g.Year)
		}
		lines = append(lines, fmt.Sprintf("%d. %s %s %s | %d个文件 | %s", i+1, icon, g.Title, year, g.Count, sizeStr))
	}
	if data, err := json.Marshal(groups); err == nil {
		b.states.SetState(userID, "CONFIRM_DELETE", string(data))
	}
	msg := "🔍 找到以下资源，回复编号删除（删除后 Emby 条目不可恢复）：\n" + strings.Join(lines, "\n")
	b.SendMessageWithID(chatID, msg)
}

// handleDeleteConfirm CONFIRM_DELETE 状态：用户回复编号后执行删除。
func (b *Bot) handleDeleteConfirm(msg *tgbotapi.Message, data string) {
	userID := msg.From.ID
	num, err := parseAnyInt(msg.Text)
	if err != nil {
		b.SubmitSend(func() { b.SendReply(msg, "请输入数字序号") })
		return
	}
	var groups []strm.StrmGroup
	if err := json.Unmarshal([]byte(data), &groups); err != nil || len(groups) == 0 {
		b.states.ClearState(userID)
		b.SubmitSend(func() { b.SendReply(msg, "搜索结果已失效，请重新发送 /delete 搜索") })
		return
	}
	if num < 1 || num > len(groups) {
		b.SubmitSend(func() { b.SendReply(msg, fmt.Sprintf("序号超出范围，请输入 1-%d", len(groups))) })
		return
	}
	group := groups[num-1]
	b.states.ClearState(userID)
	go b.executeMediaDelete(&group, msg.Chat.ID)
}

// executeMediaDelete 执行删除：网盘源文件（回收站，如已手动删除则自动跳过）+ 本地STRM + Emby条目 + 整理历史。
func (b *Bot) executeMediaDelete(group *strm.StrmGroup, chatID int64) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Bot] 执行删除失败: %v", r)
			b.SendMessageWithID(chatID, fmt.Sprintf("删除失败: %v", r))
		}
	}()
	log.Printf("开始执行删除: %s (%d)，共 %d 个 STRM", group.Title, group.Year, len(group.Files))
	items := make([]strm.DeleteItem, 0, len(group.Files))
	for _, f := range group.Files {
		items = append(items, strm.DeleteItem{
			ID:   f,
			Name: filepath.Base(f),
			Path: f,
			Size: strm.ReadURL(f).Size,
		})
	}
	if len(items) == 0 {
		b.SendMessageWithID(chatID, "没有可删除的 STRM 文件")
		return
	}
	log.Printf("开始联动删除 %d 个文件（本地STRM+网盘回收站+Emby通知+整理历史）", len(items))
	client := b.initClient()
	emby := strm.NewEmbyClient(b.env.Get("ENV_MWARP_MEDIASERVER_ADDR", ""), b.env.Get("ENV_MWARP_MEDIASERVER_AUTH", ""))
	media := &strm.DeleteMedia{
		Title: group.Title,
		Year:  strconv.Itoa(group.Year),
		Kind:  group.Kind,
	}
	result := strm.DeleteItems(context.Background(), client, items, media, emby, b.env.Get("ENV_STRM_PATHS", ""), b.transferHistory())
	log.Printf("联动删除完成: 成功 %d，失败 %d", result.Success, len(result.FailList))
	msg := fmt.Sprintf("🗑️ 已删除：%s (%d) | %d个文件", group.Title, group.Year, result.Success)
	if len(result.FailList) > 0 {
		var lines []string
		for _, f := range result.FailList {
			lines = append(lines, fmt.Sprintf("%s: %s", f.Name, f.Error))
		}
		msg += "\n⚠️ 失败项:\n" + strings.Join(lines, "\n")
	}
	b.SendMessageWithID(chatID, msg)
}

// transferHistory 获取整理历史管理器（优先复用全局执行器实例）。
func (b *Bot) transferHistory() *transfer.TransferHistory {
	if ex := transfer.GetTransferExecutor(); ex != nil && ex.History != nil {
		return ex.History
	}
	h, err := transfer.NewTransferHistory("data/transfer.db")
	if err != nil {
		log.Printf("[Bot] 初始化整理历史失败: %v", err)
		return nil
	}
	return h
}
