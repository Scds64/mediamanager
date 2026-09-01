package web

// Emby 观看时长统计（懒加载：点「加载观看数据」/图上某点才调 Emby API）。
//
// 数据来源：Emby 活动日志（/emby/System/ActivityLog/Entries）里的 playback.stop 事件。
// 本服务器经 MediaWarp 反代播放时，UserData 里 LastPlayedDate/PlayCount 为空，
// 无法按日期查询（Filters=IsPlayed + MinDateLastPlayed 会查不到任何记录），
// 但活动日志每条播放停止事件都带精确的 UTC 时间戳与 ItemId，按天聚合可靠。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mmbot/internal/strm"
)

const (
	usageCacheTTL = 60 * time.Second
	usageLogLimit = 500 // 活动日志拉取条数上限（覆盖近 7 天播放事件）
	usageMaxConc  = 4   // Emby 批量拉取并发上限
)

// usageCache Emby 观看数据短缓存（避免刷新页面反复打 Emby）。
var (
	usageCacheMu   sync.Mutex
	usageCacheData any       // 最近一次聚合结果
	usageCacheTime time.Time // 生成时间
	usageCacheDays int
)

type embyUser struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Policy struct {
		IsDisabled bool `json:"IsDisabled"`
	} `json:"Policy"`
}

type embyItem struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	Type         string `json:"Type"`
	RunTimeTicks int64  `json:"RunTimeTicks"`
	SeriesID     string `json:"SeriesId"`
	SeriesName   string `json:"SeriesName"`
	ImageTags    struct {
		Primary string `json:"Primary"`
	} `json:"ImageTags"`
	UserData struct {
		Played           bool    `json:"Played"`
		PlayedPercentage float64 `json:"PlayedPercentage"`
	} `json:"UserData"`
}

type itemsResp struct {
	Items []embyItem `json:"Items"`
}

// actLogEntry Emby 活动日志条目（只需要播放事件相关字段）。
type actLogEntry struct {
	Type   string `json:"Type"`
	Name   string `json:"Name"`
	ItemID string `json:"ItemId"`
	Date   string `json:"Date"`
}

type actLogResp struct {
	Items []actLogEntry `json:"Items"`
}

// usageEvent 一次播放停止事件（用户 GUID + 条目 ID + 事件时间）。
type usageEvent struct {
	userID string
	itemID string
	t      time.Time
}

// usageEmbyClient 按需创建 Emby 客户端（懒加载）；未配置返回 nil。
func usageEmbyClient() *strm.EmbyClient {
	return strm.NewEmbyClient(envGet("ENV_MWARP_MEDIASERVER_ADDR", ""), envGet("ENV_MWARP_MEDIASERVER_AUTH", ""))
}

// parseEmbyTime 解析 Emby 返回的时间（RFC3339 变体），失败返回零值。
func parseEmbyTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// itemSeconds 估算观看秒数：片长(秒) × 播放百分比（Played 视为看完全片）。
func itemSeconds(it embyItem) int {
	if it.RunTimeTicks <= 0 {
		return 0
	}
	sec := float64(it.RunTimeTicks) / 1e7
	pct := it.UserData.PlayedPercentage
	if it.UserData.Played || pct <= 0 || pct > 100 {
		pct = 100
	}
	return int(sec * pct / 100)
}

func fetchEmbyUsers(ctx context.Context, c *strm.EmbyClient) ([]embyUser, error) {
	raw, status, err := c.GetJSON(ctx, "/emby/Users", map[string]string{})
	if err != nil {
		return nil, fmt.Errorf("status=%d: %w", status, err)
	}
	var users []embyUser
	if err := json.Unmarshal(raw, &users); err != nil {
		return nil, fmt.Errorf("解析 Emby 用户列表失败: %w", err)
	}
	out := users[:0]
	for _, u := range users {
		if !u.Policy.IsDisabled {
			out = append(out, u)
		}
	}
	return out, nil
}

// matchUserByName 从活动日志条目 Name 中匹配用户名。活动日志的 UserId 是 Emby 内部
// 数字，无法与 /emby/Users 返回的 GUID 对应，而播放条目 Name 含用户名（如
// "iPhone 上 root 已停止播放 …"），用用户名包含匹配即可。
func matchUserByName(users []embyUser, name string) string {
	for _, u := range users {
		if u.Name != "" && strings.Contains(name, u.Name) {
			return u.ID
		}
	}
	return ""
}

