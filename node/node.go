// Package node 定义内置节点执行器：http、shell、delay、ai_agent。
// ai_agent 所需的工具框架（LLM 客户端、工具注册表、循环逻辑）位于子包 node/agent，
// 本包仅在 node/tools.go 中把具体节点桥接为 agent 工具并注册。
package node

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/example/flowgo/logger"

	"go.uber.org/zap"
)

// 内置节点类型常量。
const (
	TypeHTTP    = "http"
	TypeShell   = "shell"
	TypeDelay   = "delay"
	TypeAIAgent = "ai_agent"
)

// Context 节点执行上下文，提供变量渲染能力。
type Context struct {
	// Vars 模板变量，结构为 {trigger: any, nodes: map[string]any}。
	Vars map[string]any
	// NodeID 当前节点 ID。
	NodeID string
	// Upstream 直接上游节点 ID 列表，由引擎根据连线计算；为空表示无上游。
	Upstream []string
}

// Executor 节点执行器接口，每种节点类型实现一次并注册。
type Executor interface {
	// Type 返回节点类型标识，需与配置中的 type 一致。
	Type() string
	// Run 执行节点，返回可被引用的输出变量。
	Run(ctx context.Context, cfg map[string]any, ec *Context) (map[string]any, error)
}

// ConfigMasker 可选接口：节点声明写入执行日志前需要脱敏的配置字段。
type ConfigMasker interface {
	// MaskedFields 返回需要脱敏的配置字段名，例如 api_key。
	MaskedFields() []string
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Executor{}
)

// Register 注册一个节点执行器，重复注册会覆盖同名类型。
func Register(e Executor) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[e.Type()] = e
	logger.Debug("注册节点执行器", zap.String("类型", e.Type()))
}

// Get 按类型获取已注册的执行器，未注册返回 nil。
func Get(t string) Executor {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[t]
}

// Types 返回全部已注册节点类型，按字典序排列。
func Types() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// str 从配置中读取字符串字段，缺失返回默认值。
func str(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key]; ok && v != nil {
		switch val := v.(type) {
		case string:
			if strings.TrimSpace(val) != "" {
				return val
			}
		default:
			return fmt.Sprint(val)
		}
	}
	return def
}

// intVal 从配置中读取整数字段，支持 JSON 数字与数字字符串。
func intVal(cfg map[string]any, key string, def int) (int, error) {
	v, ok := cfg[key]
	if !ok || v == nil {
		return def, nil
	}
	switch val := v.(type) {
	case float64:
		return int(val), nil
	case float32:
		return int(val), nil
	case int:
		return val, nil
	case int64:
		return int(val), nil
	case string:
		if strings.TrimSpace(val) == "" {
			return def, nil
		}
		var n int
		if _, err := fmt.Sscanf(val, "%d", &n); err != nil {
			return 0, fmt.Errorf("field %q expects an integer, got %q", key, val)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("field %q expects an integer, got %T", key, v)
	}
}

// strMap 从配置中读取字符串映射字段（如 HTTP 请求头）。
func strMap(cfg map[string]any, key string) map[string]string {
	out := map[string]string{}
	v, ok := cfg[key]
	if !ok || v == nil {
		return out
	}
	m, ok := v.(map[string]any)
	if !ok {
		return out
	}
	for k, val := range m {
		if val == nil {
			continue
		}
		out[k] = fmt.Sprint(val)
	}
	return out
}

// truncate 截断超长输出，避免日志撑爆数据库字段。
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("... (truncated, total %d bytes)", len(s))
}
