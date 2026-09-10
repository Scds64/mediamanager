package web

// TMDB 榜单/搜索/订阅/详情/集数/日历 API（对应 server.py 的 TMDB 与订阅进度部分）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mmbot/internal/strm"
	"mmbot/internal/transfer"
)

// TMDB 内存缓存：key -> (ts, data)，TTL 24h。
var (
	tmdbCacheMu sync.Mutex
	tmdbCache   = map[string][2]any{}
	webTMDBMu   sync.Mutex
	webTMDBKey  string
	webTMDB     *transfer.TmdbClient
)

const (
	tmdbCacheTTL = 24 * time.Hour
	episodeTTL   = 10 * time.Minute
	calendarTTL  = 24 * time.Hour
)

// episodeCache 集数缓存：key -> (ts, emby, total)
var (
	episodeCacheMu sync.Mutex
	episodeCache   = map[string][3]any{}
)

// calendarCache 日历缓存：key "_" -> (ts, ids, today, shows, dates)
var (
	calendarCacheMu sync.Mutex
	calendarCache   = map[string]any{}
)

func tmdbAPIKey() string { return envGet("ENV_TMDB_API_KEY", "") }

func tmdbClient() *transfer.TmdbClient {
	key := tmdbAPIKey()
	if key == "" {
		return nil
	}
	if executor := transfer.GetTransferExecutor(); executor != nil && executor.TMDB != nil && executor.TMDB.APIKey == key {
		return executor.TMDB
	}
	webTMDBMu.Lock()
	defer webTMDBMu.Unlock()
	if webTMDB != nil && webTMDBKey == key {
		return webTMDB
	}
	c, err := transfer.NewTmdbClient(key, "")
	if err != nil {
		return nil
	}
	webTMDBKey = key
	webTMDB = c
	return webTMDB
}

func embyClient() *strm.EmbyClient {
	return strm.GetEmbyRuntime()
}

// ---------- GET /api/tmdb ----------

func (s *Server) handleTMDBSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	category := q.Get("category")
	if category == "" {
		category = "trending"
	}
	mediaType := q.Get("type")
	if mediaType == "" {
		mediaType = "movie"
	}
	timeWindow := q.Get("time_window")
	if timeWindow == "" {
		timeWindow = "week"
	}
	query := strings.TrimSpace(q.Get("q"))
	if query != "" {
		category = "search"
	}

	validCategory := map[string]bool{"trending": true, "top_rated": true, "now_playing": true, "upcoming": true, "search": true}
	if !validCategory[category] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "不支持的 category，可选 trending/top_rated/now_playing/upcoming"})
		return
	}
	if mediaType != "movie" && mediaType != "tv" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "不支持的 type，可选 movie/tv"})
		return
	}
	if category == "trending" && timeWindow != "day" && timeWindow != "week" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "不支持的 time_window，可选 day/week"})
		return
	}

	if tmdbAPIKey() == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"error":      "未配置 TMDB API Key，请前往「用户配置」填写 ENV_TMDB_API_KEY",
		})
		return
	}

	refresh := q.Get("refresh") == "1"
	var cacheKey string
	if query != "" {
		cacheKey = "search:" + mediaType + ":" + strings.ToLower(query)
	} else {
		cacheKey = category + ":" + mediaType + ":" + timeWindow
	}

	tmdbCacheMu.Lock()
	if !refresh {
		if cached, ok := tmdbCache[cacheKey]; ok {
			if time.Since(cached[0].(time.Time)) < tmdbCacheTTL {
				items := cached[1]
				tmdbCacheMu.Unlock()
				writeJSON(w, http.StatusOK, map[string]any{
					"configured":  true,
					"category":    category,
					"type":        mediaType,
					"time_window": timeWindowIf(category, timeWindow),
					"query":       queryOrNil(query),
					"items":       items,
				})
				return
			}
		}
	}
	tmdbCacheMu.Unlock()

	client := tmdbClient()
	var items []transfer.TmdbListItem
	switch {
	case query != "":
		items = client.SearchMedia(query, mediaType)
	case category == "trending":
		items = client.GetTrending(mediaType, timeWindow)
	case category == "now_playing":
		items = client.GetNowPlaying(mediaType)
	case category == "top_rated":
		items = client.GetTopRated(mediaType)
	case category == "upcoming":
		items = client.GetUpcoming(mediaType)
	}

	if items == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": true,
			"items":      []any{},
			"error":      "TMDB 请求失败，请检查 ENV_TMDB_API_KEY 是否正确或网络是否可访问 TMDB",
		})
		return
	}
	if len(items) > 0 {
		tmdbCacheMu.Lock()
		now := time.Now()
		for k, cached := range tmdbCache {
			if now.Sub(cached[0].(time.Time)) >= tmdbCacheTTL {
				delete(tmdbCache, k)
			}
		}
		if len(tmdbCache) >= 50 {
			for k := range tmdbCache {
				delete(tmdbCache, k)
				break
			}
		}
		tmdbCache[cacheKey] = [2]any{now, items}
		tmdbCacheMu.Unlock()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":  true,
		"category":    category,
		"type":        mediaType,
		"time_window": timeWindowIf(category, timeWindow),
		"query":       queryOrNil(query),
		"items":       items,
	})
}

