package bot

// 频道监控模块（对应 123bot.py 的 get_latest_messages + main() 频道监控主循环）。
// 职责：抓取 t.me/s/{channel} 页面 → 解析消息 → 过滤匹配 → 转存（分享/秒传）→ 记录数据库。

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"mmbot/internal/httpx"
	_ "modernc.org/sqlite"
)

// ChannelMessage 频道中的一条消息。
type ChannelMessage struct {
	MessageID   string
	DateStr     string
	MessageURL  string
	TargetURL   string
	MessageText string
	RawHTML     string // 消息 div 的完整 HTML（用于提取 href 中的 123 分享链接，对应 Python 的 str(msg)）
}

// MessageDB 频道消息记录数据库（对应 TG_monitor-123.db 的 messages 表）。
type MessageDB struct {
	dbPath string
	db     *sql.DB
}

var reExcludePwd = regexp.MustCompile(`(?i)提取码\s*[:：]\s*(\w+)`)

// NewMessageDB 打开/创建消息记录数据库。
func NewMessageDB(dbPath string) *MessageDB {
	if dbPath == "" {
		// 与 mem_history.json / user_states.db 一致，放在挂载的 data/ 目录以持久化去重记录
		dbPath = filepath.Join("data", "TG_monitor-123.db")
	}
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	m := &MessageDB{dbPath: dbPath}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Printf("[监控] 打开消息数据库失败: %v", err)
		return m
	}
	m.db = db
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS messages (
		msg_id INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT,
		date TEXT,
		message_url TEXT,
		target_url TEXT,
		transfer_status TEXT,
		transfer_time TEXT,
		transfer_result TEXT)`)
	return m
}

// IsProcessed 检查消息是否已处理（无论转存是否成功）。
func (m *MessageDB) IsProcessed(messageURL string) bool {
	if m == nil || m.db == nil || messageURL == "" {
		return false
	}
	var one int
	err := m.db.QueryRow("SELECT 1 FROM messages WHERE message_url = ?", messageURL).Scan(&one)
	return err == nil
}

// Save 记录/更新消息处理结果。
func (m *MessageDB) Save(messageID, date, messageURL, targetURL, status, result string) {
	if m == nil || m.db == nil || messageURL == "" {
		return
	}
	now := time.Now().Format("2006-01-02T15:04:05")
	// 与 Python 原版一致：纯 INSERT（id 非唯一约束，去重由调用方 IsProcessed 按 message_url 判断）
	_, err := m.db.Exec(`INSERT INTO messages (id, date, message_url, target_url, transfer_status, transfer_time, transfer_result)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		messageID, date, messageURL, targetURL, status, now, result)
	if err != nil {
		log.Printf("[监控] 保存消息记录失败: %v", err)
	}
}

// Cleanup 清理 retention 天前的历史记录（对应 cleanup_db）。
func (m *MessageDB) Cleanup(retentionDays int) {
	if m == nil || m.db == nil {
		return
	}
	if retentionDays <= 0 {
		retentionDays = 30
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format("2006-01-02T15:04:05")
	res, err := m.db.Exec("DELETE FROM messages WHERE transfer_time < ?", cutoff)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("[监控] 已清理 %d 条 %d 天前的消息记录", n, retentionDays)
		}
	}
}

// monitorHTTP 频道抓取共享 HTTP 客户端。
var monitorHTTP = httpx.New(15 * time.Second)

// fetchChannelMessages 抓取单个 t.me/s/ 频道页面并解析消息（对应 get_latest_messages 单频道部分）。
// 返回所有解析到的消息（含已处理过的，由调用方过滤）。
func fetchChannelMessages(channelURL string) []ChannelMessage {
	// 预处理：https://t.me/xxx → https://t.me/s/xxx
	channelURL = strings.TrimSpace(channelURL)
	if strings.HasPrefix(channelURL, "https://t.me/") && !strings.Contains(channelURL, "/s/") {
		name := strings.TrimPrefix(channelURL, "https://t.me/")
		name = strings.Trim(name, "/")
		channelURL = "https://t.me/s/" + name
	}
	body, status, err := monitorHTTP.Get(context.Background(), channelURL, nil)
	if err != nil || status != 200 {
		log.Printf("[监控] 抓取频道失败 %s: status=%d err=%v", channelURL, status, err)
		return nil
	}
	messages := parseTgmeMessages(body)
	log.Printf("[监控] 频道 %s 解析到 %d 条消息（最新的在最后）", channelURL, len(messages))
	return messages
}

