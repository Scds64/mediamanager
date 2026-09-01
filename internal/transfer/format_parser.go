package transfer

// 命名格式渲染模块（对应 format_parser.py）。
// 实现 Jinja2 模板的常用子集：{{ var }}、{{ var|filter(args) }}、
// {{ 'lit' if cond else 'lit2' }}、'lit' + var 拼接。

import (
	"log"
	"regexp"
	"strconv"
	"strings"
)

// 默认电影命名格式。
const DefaultMovieFormat = "{{category}}/{{title}} ({{year}})/{{title}} ({{year}}){{ ' - ' + resolution if resolution else '' }}"

// 默认电视剧命名格式。
const DefaultTVFormat = "{{category}}/{{title}} ({{year}})/Season {{season|string('d2') }}/{{title}} - S{{season|string('d2')}}E{{episode|string('d2')}}{{ ' - ' + resolution if resolution else '' }}"

var tmplBlockRe = regexp.MustCompile(`\{\{(.*?)\}\}`)

// FormatParser 命名格式渲染器。
type FormatParser struct {
	fmt string
	// 模板解析后的片段
	blocks []tmplBlock
}

type tmplBlock struct {
	literal string // 字面文本（非空表示纯文本）
	expr    string // 表达式（literal 为空且 cond 为空时有效）
	cond    string // if 条件（非空表示 {% if %} 块）
	body    []tmplBlock // {% if %} 块内部模板
}

// newFormatParser 解析模板。
func newFormatParser(fmt string) *FormatParser {
	if fmt == "" {
		fmt = "{{title}}"
	}
	p := &FormatParser{fmt: fmt}
	p.blocks = parseTemplateBlocks(fmt)
	return p
}

// parseTemplateBlocks 递归解析模板，支持 {{ expr }} 与 {% if C %}BODY{% endif %} 语句块。
func parseTemplateBlocks(s string) []tmplBlock {
	var blocks []tmplBlock
	for i := 0; i < len(s); {
		if s[i] == '{' && i+1 < len(s) {
			if s[i+1] == '{' { // {{ expr }}
				if j := strings.Index(s[i+2:], "}}"); j >= 0 {
					blocks = append(blocks, tmplBlock{expr: strings.TrimSpace(s[i+2 : i+2+j])})
					i = i + 2 + j + 2
					continue
				}
			} else if s[i+1] == '%' { // {% if C %}BODY{% endif %}
				if j := strings.Index(s[i+2:], "%}"); j >= 0 {
					inner := strings.TrimSpace(s[i+2 : i+2+j])
					if f := strings.Fields(inner); len(f) > 0 && f[0] == "if" {
						cond := strings.TrimSpace(inner[len("if"):])
						bodyStart := i + 2 + j + 2
						if endIdx, ok := findMatchingEndif(s, bodyStart); ok {
							blocks = append(blocks, tmplBlock{cond: cond, body: parseTemplateBlocks(s[bodyStart:endIdx])})
							if e := strings.Index(s[endIdx+2:], "%}"); e >= 0 {
								i = endIdx + 2 + e + 2
							} else {
								i = len(s)
							}
							continue
						}
					}
				}
			}
		}
		// 收集字面量直到下一个 {{ 或 {%
		j := i
		for j < len(s) {
			if s[j] == '{' && j+1 < len(s) && (s[j+1] == '{' || s[j+1] == '%') {
				break
			}
			j++
		}
		if j > i {
			blocks = append(blocks, tmplBlock{literal: s[i:j]})
		}
		i = j
	}
	return blocks
}

// findMatchingEndif 从 bodyStart 开始找与 {% if %} 配对的 {% endif %}，返回其起始位置。
func findMatchingEndif(s string, bodyStart int) (int, bool) {
	depth := 1
	for k := bodyStart; k < len(s); {
		if s[k] == '{' && k+1 < len(s) && s[k+1] == '%' {
			if e := strings.Index(s[k+2:], "%}"); e >= 0 {
				inner := strings.TrimSpace(s[k+2 : k+2+e])
				if f := strings.Fields(inner); len(f) > 0 {
					switch f[0] {
					case "if":
						depth++
					case "endif":
						depth--
						if depth == 0 {
							return k, true
						}
					}
				}
				k = k + 2 + e + 2
				continue
			}
		}
		k++
	}
	return 0, false
}