func timeWindowIf(category, tw string) any {
	if category == "trending" {
		return tw
	}
	return nil
}

func queryOrNil(q string) any {
	if q != "" {
		return q
	}
	return nil
}

// ---------- POST /api/tmdb/subscribe ----------

func (s *Server) handleTMDBSubscribe(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Title  string `json:"title"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "无效请求"})
		return
	}
	title := strings.TrimSpace(data.Title)
	if title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "影片标题不能为空"})
		return
	}
	var ok bool
	var message, filterValue string
	if data.Action == "unsubscribe" {
		ok, message, filterValue = s.removeFilterKeyword(title)
	} else {
		ok, message, filterValue = s.addFilterKeyword(title)
	}
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": message, "filter_value": filterValue})
}

// ---------- GET /api/tmdb/detail ----------

func (s *Server) handleTMDBDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	mediaType := r.URL.Query().Get("type")
	if mediaType == "" {
		mediaType = "movie"
	}
	tmdbID, err := strconv.Atoi(idStr)
	if err != nil || tmdbID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少有效的 TMDB ID"})
		return
	}
	if mediaType != "movie" && mediaType != "tv" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "不支持的 type，可选 movie/tv"})
		return
	}
	if tmdbAPIKey() == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"error":      "未配置 TMDB API Key，请前往「用户配置」填写 ENV_TMDB_API_KEY",
		})
		return
	}
	client := tmdbClient()
	detail := client.GetDetailDict(tmdbID, mediaType)
	if detail == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "error": "请求失败，请检查 ENV_TMDB_API_KEY 或网络"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "detail": detail})
}

// ---------- GET /api/media/episodes ----------

func (s *Server) handleMediaEpisodes(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	mediaType := r.URL.Query().Get("type")
	if mediaType == "" {
		mediaType = "tv"
	}
	tmdbID, err := strconv.Atoi(idStr)
	if err != nil || tmdbID <= 0 || (mediaType != "movie" && mediaType != "tv") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "参数错误"})
		return
	}

	cacheKey := mediaType + ":" + idStr
	episodeCacheMu.Lock()
	if cached, ok := episodeCache[cacheKey]; ok {
		if time.Since(cached[0].(time.Time)) < episodeTTL {
			episodeCacheMu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"emby": cached[1], "total": cached[2]})
			return
		}
	}
	episodeCacheMu.Unlock()

	var emby, total any = nil, nil

	// TMDB 总集数（电影为 1）
	if tmdbAPIKey() != "" {
		client := tmdbClient()
		data, err := client.GetJSON(fmt.Sprintf("/%s/%d", mediaType, tmdbID), nil)
		if err == nil {
			var d struct {
				NumberOfEpisodes any `json:"number_of_episodes"`
			}
			if json.Unmarshal(data, &d) == nil {
				if mediaType == "tv" {
					total = d.NumberOfEpisodes
				} else {
					total = 1
				}
			}
		}
	}

	// Emby 已收录条目数
	embyClient := embyClient()
	if embyClient != nil {
		ctx := context.Background()
		if mediaType == "movie" {
			if resp := embyRequest(ctx, embyClient, "/emby/Items", map[string]string{
				"Recursive":           "true",
				"IncludeItemTypes":    "Movie",
				"AnyProviderIdEquals": "tmdb." + idStr,
				"Limit":               "1",
			}); resp != nil {
				emby = resp["TotalRecordCount"]
			}
		} else {
			resp := embyRequest(ctx, embyClient, "/emby/Items", map[string]string{
				"Recursive":           "true",
				"IncludeItemTypes":    "Series",
				"AnyProviderIdEquals": "tmdb." + idStr,
				"Limit":               "1",
			})
			if resp != nil {
				if items, ok := resp["Items"].([]any); ok && len(items) > 0 {
					if first, ok := items[0].(map[string]any); ok {
						if resp2 := embyRequest(ctx, embyClient, "/emby/Items", map[string]string{
							"ParentId":         fmt.Sprintf("%v", first["Id"]),
							"Recursive":        "true",
							"IncludeItemTypes": "Episode",
							"Limit":            "1",
						}); resp2 != nil {
							emby = resp2["TotalRecordCount"]
						}
					}
				}
			}
		}
	}

	episodeCacheMu.Lock()
	if len(episodeCache) >= 300 {
		for k := range episodeCache {
			delete(episodeCache, k)
			break
		}
	}
	episodeCache[cacheKey] = [3]any{time.Now(), emby, total}
	episodeCacheMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"emby": emby, "total": total})
}

// embyRequest 请求 Emby GET API，失败返回 nil。
func embyRequest(ctx context.Context, c *strm.EmbyClient, endpoint string, params map[string]string) map[string]any {
	raw, status, err := c.GetJSON(ctx, endpoint, params)
	if err != nil || status != 200 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// ---------- GET /api/media/calendar ----------

func (s *Server) handleMediaCalendar(w http.ResponseWriter, r *http.Request) {
	idsParam := r.URL.Query().Get("ids")
	tvSet := map[int]bool{}
	for _, seg := range strings.Split(idsParam, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		mtype, mid := "tv", seg
		if strings.Contains(seg, ":") {
			parts := strings.SplitN(seg, ":", 2)
			mtype, mid = parts[0], parts[1]
		}
		if mtype == "tv" {
			if id, err := strconv.Atoi(mid); err == nil {
				tvSet[id] = true
			}
		}
	}
	var tvIDs []int
	for id := range tvSet {
		tvIDs = append(tvIDs, id)
	}
	sortInts(tvIDs)

	today := time.Now().Format("2006-01-02")

	// 缓存命中要求仍是同一天：today 不参与缓存，避免跨天后返回昨天的日期（追剧日历周几错位）。
	calendarCacheMu.Lock()
	if cached, ok := calendarCache["_"]; ok {
		arr := cached.([]any)
		if arr[2].(string) == today && time.Since(arr[0].(time.Time)) < calendarTTL {
			if ids, ok := arr[1].([]int); ok && equalInts(ids, tvIDs) {
				calendarCacheMu.Unlock()
				writeJSON(w, http.StatusOK, map[string]any{
					"configured": true, "today": arr[2], "shows": arr[3], "dates": arr[4],
				})
				return
			}
		}
	}
	calendarCacheMu.Unlock()

	if tmdbAPIKey() == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"error":      "未配置 TMDB API Key，请前往「用户配置」填写 ENV_TMDB_API_KEY",
		})
		return
	}

	shows := map[string]any{}
	dates := map[string]any{}
	client := tmdbClient()
	for _, tmdbID := range tvIDs {
		info := getUpcomingEpisodes(client, tmdbID, today)
		if info == nil {
			continue
		}
		shows[strconv.Itoa(tmdbID)] = map[string]any{"title": info.title, "season": info.season}
		for airDate, eps := range info.byDate {
			if dates[airDate] == nil {
				dates[airDate] = map[string]any{}
			}
			dates[airDate].(map[string]any)[strconv.Itoa(tmdbID)] = eps
		}
	}

	calendarCacheMu.Lock()
	calendarCache["_"] = []any{time.Now(), tvIDs, today, shows, dates}
	calendarCacheMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true, "today": today, "shows": shows, "dates": dates,
	})
}

// upcomingInfo 已订阅剧未来已排播集。
type upcomingInfo struct {
	title  string
	season int
	byDate map[string][]int
}

func getUpcomingEpisodes(client *transfer.TmdbClient, tmdbID int, today string) *upcomingInfo {
	data, err := client.GetJSON(fmt.Sprintf("/tv/%d", tmdbID), nil)
	if err != nil {
		return nil
	}
	var d struct {
		Name    string `json:"name"`
		Orig    string `json:"original_name"`
		Seasons []struct {
			SeasonNumber int    `json:"season_number"`
			EpisodeCount int    `json:"episode_count"`
			AirDate      string `json:"air_date"`
		} `json:"seasons"`
	}
	if json.Unmarshal(data, &d) != nil {
		return nil
	}
	title := d.Name
	if title == "" {
		title = d.Orig
	}
	var seasons []struct {
		SeasonNumber int    `json:"season_number"`
		EpisodeCount int    `json:"episode_count"`
		AirDate      string `json:"air_date"`
	}
	for _, s := range d.Seasons {
		if s.SeasonNumber > 0 && s.EpisodeCount > 0 {
			seasons = append(seasons, s)
		}
	}
	if len(seasons) == 0 {
		return nil
	}
	// 当前播出季 = 已开播（首播日 ≤ 今天）的最晚一季；否则回退到最新一季
	target := seasons[0]
	latest := seasons[0]
	for _, s := range seasons {
		if s.SeasonNumber > latest.SeasonNumber {
			latest = s
		}
	}
	for _, s := range seasons {
		if s.AirDate != "" && s.AirDate <= today && s.SeasonNumber > target.SeasonNumber {
			target = s
		}
	}
	if !(target.AirDate != "" && target.AirDate <= today) {
		target = latest
	}

	episodes := client.GetEpisodes(tmdbID, target.SeasonNumber)
	byDate := map[string][]int{}
	for _, ep := range episodes {
		airDate := strings.TrimSpace(ep.AirDate)
		if airDate == "" || airDate < today || ep.EpisodeNumber <= 0 {
			continue
		}
		byDate[airDate] = append(byDate[airDate], ep.EpisodeNumber)
	}
	if len(byDate) == 0 {
		return nil
	}
	return &upcomingInfo{title: title, season: target.SeasonNumber, byDate: byDate}
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
