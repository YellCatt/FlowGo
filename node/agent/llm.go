// Package agent 提供 ai_agent 节点所需的纯 AI 能力：
// 大模型 HTTP 客户端（OpenAI 兼容）、工具注册表框架、以及「思考-工具-观察」循环。
// 本包不依赖任何具体节点执行器，可被 node 包单向引用。
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/example/flowgo/logger"

	"go.uber.org/zap"
)

// maxLLMErrorBody 大模型返回错误时，写入错误信息与日志的响应体最大长度。
const maxLLMErrorBody = 1024

// chatCompletionsPath OpenAI 兼容接口的会话补全路径。
const chatCompletionsPath = "/chat/completions"

// llmMessage 一条对话消息，兼容 OpenAI 的 tool_calls 协议。
type llmMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []llmToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

// llmToolCall 大模型返回的一次工具调用。
type llmToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// llmToolDef 下发给大模型的工具定义（function calling 协议）。
type llmToolDef struct {
	Type     string   `json:"type"`
	Function ToolSpec `json:"function"`
}

// llmRequest 会话补全请求体。
type llmRequest struct {
	Model       string       `json:"model"`
	Messages    []llmMessage `json:"messages"`
	Temperature float64      `json:"temperature,omitempty"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Stream      bool         `json:"stream"`
	Tools       []llmToolDef `json:"tools,omitempty"`
	ToolChoice  any          `json:"tool_choice,omitempty"`
}

// llmResponse 会话补全响应体。
type llmResponse struct {
	Choices []struct {
		Message      llmMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

// llmClient 调用 OpenAI 兼容的大模型接口。
type llmClient struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// newLLMClient 创建大模型客户端，timeout 为单次请求超时。
func newLLMClient(baseURL, apiKey, model string, timeout time.Duration) *llmClient {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &llmClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{Timeout: timeout},
	}
}

// chat 发起一次会话补全，返回原始响应。
// 流程：补全 model（缺省用客户端默认）→ 序列化请求体 → 带鉴权头发起 POST →
// 读取并解析响应 → 处理业务错误、HTTP 错误码与空 choices 三种异常。
func (c *llmClient) chat(ctx context.Context, req llmRequest) (*llmResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	req.Stream = false

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to encode llm request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to build llm request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	start := time.Now()
	logger.Debug("正在向大模型发起 HTTP 请求",
		zap.String("模型", req.Model),
		zap.String("接口地址", c.endpoint()),
		zap.Int("消息条数", len(req.Messages)),
	)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		logger.Error("大模型 HTTP 请求失败",
			zap.String("模型", req.Model),
			zap.String("接口地址", c.endpoint()),
			zap.Error(err),
		)
		return nil, fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("failed to read llm response: %w", err)
	}

	var out llmResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("failed to decode llm response (%d ms): %s", elapsed, truncate(string(raw), maxLLMErrorBody))
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, fmt.Errorf("llm returned error: %s", out.Error.Message)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Error("大模型返回非成功状态码",
			zap.String("模型", req.Model),
			zap.Int("状态码", resp.StatusCode),
		)
		return nil, fmt.Errorf("llm returned http %d: %s", resp.StatusCode, truncate(string(raw), maxLLMErrorBody))
	}
	if len(out.Choices) == 0 {
		logger.Error("大模型返回结果为空（无 choices）",
			zap.String("模型", req.Model),
		)
		return nil, fmt.Errorf("llm returned no choices: %s", truncate(string(raw), maxLLMErrorBody))
	}

	logger.Debug("大模型调用完成",
		zap.String("模型", req.Model),
		zap.Int64("耗时_毫秒", elapsed),
		zap.Int("输入令牌", out.Usage.PromptTokens),
		zap.Int("输出令牌", out.Usage.CompletionTokens),
	)
	return &out, nil
}

// endpoint 计算补全接口地址，允许 base_url 直接指到 /chat/completions。
func (c *llmClient) endpoint() string {
	if strings.HasSuffix(c.baseURL, chatCompletionsPath) {
		return c.baseURL
	}
	return c.baseURL + chatCompletionsPath
}
