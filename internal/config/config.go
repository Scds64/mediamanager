// Package config 提供 .env 配置文件的解析、读取与回写（对应 Python python-dotenv 用法）。
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config 维护一份 key=value 配置。
// 支持从 user.env 加载，进程环境变量优先级更高（与 load_dotenv(override=False) 行为一致）。
type Config struct {
	path string
	vals map[string]string
	keys []string // 保留原始顺序用于回写
}

// Load 从 path 读取 .env 配置。
// 不覆盖 os.Environ() 值（避免 Docker env_file 加载后与文件值冲突）。
// 进程环境变量仅在文件没有该 key 时作为 fallback 使用。
func Load(path string) (*Config, error) {
	c := &Config{path: path, vals: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	parseEnv(string(data), c)
	// 补充进程环境变量中文件没有的 key（不覆盖文件值）
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		k, v := kv[:i], kv[i+1:]
		if _, ok := c.vals[k]; !ok {
			c.set(k, v)
		}
	}
	return c, nil
}

// parseEnv 解析 dotenv 格式文本。支持 # 注释、KEY=VALUE、引号包裹的值。
func parseEnv(text string, c *Config) {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 行内注释：仅当 # 前是空白（或行首）时才视为注释；否则 # 属于值的一部分（与 python-dotenv 一致）。
		// 例如 ENV_STRM_PATHS=/media#影视2 中的 # 是映射分隔符，不能截断
		for j := 0; j < len(line); j++ {
			if line[j] == '#' && (j == 0 || line[j-1] == ' ' || line[j-1] == '\t') {
				line = strings.TrimSpace(line[:j])
				break
			}
		}
		i := strings.IndexByte(line, '=')
		if i < 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		v = unquote(v)
		c.set(k, v)
	}
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

func (c *Config) set(k, v string) {
	if _, ok := c.vals[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.vals[k] = v
}

// Path 返回配置文件路径。
func (c *Config) Path() string { return c.path }

// Get 读取字符串配置，空值返回 def。
func (c *Config) Get(name, def string) string {
	if v, ok := c.vals[name]; ok {
		return v
	}
	return def
}

// GetInt 读取整数配置。
func (c *Config) GetInt(name string, def int) int {
	if v, ok := c.vals[name]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// GetBool 读取布尔配置（兼容 "1"/"true"/"yes"）。
func (c *Config) GetBool(name string, def bool) bool {
	v, ok := c.vals[name]
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	return def
}

// GetFloat 读取浮点配置。
func (c *Config) GetFloat(name string, def float64) float64 {
	if v, ok := c.vals[name]; ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return def
}

// GetOrEnv 先取配置再取进程环境变量。
func (c *Config) GetOrEnv(name, def string) string {
	if v := c.Get(name, ""); v != "" {
		return v
	}
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Set 设置配置值。
func (c *Config) Set(name, value string) { c.set(name, value) }

// All 返回全部键值（拷贝）。
func (c *Config) All() map[string]string {
	out := make(map[string]string, len(c.vals))
	for k, v := range c.vals {
		out[k] = v
	}
	return out
}

// Has 判断键是否存在。
func (c *Config) Has(name string) bool {
	_, ok := c.vals[name]
	return ok
}

// Save 将当前配置写回文件。保留原始顺序与未知键，不改变文件中其他内容。
func (c *Config) Save() error {
	return c.SaveAs(c.path)
}

// SaveAs 将当前配置写回指定文件。
func (c *Config) SaveAs(path string) error {
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, k := range c.keys {
		v := c.vals[k]
		// 需要引号的情况：含 # 或空格
		if strings.ContainsAny(v, "# \t") {
			v = strconv.Quote(v)
		}
		b.WriteString(fmt.Sprintf("%s=%s\n", k, v))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// UpdateFromMap 批量更新值并合并新键。
func (c *Config) UpdateFromMap(m map[string]string) {
	for k, v := range m {
		c.set(k, v)
	}
}

func dirOf(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return "."
	}
	return p[:i]
}
