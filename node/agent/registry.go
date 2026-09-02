package agent

import (
	"context"
	"fmt"
	"sort"
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