// renderBlocks 渲染模板块列表（递归处理 if 块）。
func renderBlocks(blocks []tmplBlock, ctx renderCtx) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.literal != "" {
			b.WriteString(blk.literal)
			continue
		}
		if blk.cond != "" {
			if evalCond(blk.cond, ctx) {
				b.WriteString(renderBlocks(blk.body, ctx))
			}
			continue
		}
		b.WriteString(evalExpr(blk.expr, ctx))
	}
	return b.String()
}

// renderCtx 渲染上下文（变量 → 字符串值）。
type renderCtx map[string]string

// Render 渲染路径，返回相对路径。
func (p *FormatParser) Render(meta MetaInfo, media *MediaInfo, episode int) string {
	ctx := buildRenderCtx(meta, media, episode)

	rendered := renderBlocks(p.blocks, ctx)

	// 清理多余的分隔符和空目录层
	parts := make([]string, 0)
	for _, p := range strings.Split(rendered, "/") {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "/")
}

// buildRenderCtx 构建渲染上下文（与 Python render 的 ctx 一致）。
func buildRenderCtx(meta MetaInfo, media *MediaInfo, episode int) renderCtx {
	// 优先使用 media 信息，降级用 meta
	title := meta.Name
	if media != nil && media.Title != "" {
		title = media.Title
	}
	if title == "" {
		title = "未知标题"
	}
	year := meta.Year
	if media != nil && media.Year > 0 {
		year = media.Year
	}
	mtype := meta.Type
	if media != nil && media.Type != "" {
		mtype = media.Type
	}
	category := ""
	if media != nil {
		category = media.Category
	}

	ep := meta.Episode
	if episode > 0 {
		ep = episode
	}

	rawName := meta.RawName
	videoCodec := extractVideoCodec(rawName)
	audioCodec := extractAudioCodec(rawName)
	edition := meta.Source
	part := extractPart(rawName)
	fileExt := ""
	if i := strings.LastIndexByte(rawName, '.'); i >= 0 {
		fileExt = "." + rawName[i+1:]
	}

	var tmdbID, originalTitle string
	if media != nil {
		if media.TMDBID > 0 {
			tmdbID = strconv.Itoa(media.TMDBID)
		}
		originalTitle = media.OriginalTitle
	}

	yearStr := ""
	if year > 0 {
		yearStr = strconv.Itoa(year)
	}
	seasonStr := ""
	if meta.Season > 0 {
		seasonStr = strconv.Itoa(meta.Season)
	}
	epStr := ""
	if ep > 0 {
		epStr = strconv.Itoa(ep)
	}

	enTitle := ""
	if originalTitle != "" && originalTitle != title {
		enTitle = originalTitle
	}

	seasonEpisode := formatSeasonEpisode(meta.Season, ep)
	effect := extractEffect(rawName)

	ctx := renderCtx{
		"title":          title,
		"year":           yearStr,
		"type":           mtype,
		"category":       category,
		"tmdb_id":        tmdbID,
		"season":         seasonStr,
		"episode":        epStr,
		"episodes":       joinInts(meta.Episodes, ","),
		"resolution":     meta.Resolution,
		"source":         meta.Source,
		"release_group":  meta.ReleaseGroup,
		"container":      meta.Container,
		"original_title": originalTitle,
		"tmdbid":         tmdbID,
		"en_title":       enTitle,
		"edition":        edition,
		"part":           part,
		"videoFormat":    meta.Resolution,
		"videoCodec":     videoCodec,
		"audioCodec":     audioCodec,
		"customization":  "",
		"releaseGroup":   meta.ReleaseGroup,
		"fileExt":        fileExt,
		"season_year":    yearStr,
		"season_episode": seasonEpisode,
		"effect":         effect,
	}
	return ctx
}

// evalExpr 计算表达式。
func evalExpr(expr string, ctx renderCtx) string {
	expr = strings.TrimSpace(expr)
	// 三元表达式：'a' if cond else 'b'
	if idx := findTopLevel(expr, "if"); idx >= 0 {
		thenPart := strings.TrimSpace(expr[:idx])
		rest := strings.TrimSpace(expr[idx+2:])
		cond := rest
		elseVal := ""
		if eidx := findTopLevel(rest, "else"); eidx >= 0 {
			cond = strings.TrimSpace(rest[:eidx])
			elseVal = strings.TrimSpace(rest[eidx+4:])
		}
		if evalCond(cond, ctx) {
			return evalConcat(thenPart, ctx)
		}
		return evalConcat(elseVal, ctx)
	}
	return evalConcat(expr, ctx)
}

