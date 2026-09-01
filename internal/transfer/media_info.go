// Package transfer 对应 Python transfer_engine：文件整理引擎。
// 识别 → 分类 → 目录匹配 → 改名 → 移动 → 刮削 → 历史 → 通知。
package transfer

// MetaInfo 文件名识别结果（由 meta_recognizer 产出）。
type MetaInfo struct {
	Name          string
	Year          int
	Season        int
	Episode       int
	Episodes      []int
	Type          string // movie / tv，空表示未确定
	Source        string
	Resolution    string
	ReleaseGroup  string
	Container     string
	TMDBID        int
	RawName       string
}

// MediaInfo TMDB 识别结果。
type MediaInfo struct {
	Title              string
	Year               int
	Type               string // movie / tv
	Category           string
	TMDBID             int
	IMDBID             string
	OriginalTitle      string
	OriginalLanguage   string
	ProductionCountries []string
	OriginCountry      []string
	GenreIDs           []int
	Overview           string
	PosterPath         string
	BackdropPath       string
	NumberOfEpisodes   int
	NumberOfSeasons    int
	Runtime            int
	VoteAverage        float64
}

// GenreNames 用于 NFO 的 genre 名称列表（与 Python 用 genre_ids 一致，此处保留 ids 转字符串）。
func (m *MediaInfo) genreStrings() []string {
	out := make([]string, 0, len(m.GenreIDs))
	for _, g := range m.GenreIDs {
		out = append(out, itoa(g))
	}
	return out
}
