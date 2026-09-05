// Package oauth 实现 LitePan OAuth 认证客户端（对应 Python oauth_client.py）。
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"mmbot/internal/httpx"
)

// OAuthServerURL LitePan OAuth 服务地址。
const OAuthServerURL = "https://oauth.litepan.top"

// Client LitePan OAuth 客户端。
type Client struct {
	DriverType     string
	AccessToken    string
	RefreshToken   string
	TokenExpiresAt int64
	tokenFile      string
	http           *httpx.Client
}

// New 创建 OAuth 客户端，自动加载持久化 token。
func New(driverType string) *Client {
	c := &Client{
		DriverType: driverType,
		// 与 Web 层 handleOAuthStatus 读取路径保持一致（data/ 挂载卷，容器重建不丢失）
		tokenFile: filepath.Join("data", "oauth_token.json"),
		http:      httpx.New(30 * time.Second),
	}
	c.loadToken()
	return c
}

// NewWithFile 指定 token 文件路径创建客户端。
func NewWithFile(driverType, tokenFile string) *Client {
	c := &Client{
		DriverType: driverType,
		tokenFile:  tokenFile,
		http:       httpx.New(30 * time.Second),
	}
	c.loadToken()
	return c
}

func (c *Client) headers() map[string]string {
	return map[string]string{
		"User-Agent":   "LitePan/0.3.2-Beta",
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}
}

func (c *Client) loadToken() {
	data, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return
	}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &t); err != nil {
		log.Printf("[OAuth] 读取token文件失败: %v", err)
		return
	}
	c.AccessToken = t.AccessToken
	c.RefreshToken = t.RefreshToken
	c.TokenExpiresAt = t.ExpiresAt
}

func (c *Client) saveToken() {
	if err := os.MkdirAll(filepath.Dir(c.tokenFile), 0o755); err != nil {
		return
	}
	data, _ := json.Marshal(map[string]any{
		"access_token":  c.AccessToken,
		"refresh_token": c.RefreshToken,
		"expires_at":    c.TokenExpiresAt,
	})
	if err := os.WriteFile(c.tokenFile, data, 0o644); err != nil {
		log.Printf("[OAuth] 保存token文件失败: %v", err)
	}
}

func (c *Client) isTokenExpired() bool {
	if c.AccessToken == "" {
		return true
	}
	return time.Now().Unix() >= c.TokenExpiresAt-60
}

// StartAuth 发起 OAuth 授权，返回授权信息（含 URL、session_id）。
func (c *Client) StartAuth(ctx context.Context) (map[string]any, error) {
	url := OAuthServerURL + "/api/oauth/start"
	payload := map[string]any{
		"driver_type":  c.DriverType,
		"callback_url": OAuthServerURL + "/callback-popup",
	}
	raw, status, err := c.http.PostJSON(ctx, url, c.headers(), payload)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("OAuth start HTTP %d: %s", status, string(raw))
	}
	var resp struct {
		Success bool `json:"success"`
		Data    map[string]any
		Message string
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("OAuth start failed: %s", resp.Message)
	}
	return resp.Data, nil
}

// PollStatus 轮询授权状态，返回 token 数据。
func (c *Client) PollStatus(ctx context.Context, sessionID string, maxAttempts int) (map[string]any, error) {
	if maxAttempts <= 0 {
		maxAttempts = 60
	}
	url := fmt.Sprintf("%s/api/oauth/status/%s", OAuthServerURL, sessionID)
	for i := 0; i < maxAttempts; i++ {
		raw, status, err := c.http.Get(ctx, url, c.headers())
		if err == nil && status == 200 {
			var resp struct {
				Success bool `json:"success"`
				Data    map[string]any
			}
			if err := json.Unmarshal(raw, &resp); err == nil && resp.Success {
				if at, _ := resp.Data["access_token"].(string); at != "" {
					return resp.Data, nil
				}
				if td, ok := resp.Data["token_data"].(map[string]any); ok {
					return td, nil
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("OAuth认证超时")
}

// ConfirmReceived 确认 token 已接收。
func (c *Client) ConfirmReceived(ctx context.Context, sessionID string) {
	url := fmt.Sprintf("%s/api/oauth/confirm-received/%s", OAuthServerURL, sessionID)
	_, _, _ = c.http.Post(ctx, url, c.headers(), nil)
}

// DoRefreshToken 刷新 token，成功后更新内存并持久化。
func (c *Client) DoRefreshToken(ctx context.Context) bool {
	if c.RefreshToken == "" {
		log.Printf("[OAuth] 没有refresh_token，无法刷新")
		return false
	}
	url := OAuthServerURL + "/api/oauth/refresh"
	payload := map[string]any{
		"driver_type":   c.DriverType,
		"refresh_token": c.RefreshToken,
	}
	raw, status, err := c.http.PostJSON(ctx, url, c.headers(), payload)
	if err != nil {
		log.Printf("[OAuth] 刷新token异常: %v", err)
		return false
	}
	if status != 200 {
		log.Printf("[OAuth] 刷新token HTTP %d: %s", status, string(raw))
		return false
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		}
		Message string
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false
	}
	if !resp.Success {
		log.Printf("[OAuth] 刷新token失败: %s", resp.Message)
		return false
	}
	c.AccessToken = resp.Data.AccessToken
	c.RefreshToken = resp.Data.RefreshToken
	if resp.Data.ExpiresIn == 0 {
		resp.Data.ExpiresIn = 7200
	}
	c.TokenExpiresAt = time.Now().Unix() + resp.Data.ExpiresIn
	c.saveToken()
	return true
}

// GetToken 获取有效 access_token，过期时自动刷新。
func (c *Client) GetToken(ctx context.Context) (string, error) {
	if c.AccessToken == "" {
		return "", fmt.Errorf("未找到OAuth token，请先进行OAuth认证")
	}
	if c.isTokenExpired() {
		if c.RefreshToken != "" && c.DoRefreshToken(ctx) {
			return c.AccessToken, nil
		}
		return "", fmt.Errorf("OAuth token已过期且无法刷新，请重新认证")
	}
	return c.AccessToken, nil
}

// Authenticate 返回可用 token；必要时刷新或轮询 session。
func (c *Client) Authenticate(ctx context.Context, sessionID string) (string, error) {
	if !c.isTokenExpired() {
		return c.AccessToken, nil
	}
	if c.RefreshToken != "" && c.DoRefreshToken(ctx) {
		return c.AccessToken, nil
	}
	if sessionID != "" {
		tokenData, err := c.PollStatus(ctx, sessionID, 60)
		if err != nil {
			return "", err
		}
		at, _ := tokenData["access_token"].(string)
		rt, _ := tokenData["refresh_token"].(string)
		expiresIn, _ := tokenData["expires_in"].(float64)
		if expiresIn == 0 {
			expiresIn = 7200
		}
		c.AccessToken = at
		c.RefreshToken = rt
		c.TokenExpiresAt = time.Now().Unix() + int64(expiresIn)
		c.saveToken()
		c.ConfirmReceived(ctx, sessionID)
		return c.AccessToken, nil
	}
	return "", fmt.Errorf("需要进行OAuth认证，请先调用StartAuth()获取认证URL")
}