// evalCond 求值条件表达式（形如 resolution 或 "literal" == "x"）。
func evalCond(cond string, ctx renderCtx) bool {
	cond = strings.TrimSpace(cond)
	// 处理 != == 比较
	if idx := findTopLevel(cond, "=="); idx >= 0 {
		l := strings.TrimSpace(cond[:idx])
		r := strings.TrimSpace(cond[idx+2:])
		return evalValue(l, ctx) == evalValue(r, ctx)
	}
	if idx := findTopLevel(cond, "!="); idx >= 0 {
		l := strings.TrimSpace(cond[:idx])
		r := strings.TrimSpace(cond[idx+2:])
		return evalValue(l, ctx) != evalValue(r, ctx)
	}
	v := evalValue(cond, ctx)
	return v != "" && v != "0"
}

// evalConcat 求值拼接表达式（'lit' + var 形式）。
func evalConcat(expr string, ctx renderCtx) string {
	expr = strings.TrimSpace(expr)
	parts := splitConcat(expr)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(evalValue(p, ctx))
	}
	return b.String()
}

// evalValue 求值单个值：字面量字符串 / 变量 / 带过滤器。
func evalValue(v string, ctx renderCtx) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// 字符串字面量
	if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
		return v[1 : len(v)-1]
	}
	// 过滤器：var|filter(args)
	if fIdx := findTopLevel(v, "|"); fIdx >= 0 {
		varName := strings.TrimSpace(v[:fIdx])
		filterPart := strings.TrimSpace(v[fIdx+1:])
		val := ctx[varName]
		return applyFilter(filterPart, val)
	}
	return ctx[v]
}

// applyFilter 应用过滤器。
func applyFilter(filterExpr string, val string) string {
	// 形如 string('d2') 或 pad(3)
	name := filterExpr
	args := ""
	if i := strings.IndexByte(filterExpr, '('); i >= 0 {
		name = strings.TrimSpace(filterExpr[:i])
		if j := strings.LastIndexByte(filterExpr, ')'); j > i {
			args = strings.TrimSpace(filterExpr[i+1 : j])
		}
	}
	name = strings.ToLower(name)
	switch name {
	case "string", "pad", "zfill":
		width := 2
		if strings.HasPrefix(args, "d") {
			if n, err := strconv.Atoi(args[1:]); err == nil {
				width = n
			}
		} else if n, err := strconv.Atoi(strings.Trim(args, "'\"")); err == nil {
			width = n
		}
		return padNumber(val, width)
	}
	return val
}

// splitConcat 按顶层 '+' 拆分。
func splitConcat(expr string) []string {
	var parts []string
	depth := 0
	inStr := byte(0)
	start := 0
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			inStr = c
			continue
		}
		if c == '(' {
			depth++
		} else if c == ')' {
			depth--
		} else if c == '+' && depth == 0 {
			parts = append(parts, strings.TrimSpace(expr[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(expr[start:]))
	return parts
}

// findTopLevel 在顶层（字符串字面量外）查找关键词。
func findTopLevel(expr, keyword string) int {
	depth := 0
	inStr := byte(0)
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			inStr = c
			continue
		}
		if c == '(' {
			depth++
			continue
		} else if c == ')' {
			depth--
			continue
		}
		if depth == 0 && strings.HasPrefix(expr[i:], keyword) {
			// 确保是完整词
			after := i + len(keyword)
			if after >= len(expr) || expr[after] == ' ' || expr[after] == '\t' {
				return i
			}
		}
	}
	return -1
}

// ---------- 工具 ----------

func padNumber(value string, width int) string {
	if value == "" {
		return ""
	}
	if n, err := strconv.Atoi(value); err == nil {
		out := strconv.FormatInt(int64(n), 10)
		if len(out) < width {
			return strings.Repeat("0", width-len(out)) + out
		}
		return out
	}
	// 字符串补零
	if len(value) < width {
		return strings.Repeat("0", width-len(value)) + value
	}
	return value
}

func joinInts(nums []int, sep string) string {
	parts := make([]string, 0, len(nums))
	for _, n := range nums {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, sep)
}

func formatSeasonEpisode(season, episode int) string {
	var b strings.Builder
	if season > 0 {
		b.WriteString("S")
		b.WriteString(padNumber(strconv.Itoa(season), 2))
	}
	if episode > 0 {
		b.WriteString("E")
		b.WriteString(padNumber(strconv.Itoa(episode), 2))
	}
	return b.String()
}

// ---------- 从文件名提取编码/分片/特效 ----------

