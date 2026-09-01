package node

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxBodyLog 响应体写入日志的最大字节数。
const maxBodyLog = 8192

// HTTPExecutor 发起 HTTP 请求的节点执行器。
type HTTPExecutor struct{}

func init() { Register(&HTTPExecutor{}) }

// Type 返回节点类型 http。
func (e *HTTPExecutor) Type() string { return TypeHTTP }

// Run 按配置发起 HTTP 请求，输出 status_code、body 与 headers。
func (e *HTTPExecutor) Run(ctx context.Context, cfg map[string]any, ec *Context) (map[string]any, error) {
	method := strings.ToUpper(strOr(cfg, "method", "GET"))
	url := strOr(cfg, "url", "")
	if url == "" {
		return nil, fmt.Errorf("http node requires a non-empty url")
	}

	timeoutSec, err := intVal(cfg, "timeout", 30)
	if err != nil {
		return nil, err
	}
	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	body := strOr(cfg, "body", "")
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}

	req.Header.Set("User-Agent", "FlowGo/1.0")
	for k, v := range strMap(cfg, "headers") {
		req.Header.Set(k, v)
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyLog))
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	headers := map[string]any{}
	for k, v := range resp.Header {
		headers[k] = v[0]
	}

	out := map[string]any{
		"status_code": resp.StatusCode,
		"body":        string(raw),
		"headers":     headers,
		"duration_ms": elapsed,
	}

	// 状态码 >= 400 视为节点失败，中断后续流程。
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("http node got unexpected status %d: %s", resp.StatusCode, truncate(string(raw), 512))
	}
	return out, nil
}

// strOr 读取字符串配置项的简写。
func strOr(cfg map[string]any, key, def string) string { return str(cfg, key, def) }