// fetchPlaybackStops 拉取活动日志，解析出播放停止事件并映射到用户 GUID。
func fetchPlaybackStops(ctx context.Context, c *strm.EmbyClient, users []embyUser) ([]usageEvent, error) {
	raw, status, err := c.GetJSON(ctx, "/emby/System/ActivityLog/Entries", map[string]string{
		"Limit": strconv.Itoa(usageLogLimit),
	})
	if err != nil {
		return nil, fmt.Errorf("status=%d: %w", status, err)
	}
	var resp actLogResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("解析 Emby 活动日志失败: %w", err)
	}
	var events []usageEvent
	for _, e := range resp.Items {
		if e.Type != "playback.stop" || e.ItemID == "" {
			continue
		}
		t := parseEmbyTime(e.Date)
		if t.IsZero() {
			continue
		}
		if uid := matchUserByName(users, e.Name); uid != "" {
			events = append(events, usageEvent{userID: uid, itemID: e.ItemID, t: t})
		}
	}
	return events, nil
}

// fetchItemsByIDs 批量拉取指定条目（Emby Ids 逗号分隔），返回按 Id 索引的映射。
func fetchItemsByIDs(ctx context.Context, c *strm.EmbyClient, userID string, ids []string) (map[string]embyItem, error) {
	m := map[string]embyItem{}
	if len(ids) == 0 {
		return m, nil
	}
	raw, status, err := c.GetJSON(ctx, "/emby/Users/"+userID+"/Items", map[string]string{
		"Ids":            strings.Join(ids, ","),
		"Fields":         "UserData,RunTimeTicks,PrimaryImageAspectRatio",
		"EnableUserData": "true",
		"Recursive":      "true",
	})
	if err != nil {
		return nil, fmt.Errorf("status=%d: %w", status, err)
	}
	var resp itemsResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("解析 Emby 条目失败: %w", err)
	}
	for _, it := range resp.Items {
		m[it.ID] = it
	}
	return m, nil
}

// handleEmbyUsage 近 N 天（默认 7）每用户每日观看时长聚合（基于活动日志 playback.stop）。
func (s *Server) handleEmbyUsage(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v >= 1 && v <= 31 {
		days = v
	}
	refresh := r.URL.Query().Get("refresh") == "1"

	c := usageEmbyClient()
	if c == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}

	usageCacheMu.Lock()
	if !refresh && usageCacheData != nil && usageCacheDays == days && time.Since(usageCacheTime) < usageCacheTTL {
		writeJSON(w, http.StatusOK, usageCacheData)
		usageCacheMu.Unlock()
		return
	}
	usageCacheMu.Unlock()

	ctx := r.Context()
	users, err := fetchEmbyUsers(ctx, c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "error": "获取 Emby 用户列表失败: " + err.Error()})
		return
	}

	now := time.Now()
	dayList := make([]string, days)
	start := now.AddDate(0, 0, -(days - 1))
	for i := 0; i < days; i++ {
		dayList[i] = start.AddDate(0, 0, i).Format("2006-01-02")
	}
	winStart := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	winEnd := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.Local)

	events, err := fetchPlaybackStops(ctx, c, users)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "error": "获取 Emby 活动日志失败: " + err.Error()})
		return
	}

	// 窗口过滤 + 收集去重条目
	itemIDs := map[string][]string{} // userID → itemID 列表（去重）
	for _, e := range events {
		lt := e.t.Local()
		if lt.Before(winStart) || lt.After(winEnd) {
			continue
		}
		dup := false
		for _, id := range itemIDs[e.userID] {
			if id == e.itemID {
				dup = true
				break
			}
		}
		if !dup {
			itemIDs[e.userID] = append(itemIDs[e.userID], e.itemID)
		}
	}

	// 每用户并行拉取条目信息，限制并发；单用户失败不影响其它用户
	itemInfo := make(map[string]map[string]embyItem, len(itemIDs))
	sem := make(chan struct{}, usageMaxConc)
	var wg sync.WaitGroup
	for uid, ids := range itemIDs {
		wg.Add(1)
		go func(uid string, ids []string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if m, err := fetchItemsByIDs(ctx, c, uid, ids); err == nil {
				itemInfo[uid] = m
			}
		}(uid, ids)
	}
	wg.Wait()

	// 聚合：同一 (用户, 条目, 日期) 多次停止只计一次
	dur := make([][]int, len(users))
	userIdx := make(map[string]int, len(users))
	dayIdx := make(map[string]int, days)
	for i, u := range users {
		userIdx[u.ID] = i
		dur[i] = make([]int, days)
	}
	for i, d := range dayList {
		dayIdx[d] = i
	}
	seen := make(map[string]bool)
	for _, e := range events {
		lt := e.t.Local()
		if lt.Before(winStart) || lt.After(winEnd) {
			continue
		}
		di, ok := dayIdx[lt.Format("2006-01-02")]
		if !ok {
			continue
		}
		key := e.userID + "|" + e.itemID + "|" + lt.Format("2006-01-02")
		if seen[key] {
			continue
		}
		seen[key] = true
		ui, ok := userIdx[e.userID]
		if !ok {
			continue
		}
		if it, ok := itemInfo[e.userID][e.itemID]; ok {
			dur[ui][di] += itemSeconds(it)
		}
	}

	outUsers := make([]map[string]any, 0, len(users))
	for i, u := range users {
		outUsers = append(outUsers, map[string]any{
			"id":        u.ID,
			"name":      u.Name,
			"durations": dur[i],
		})
	}
	result := map[string]any{
		"configured":   true,
		"days":         dayList,
		"users":        outUsers,
		"generated_at": now.Format(time.RFC3339),
	}
	usageCacheMu.Lock()
	usageCacheData = result
	usageCacheTime = now
	usageCacheDays = days
	usageCacheMu.Unlock()
	writeJSON(w, http.StatusOK, result)
}