// parseTgmeMessages 解析 t.me/s/ 页面的消息块（div.tgme_widget_message）。
func parseTgmeMessages(body []byte) []ChannelMessage {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		log.Printf("[监控] 解析频道 HTML 失败: %v", err)
		return nil
	}
	var out []ChannelMessage
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "tgme_widget_message") {
			if cm := parseTgmeMessage(n); cm != nil {
				out = append(out, *cm)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// parseTgmeMessage 解析单个消息 div。
func parseTgmeMessage(div *html.Node) *ChannelMessage {
	cm := &ChannelMessage{}
	// data-post 作为消息 ID
	for _, a := range div.Attr {
		if a.Key == "data-post" {
			parts := strings.Split(a.Val, "/")
			cm.MessageID = parts[len(parts)-1]
		}
	}
	// 渲染消息 div 完整 HTML，用于提取 href 中的 123 分享链接（对应 Python 的 str(msg)）
	var buf strings.Builder
	if err := html.Render(&buf, div); err == nil {
		cm.RawHTML = buf.String()
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch {
		case n.Data == "time":
			for _, a := range n.Attr {
				if a.Key == "datetime" {
					cm.DateStr = a.Val
				}
			}
		case n.Data == "a" && hasClass(n, "tgme_widget_message_date"):
			for _, a := range n.Attr {
				if a.Key == "href" {
					cm.MessageURL = strings.TrimPrefix(a.Val, "/")
				}
			}
		case n.Data == "div" && hasClass(n, "tgme_widget_message_text"):
			cm.MessageText = nodeText(n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(div)

	if cm.MessageID == "" && cm.MessageURL == "" && cm.MessageText == "" {
		return nil
	}
	return cm
}

// nodeText 提取节点纯文本（\n 分隔，对应 bs4 get_text(separator='\n', strip=True)）。
func nodeText(n *html.Node) string {
	var sb strings.Builder
	var rec func(n *html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return strings.TrimSpace(sb.String())
}

func hasClass(n *html.Node, cls string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == cls {
					return true
				}
			}
		}
	}
	return false
}

// 二次过滤规则：ENV_SECOND_FILTER 形如 "DV:1,DOLBY VISION:2" → 关键词:文件夹ID
type secondFilterRule struct {
	keyword string
	folder  int
}

// parseSecondFilters 解析二次过滤规则。
func parseSecondFilters(raw string) []secondFilterRule {
	var rules []secondFilterRule
	for _, rule := range strings.Split(raw, ",") {
		if idx := strings.Index(rule, ":"); idx > 0 {
			kw := strings.TrimSpace(rule[:idx])
			id := 0
			fmt.Sscanf(strings.TrimSpace(rule[idx+1:]), "%d", &id)
			if kw != "" && id > 0 {
				rules = append(rules, secondFilterRule{keyword: kw, folder: id})
			}
		}
	}
	return rules
}

// matchSecondFilter 二次过滤：命中规则返回目标文件夹 ID，否则返回 defaultID。
func matchSecondFilter(text string, rules []secondFilterRule, defaultID int) int {
	for _, r := range rules {
		if strings.Contains(text, r.keyword) {
			log.Printf("[监控] 消息匹配二次过滤关键词 '%s'，将转存到文件夹ID: %d", r.keyword, r.folder)
			return r.folder
		}
	}
	return defaultID
}

// checkChannel 单次频道检查：抓取全部配置频道并处理未处理消息。
// 返回处理的消息数。
func (b *Bot) checkChannel() int {
	if b.env.GetInt("ENV_AUTHORIZATION", 0) == 0 {
		return 0
	}
	channelRaw := b.env.Get("ENV_TG_CHANNEL", "")
	channelURLs := strings.Split(channelRaw, "|")
	if len(channelURLs) == 0 || (len(channelURLs) == 1 && strings.TrimSpace(channelURLs[0]) == "") {
		log.Printf("[监控] 未设置 ENV_TG_CHANNEL 环境变量")
		return 0
	}
	// 123 客户端未就绪时，转存内部会按需 initClient，这里直接检查授权开关即可
	excludeFilter := b.env.Get("ENV_EXCLUDE_FILTER", "")
	secondRules := parseSecondFilters(b.env.Get("ENV_SECOND_FILTER", ""))
	defaultPID := b.env.GetInt("ENV_123_UPLOAD_PID", 0)
	msgDB := NewMessageDB("")
	if msgDB.db != nil {
		defer msgDB.db.Close()
	}

	processed := 0
	for _, channelURL := range channelURLs {
		channelURL = strings.TrimSpace(channelURL)
		if channelURL == "" {
			continue
		}
		log.Printf("[监控] ===== 处理频道: %s =====", channelURL)
		messages := fetchChannelMessages(channelURL)
		for _, cm := range messages {
			// 消息可能含多个 123 链接，逐一处理；提取码自动附加
			// 用消息 div 完整 HTML 提取（Python 原版 extract_target_url(str(msg))，链接在 href 属性中）
			targetURLs := extractTargetURL(cm.RawHTML)
			for _, url := range targetURLs {
				// 提取码：消息文本中有"提取码:xxx"且 URL 无 pwd 参数则附加
				if pm := reExcludePwd.FindStringSubmatch(cm.MessageText); pm != nil && !strings.Contains(url, "pwd=") {
					if strings.Contains(url, "?") {
						url = url + "&pwd=" + pm[1]
					} else {
						url = url + "?pwd=" + pm[1]
					}
					log.Printf("[监控] 已为URL添加提取码: %s", url)
				}
				if msgDB.IsProcessed(cm.MessageURL) {
					log.Printf("[监控] 消息已处理，跳过")
					continue
				}
				cm.TargetURL = url
				b.processChannelMessage(cm, msgDB, excludeFilter, secondRules, defaultPID)
				processed++
			}
			// 无 123 链接但消息未处理时也尝试秒传链接
			if len(targetURLs) == 0 {
				if msgDB.IsProcessed(cm.MessageURL) {
					continue
				}
				b.processChannelMessage(cm, msgDB, excludeFilter, secondRules, defaultPID)
				processed++
			}
		}
		time.Sleep(1 * time.Second) // 频道间限速
	}
	return processed
}

// processChannelMessage 处理单条频道消息：过滤 → 二次过滤 → 转存 → 记录。
func (b *Bot) processChannelMessage(cm ChannelMessage, msgDB *MessageDB, excludeFilter string, secondRules []secondFilterRule, defaultPID int) {
	// 过滤条件匹配（消息标题/链接命中任一过滤词）
	if !b.filterMatch(cm.TargetURL, cm.MessageText) {
		b.recordChannelResult(cm, msgDB, "未转存", fmt.Sprintf("未匹配过滤条件（%s），跳过转存", b.Filter()))
		time.Sleep(1 * time.Second)
		return
	}
	// 排除关键词
	if excludeFilter != "" && (strings.Contains(cm.TargetURL, excludeFilter) || strings.Contains(cm.MessageText, excludeFilter)) {
		b.recordChannelResult(cm, msgDB, "未转存", fmt.Sprintf("包含排除关键词（%s），跳过转存", excludeFilter))
		time.Sleep(1 * time.Second)
		return
	}

	log.Printf("[监控] 消息匹配过滤条件（%s），开始转存...", b.Filter())
	transferID := matchSecondFilter(cm.MessageText, secondRules, defaultPID)

	if cm.TargetURL != "" {
		if b.transferSharedLinkOptimize(cm.TargetURL, transferID) {
			title := b.matchedFilterTitle(cm.TargetURL, cm.MessageText)
			msg := fmt.Sprintf("📡 监控到『<a href=\"%s\">%s</a>』已转存✅", cm.MessageURL, htmlEscape(title))
			b.SubmitSend(func() { b.SendMessage(msg) })
			b.triggerTransferAfterSave(transferID)
			b.recordChannelResult(cm, msgDB, "转存成功", msg)
		} else {
			msg := fmt.Sprintf("❌123云盘转存失败\n消息内容: %s\n链接: %s", cm.MessageURL, cm.TargetURL)
			b.SubmitSend(func() { b.SendMessage(msg) })
		b.recordChannelResult(cm, msgDB, "转存失败", msg)
	}
	// 与秒传分支对齐：每条分享链接转存后间隔 10 秒，避免触发 123pan 账号级限流
	time.Sleep(10 * time.Second)
	return
}

	// 无分享链接 → 尝试秒传链接
	fullLinks := extract123LinksFromFullText(cm.MessageText)
	if len(fullLinks) > 0 {
		ok := false
		for _, link := range fullLinks {
			if b.saveShareLinkLink(link, transferID) {
				ok = true
			}
		}
		if ok {
			msg := "✅123云盘秒传链接转存成功\n消息内容: " + cm.MessageURL + "\n"
			b.SubmitSend(func() { b.SendMessage(msg) })
			b.triggerTransferAfterSave(transferID)
			b.recordChannelResult(cm, msgDB, "转存成功", msg)
		} else {
			msg := "❌123云盘秒传链接转存失败\n消息内容: " + cm.MessageURL + "\n"
			b.recordChannelResult(cm, msgDB, "转存失败", msg)
		}
	} else {
		b.recordChannelResult(cm, msgDB, "转存失败", "❌123云盘秒传链接转存失败\n消息内容: "+cm.MessageURL)
	}
	time.Sleep(10 * time.Second)
}

// saveShareLinkLink 转存单条秒传链接，返回是否成功。
func (b *Bot) saveShareLinkLink(link string, targetPID int) bool {
	entries := parseShareLink(link)
	if len(entries) == 0 {
		return false
	}
	files := make([]FileItem, 0, len(entries))
	usesV2 := false
	for _, e := range entries {
		files = append(files, FileItem{Path: e.Path, Etag: e.Etag, Size: e.Size})
		if e.IsV2Etag {
			usesV2 = true
		}
	}
	s, _ := b.saveFilesTo123(nil, "", files, len(files), 0, usesV2, false, targetPID, nil, nil, "秒传")
	return s > 0
}

// recordChannelResult 记录频道消息处理结果。
func (b *Bot) recordChannelResult(cm ChannelMessage, msgDB *MessageDB, status, result string) {
	msgDB.Save(cm.MessageID, cm.DateStr, cm.MessageURL, cm.TargetURL, status, result)
	log.Printf("[监控] 已记录: %s | %s | 状态: %s", cm.MessageID, cm.TargetURL, status)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;",
	)
	return r.Replace(s)
}

// StartMonitor 启动频道监控主循环（对应 main() 的频道检查循环）。
// checkInterval 单位：分钟（ENV_CHECK_INTERVAL）。
func (b *Bot) StartMonitor() {
	interval := b.env.GetInt("ENV_CHECK_INTERVAL", 0)
	log.Printf("[监控] 123转存目标目录ID: %d | 检查间隔: %d分钟", b.env.GetInt("ENV_123_UPLOAD_PID", 0), interval)
	b.monitorMu.Lock()
	b.monitorActive = true
	b.monitorNext = time.Time{}
	b.monitorMu.Unlock()
	go func() {
		lastCleanup := time.Now()
		for {
			select {
			case <-b.ctx.Done():
				b.monitorMu.Lock()
				b.monitorActive = false
				b.monitorMu.Unlock()
				return
			default:
			}
			b.monitorScanMu.Lock()
			b.checkChannel()
			b.monitorScanMu.Unlock()
			b.monitorMu.Lock()
			b.monitorLast = time.Now()
			if interval > 0 {
				b.monitorNext = b.monitorLast.Add(time.Duration(interval) * time.Minute)
			} else {
				b.monitorNext = time.Time{}
			}
			b.monitorMu.Unlock()
			// 每日清理数据库记录
			if time.Since(lastCleanup) >= 24*time.Hour {
				NewMessageDB("").Cleanup(b.env.GetInt("ENV_DB_RETENTION_DAYS", 30))
				lastCleanup = time.Now()
			}
			log.Printf("[监控] 休息%d分钟...", interval)
			select {
			case <-b.ctx.Done():
				b.monitorMu.Lock()
				b.monitorActive = false
				b.monitorMu.Unlock()
				return
			case <-time.After(time.Duration(interval) * time.Minute):
			}
		}
	}()
}

// MonitorStatus 返回频道监控运行状态（是否激活、上次/下次检查时间，零值表示无）。
func (b *Bot) MonitorStatus() (active bool, last, next time.Time) {
	b.monitorMu.Lock()
	defer b.monitorMu.Unlock()
	return b.monitorActive, b.monitorLast, b.monitorNext
}

// TriggerCheck 手动触发一次频道检查；与定时扫描共用 monitorScanMu 互斥，
// 已有扫描在进行时返回 started=false，避免并发扫描重复转存。
func (b *Bot) TriggerCheck() (started bool, processed int) {
	if !b.monitorScanMu.TryLock() {
		return false, 0
	}
	defer b.monitorScanMu.Unlock()
	return true, b.checkChannel()
}
