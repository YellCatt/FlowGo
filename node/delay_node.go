package node

import (
	"context"
	"fmt"
	"time"
)

// DelayExecutor 等待指定时长的节点执行器。
type DelayExecutor struct{}

func init() { Register(&DelayExecutor{}) }

// Type 返回节点类型 delay。
func (e *DelayExecutor) Type() string { return TypeDelay }

// Run 按 seconds 配置挂起等待，支持请求取消。
func (e *DelayExecutor) Run(ctx context.Context, cfg map[string]any, ec *Context) (map[string]any, error) {
	seconds, err := intVal(cfg, "seconds", 0)
	if err != nil {
		return nil, err
	}
	if seconds < 0 {
		return nil, fmt.Errorf("delay seconds must be >= 0, got %d", seconds)
	}
	if seconds == 0 {
		return map[string]any{"seconds": 0, "slept_ms": 0}, nil
	}

	start := time.Now()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(seconds) * time.Second):
	}

	return map[string]any{
		"seconds":   seconds,
		"slept_ms":  time.Since(start).Milliseconds(),
		"resume_at": time.Now().Format("2006-01-02 15:04:05"),
	}, nil
}