// handleEmbyUsageDetail 某用户某一天的观看明细（点图上某点才调）。
func (s *Server) handleEmbyUsageDetail(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	date := r.URL.Query().Get("date")
	if userID == "" || date == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 userId 或 date 参数"})
		return
	}
	dayStart, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "date 格式错误，应为 YYYY-MM-DD"})
		return
	}
	c := usageEmbyClient()
	if c == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	dayEnd := dayStart.Add(24*time.Hour - time.Second)

	ctx := r.Context()
	users, err := fetchEmbyUsers(ctx, c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "error": "获取 Emby 用户列表失败: " + err.Error()})
		return
	}
	events, err := fetchPlaybackStops(ctx, c, users)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "error": "获取 Emby 活动日志失败: " + err.Error()})
		return
	}

	// 该用户当天播放过的条目（去重），并记录最后一次停止时间用于排序
	var ids []string
	lastStop := map[string]time.Time{}
	for _, e := range events {
		lt := e.t.Local()
		if e.userID != userID || lt.Before(dayStart) || lt.After(dayEnd) {
			continue
		}
		if _, ok := lastStop[e.itemID]; !ok {
			ids = append(ids, e.itemID)
		}
		if lt.After(lastStop[e.itemID]) {
			lastStop[e.itemID] = lt
		}
	}

	itemInfo, err := fetchItemsByIDs(ctx, c, userID, ids)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "error": "获取观看记录失败: " + err.Error()})
		return
	}

	items := make([]embyItem, 0, len(ids))
	for _, id := range ids {
		if it, ok := itemInfo[id]; ok {
			items = append(items, it)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return lastStop[items[i].ID].After(lastStop[items[j].ID])
	})

	base := strings.TrimRight(envGet("ENV_MWARP_MEDIASERVER_ADDR", ""), "/")
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		row := map[string]any{
			"id":                it.ID,
			"name":              it.Name,
			"series_name":       it.SeriesName,
			"type":              it.Type,
			"seconds":           itemSeconds(it),
			"played_percentage": it.UserData.PlayedPercentage,
		}
		// 海报：优先条目自身，剧集无 Primary 图时回退到剧集（Series）海报
		picID := it.ID
		if it.ImageTags.Primary == "" && it.SeriesID != "" {
			picID = it.SeriesID
		}
		if picID != "" {
			row["poster"] = base + "/emby/Items/" + picID + "/Images/Primary?maxWidth=300"
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true,
		"date":       date,
		"user_id":    userID,
		"items":      out,
	})
}
