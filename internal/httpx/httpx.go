// Package httpx 提供带代理、超时、重试的 HTTP 客户端（对应 Python requests 用法）。
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client 封装 http.Client 与常用请求辅助。
type Client struct {
	http       *http.Client
	Timeout    time.Duration
	MaxRetries int
}

// New 创建客户端（直连，透明代理自动处理；需显式代理请使用 SetProxy）。
func New(timeout time.Duration) *Client {
	transport := &http.Transport{
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{
		http:       &http.Client{Transport: transport, Timeout: timeout},
		Timeout:    timeout,
		MaxRetries: 3,
	}
}

// NewWithClient 包装已有 http.Client。
func NewWithClient(c *http.Client) *Client { return &Client{http: c} }

// HTTP 返回底层 *http.Client。
func (c *Client) HTTP() *http.Client { return c.http }

// SetProxy 手动设置代理。
func (c *Client) SetProxy(proxyURL string) {
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		tr = &http.Transport{}
		c.http.Transport = tr
	}
	if proxyURL == "" {
		tr.Proxy = nil
		return
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return
	}
	tr.Proxy = http.ProxyURL(u)
}

// Req 发起请求并自动重试瞬时错误（网络错误 / 5xx / 429）。
func (c *Client) Req(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (*http.Response, error) {
	var lastErr error
	retries := c.MaxRetries
	if retries < 1 {
		retries = 1
	}
	for attempt := 0; attempt < retries; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", UA)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			// 网络层错误可重试
			if attempt < retries-1 && isTransient(err) {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return nil, err
		}
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if attempt < retries-1 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return nil, lastErr
		}
		return resp, nil
	}
	return nil, lastErr
}

func isTransient(err error) bool {
	if ue, ok := err.(*url.Error); ok {
		err = ue.Err
	}
	return err != nil && !strings.Contains(err.Error(), "context canceled")
}

// Get 便捷 GET。
func (c *Client) Get(ctx context.Context, rawURL string, headers map[string]string) ([]byte, int, error) {
	return c.doBody(ctx, http.MethodGet, rawURL, headers, nil)
}

// Post 便捷 POST（body 为原始字节）。
func (c *Client) Post(ctx context.Context, rawURL string, headers map[string]string, body []byte) ([]byte, int, error) {
	return c.doBody(ctx, http.MethodPost, rawURL, headers, body)
}

// PostJSON 便捷 POST，自动设置 Content-Type: application/json。
func (c *Client) PostJSON(ctx context.Context, rawURL string, headers map[string]string, v any) ([]byte, int, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, 0, err
	}
	if headers == nil {
		headers = map[string]string{}
	}
	if _, ok := headers["Content-Type"]; !ok {
		headers["Content-Type"] = "application/json"
	}
	return c.doBody(ctx, http.MethodPost, rawURL, headers, data)
}

// Put 便捷 PUT（body 为原始字节）。
func (c *Client) Put(ctx context.Context, rawURL string, headers map[string]string, body []byte) ([]byte, int, error) {
	return c.doBody(ctx, http.MethodPut, rawURL, headers, body)
}

func (c *Client) doBody(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) ([]byte, int, error) {
	resp, err := c.Req(ctx, method, rawURL, headers, body)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// UA 默认 User-Agent。
const UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// ReadBody 读取响应体并关闭。
func ReadBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
