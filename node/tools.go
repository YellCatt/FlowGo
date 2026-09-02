package node

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/node/agent"

	"go.uber.org/zap"
)

// 本文件把内置节点桥接为 ai_agent 可用的工具，并在包初始化时注册进全局工具表。
// 工具实现复用具体节点执行器的能力，因此必须留在 node 包（agent 包不依赖具体节点）。

func init() {
	agent.RegisterTool(&agent.Tool{
		Name:        agent.ToolHTTPCall,
		Description: "发起一次 HTTP 请求，返回状态码与响应体。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"method":  map[string]any{"type": "string", "description": "请求方法，默认 GET"},
				"url":     map[string]any{"type": "string", "description": "完整 URL，必填"},
				"body":    map[string]any{"type": "string", "description": "请求体，可选"},
				"headers": map[string]any{"type": "object", "description": "请求头对象，可选"},
				"timeout": map[string]any{"type": "integer", "description": "超时秒数，默认 30，最大 60"},
			},
			"required": []string{"url"},
		},
		Fn: toolHTTPCall,
	})

	agent.RegisterTool(&agent.Tool{
		Name:        agent.ToolShellRun,
		Description: "执行一条 shell 命令，返回退出码、stdout 与 stderr。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "要执行的命令，必填"},
				"timeout": map[string]any{"type": "integer", "description": "超时秒数，默认 30，最大 120"},
			},
			"required": []string{"command"},
		},
		Fn: toolShellRun,
	})

	agent.RegisterTool(&agent.Tool{
		Name:        agent.ToolDelayWait,
		Description: "等待一段时间，用于限速或轮询间隔。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"seconds": map[string]any{"type": "number", "description": "等待秒数，可为小数"},
				"ms":      map[string]any{"type": "integer", "description": "等待毫秒数，填了 seconds 时忽略"},
			},
		},
		Fn: toolDelayWait,
	})
}

// toolHTTPCall 复用 HTTPExecutor 的能力，返回响应摘要。
func toolHTTPCall(ctx context.Context, args map[string]any) (string, error) {
	cfg := map[string]any{
		"method":  toolStr(args, "method", "GET"),
		"url":     toolStr(args, "url", ""),
		"body":    toolStr(args, "body", ""),
		"headers": args["headers"],
		"timeout": clampInt(args["timeout"], 30, agent.MaxToolHTTPTimeout, 1),
	}
	logger.Debug("AI 工具调用：http-call",
		zap.String("方法", fmt.Sprint(cfg["method"])),
		zap.String("URL", fmt.Sprint(cfg["url"])),
		zap.Int("超时_秒", cfg["timeout"].(int)),
	)
	out, err := (&HTTPExecutor{}).Run(ctx, cfg, &Context{})
	return formatToolResult(out, err)
}

// toolShellRun 复用 ShellExecutor 的能力，返回退出码与输出。
func toolShellRun(ctx context.Context, args map[string]any) (string, error) {
	cfg := map[string]any{
		"command": toolStr(args, "command", ""),
		"timeout": clampInt(args["timeout"], 30, agent.MaxToolShellTimeout, 1),
	}
	logger.Debug("AI 工具调用：shell-run",
		zap.String("命令", fmt.Sprint(cfg["command"])),
		zap.Int("超时_秒", cfg["timeout"].(int)),
	)
	out, err := (&ShellExecutor{}).Run(ctx, cfg, &Context{})
	return formatToolResult(out, err)
}

// toolDelayWait 复用 DelayExecutor 的能力，支持秒与毫秒两种写法。
func toolDelayWait(ctx context.Context, args map[string]any) (string, error) {
	seconds := toolFloat(args, "seconds", 0)
	if seconds <= 0 {
		if ms := toolFloat(args, "ms", 0); ms > 0 {
			seconds = ms / 1000
		}
	}
	if seconds > agent.MaxToolDelaySeconds {
		seconds = agent.MaxToolDelaySeconds
	}
	logger.Debug("AI 工具调用：delay-sleep", zap.Float64("请求等待_秒", seconds))
	out, err := (&DelayExecutor{}).Run(ctx, map[string]any{
		"seconds": int(seconds + 0.999), // 向上取整，DelayExecutor 只接受整数秒
	}, &Context{})
	if out == nil {
		out = map[string]any{"seconds": seconds}
	}
	out["requested_seconds"] = seconds
	return formatToolResult(out, err)
}

// formatToolResult 把节点输出序列化成给大模型阅读的文本。
func formatToolResult(out map[string]any, err error) (string, error) {
	if len(out) == 0 {
		if err != nil {
			return "", err
		}
		return "{}", nil
	}
	data, mErr := json.Marshal(out)
	if mErr != nil {
		return fmt.Sprint(out), err
	}
	return string(data), err
}

// toolStr 从工具参数中读取字符串。
func toolStr(args map[string]any, key, def string) string {
	if v, ok := args[key]; ok && v != nil {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
		return fmt.Sprint(v)
	}
	return def
}

// toolFloat 从工具参数中读取数字。
func toolFloat(args map[string]any, key string, def float64) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
			return f
		}
	}
	return def
}

// clampInt 读取整数参数并限制在 [min, max] 区间，非法值回落到 def。
func clampInt(v any, def, max, min int) int {
	n := def
	switch val := v.(type) {
	case float64:
		n = int(val)
	case float32:
		n = int(val)
	case int:
		n = val
	case int64:
		n = int(val)
	case string:
		if _, err := fmt.Sscanf(val, "%d", &n); err != nil {
			n = def
		}
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
