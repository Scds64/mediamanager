package bot

// 用户状态管理（对应 UserStateManager：SELECTING_FILE / CONFIRM_DELETE / ASK_POST 等会话状态）。

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// UserState 用户会话状态。
type UserState struct {
	State string
	Data  string
}

// UserStateManager 基于 SQLite 的用户状态管理器。
type UserStateManager struct {
	dbPath string
	db     *sql.DB
}

// NewUserStateManager 创建状态管理器并初始化表。
func NewUserStateManager(dbPath string) *UserStateManager {
	if dbPath == "" {
		dbPath = "data/user_states.db"
	}
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	m := &UserStateManager{dbPath: dbPath}
	m.init()
	return m
}

func (m *UserStateManager) init() {
	db, err := sql.Open("sqlite", m.dbPath)
	if err != nil {
		return
	}
	m.db = db
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS user_states (
		user_id INTEGER PRIMARY KEY,
		state TEXT,
		data TEXT)`)
}

func (m *UserStateManager) dbConn() *sql.DB {
	if m.db != nil {
		return m.db
	}
	db, err := sql.Open("sqlite", m.dbPath)
	if err != nil {
		return nil
	}
	m.db = db
	return db
}

// SetState 保存用户状态。
func (m *UserStateManager) SetState(userID int64, state, data string) {
	db := m.dbConn()
	if db == nil {
		return
	}
	_, _ = db.Exec("INSERT OR REPLACE INTO user_states VALUES (?, ?, ?)", userID, state, data)
}

// GetState 获取用户状态。
func (m *UserStateManager) GetState(userID int64) (string, string) {
	db := m.dbConn()
	if db == nil {
		return "", ""
	}
	var state, data string
	err := db.QueryRow("SELECT state, data FROM user_states WHERE user_id=?", userID).Scan(&state, &data)
	if err != nil {
		return "", ""
	}
	return state, data
}

// ClearState 清除用户状态。
func (m *UserStateManager) ClearState(userID int64) {
	db := m.dbConn()
	if db == nil {
		return
	}
	_, _ = db.Exec("DELETE FROM user_states WHERE user_id=?", userID)
}

// Close 关闭数据库。
func (m *UserStateManager) Close() {
	if m.db != nil {
		_ = m.db.Close()
		m.db = nil
	}
}
