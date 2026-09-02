package node

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// 内置工具名称，ai_agent 节点内部只能调用这些已注册的工具。
const (
	ToolHTTPCall  = "http-call"
	ToolShellRun  = "shell-run"
	ToolDelayWait = "delay-sleep"
)

// 工具参数的安全上限，避免 AI 拼出超长调用把节点拖死。
const (
	maxToolHTTPTimeout  = 60
	maxToolShellTimeout = 120
	maxToolDelaySeconds = 60
)

// InnerTool 是 ai_agent 节点内部可调用的工具实现。
// args 为大模型给出的参数对象，返回供大模型阅读的字符串结果。
type InnerTool func(ctx context.Context, args map[string]any) (string, error)

// ToolSpec 工具描述，用于生成给大模型看的工具清单与 JSON Schema。
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// Tool 一个注册在节点内部工具集中的可执行工具。
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Fn          InnerTool
}

// ToolRegistry 节点内部工具表，AI 只能调用表中已注册的工具。
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

// NewToolRegistry 创建一个空工具表。
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]*Tool{}}
}

var defaultToolRegistry = NewToolRegistry()

// DefaultToolRegistry 返回全局默认工具表，内置工具注册在其中。
func DefaultToolRegistry() *ToolRegistry { return defaultToolRegistry }

// RegisterTool 向全局默认工具表注册一个工具，同名会覆盖。
func RegisterTool(t *Tool) { defaultToolRegistry.Register(t) }

// ToolNames 返回全局默认工具表中全部工具名，按字典序排列。
func ToolNames() []string { return defaultToolRegistry.Names() }

// Register 注册一个工具，名称为空或缺少实现时忽略。
func (r *ToolRegistry) Register(t *Tool) {
	if t == nil || t.Name == "" || t.Fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
}

// Get 按名称获取工具，未注册返回 nil。
func (r *ToolRegistry) Get(name string) *Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// Names 返回全部工具名，按字典序排列。
func (r *ToolRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for k := range r.tools {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Specs 返回全部工具描述，顺序与 Names 一致。
func (r *ToolRegistry) Specs() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for k := range r.tools {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]ToolSpec, 0, len(names))
	for _, n := range names {
		out = append(out, ToolSpec{
			Name:        r.tools[n].Name,
			Description: r.tools[n].Description,
			Parameters:  r.tools[n].Parameters,
		})
	}
	return out
}

// Filter 返回只包含指定名称的新工具表；names 为空时返回全部工具的副本。
// 用于按节点配置限制 AI 可调用的工具范围。
func (r *ToolRegistry) Filter(names []string) *ToolRegistry {
	out := NewToolRegistry()
	if len(names) == 0 {
		r.mu.RLock()
		defer r.mu.RUnlock()
		for _, t := range r.tools {
			out.tools[t.Name] = t
		}
		return out
	}
	for _, n := range names {
		if t := r.Get(n); t != nil {
			out.tools[t.Name] = t
		}
	}
	return out
}

// Call 执行指定工具，工具不存在时返回错误。
func (r *ToolRegistry) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	t := r.Get(name)
	if t == nil {
		return "", fmt.Errorf("tool %q is not registered", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return t.Fn(ctx, args)
}

func init() {
	RegisterTool(&Tool{
		Name:        ToolHTTPCall,
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

	RegisterTool(&Tool{
		Name:        ToolShellRun,
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

	RegisterTool(&Tool{
		Name:        ToolDelayWait,
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
		"timeout": clampInt(args["timeout"], 30, maxToolHTTPTimeout, 1),
	}
	out, err := (&HTTPExecutor{}).Run(ctx, cfg, &Context{})
	return formatToolResult(out, err)
}

// toolShellRun 复用 ShellExecutor 的能力，返回退出码与输出。
func toolShellRun(ctx context.Context, args map[string]any) (string, error) {
	cfg := map[string]any{
		"command": toolStr(args, "command", ""),
		"timeout": clampInt(args["timeout"], 30, maxToolShellTimeout, 1),
	}
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
	if seconds > maxToolDelaySeconds {
		seconds = maxToolDelaySeconds
	}
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