var (
	videoCodecRe2 = regexp.MustCompile(`(?i)\b(HEVC|H\.?265|x265|H\.?264|x264|AVC|XVID|DivX|VC-?1|AV1)\b`)
	audioCodecRe  = regexp.MustCompile(`(?i)\b(TrueHD|Dolby\s?Atmos|Atmos|E-?AC-?3|DDP|DD\+?|DTS-HD|DTS-?HD.?MA|DTS|AC-?3|AAC|FLAC|LPCM|PCM|DD)([.\s-]*\d[.\s-]*\d?\b|\b)`) // ponytail: 第 2 组捕获声道（DDP5.1 / DDP 5.1 / DDP.5.1），标题清洗与声道展示共用；整体匹配避免声道数字残留进标题
	partRe        = regexp.MustCompile(`(?i)\b(?:CD|DISC|DISK|PART|PT)\s*(\d+)\b`)
	effectRe      = regexp.MustCompile(`(?i)\b(Dolby\.?Vision|DoVi|DV|HDR10\+|HDR10|HDR|HLG|SDR)\b`)
)

func extractVideoCodec(filename string) string {
	if m := videoCodecRe2.FindStringSubmatch(filename); m != nil {
		codec := strings.ToUpper(m[1])
		codec = strings.ReplaceAll(codec, "H.265", "H265")
		codec = strings.ReplaceAll(codec, "H.264", "H264")
		return codec
	}
	return ""
}

func extractAudioCodec(filename string) string {
	if m := audioCodecRe.FindStringSubmatch(filename); m != nil {
		codec := strings.ToUpper(m[1])
		codec = strings.ReplaceAll(codec, "-", "")
		codec = strings.ReplaceAll(codec, ".", "")
		codecMap := map[string]string{
			"EAC3": "EAC3", "DDP": "DDP", "DD": "DD", "DTSHDMA": "DTS-HD MA",
			"DTSHD": "DTS-HD", "DTS": "DTS", "AC3": "AC3", "AAC": "AAC",
			"FLAC": "FLAC", "TRUEHD": "TrueHD", "ATMOS": "Atmos", "LPCM": "LPCM",
			"PCM": "PCM",
		}
		if v, ok := codecMap[codec]; ok {
			codec = v
		}
		// 编解码+声道连写/连排（DDP5.1 / DDP 5.1 / DDP.5.1）时把声道拼回展示
		if len(m) > 2 {
			if ch := strings.Trim(m[2], " .-_"); ch != "" {
				codec += ch
			}
		}
		return codec
	}
	return ""
}

func extractPart(filename string) string {
	if m := partRe.FindStringSubmatch(filename); m != nil {
		return "CD" + m[1]
	}
	return ""
}

// effectStdMap 特效标准化映射。
var effectStdMap = map[string]string{
	"DOLBYVISION": "DV", "DOVI": "DV", "DV": "DV",
	"HDR10PLUS": "HDR10+", "HDR10": "HDR10", "HDR": "HDR",
	"HLG": "HLG", "SDR": "SDR",
}

func extractEffect(filename string) string {
	if m := effectRe.FindStringSubmatch(filename); m != nil {
		effect := strings.ToUpper(m[1])
		effect = strings.ReplaceAll(effect, ".", "")
		if v, ok := effectStdMap[effect]; ok {
			return v
		}
		return effect
	}
	return ""
}

// ExtractEffects 从文件名提取所有视频特效（DV、HDR、HDR10+ 等），空格连接去重；普通 SDR 不返回。
func ExtractEffects(filename string) string {
	if filename == "" {
		return ""
	}
	var effects []string
	for _, m := range effectRe.FindAllStringSubmatch(filename, -1) {
		std := strings.ToUpper(m[1])
		std = strings.ReplaceAll(std, ".", "")
		if v, ok := effectStdMap[std]; ok {
			std = v
		}
		if std == "SDR" {
			continue
		}
		found := false
		for _, e := range effects {
			if e == std {
				found = true
				break
			}
		}
		if !found {
			effects = append(effects, std)
		}
	}
	return strings.Join(effects, " ")
}

// BuildTargetPath 便捷函数：根据媒体类型选择格式并渲染。
func BuildTargetPath(meta MetaInfo, media *MediaInfo, movieFormat, tvFormat string, episode int) string {
	mtype := meta.Type
	if media != nil && media.Type != "" {
		mtype = media.Type
	}
	if mtype == "" {
		mtype = "movie"
	}
	fmt := movieFormat
	if mtype == "tv" {
		fmt = tvFormat
	}
	if fmt == "" {
		if mtype == "tv" {
			fmt = DefaultTVFormat
		} else {
			fmt = DefaultMovieFormat
		}
	}
	parser := newFormatParser(fmt)
	return parser.Render(meta, media, episode)
}

var _ = log.Printf
