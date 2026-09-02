package node

import (
	"context"
	"fmt"
	"time"

	"github.com/example/flowgo/logger"

	"go.uber.org/zap"
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
		logger.Error("Delay 节点配置了负数秒数",
			zap.String("node", nodeIDOf(ec)),
			zap.Int("秒数", seconds),
		)
		return nil, fmt.Errorf("delay seconds must be >= 0, got %d", seconds)
	}
	if seconds == 0 {
		logger.Debug("Delay 节点配置为 0 秒，立即放行",
			zap.String("node", nodeIDOf(ec)),
		)
		return map[string]any{"seconds": 0, "slept_ms": 0}, nil
	}

	logger.Debug("Delay 节点开始等待",
		zap.String("node", nodeIDOf(ec)),
		zap.Int("秒数", seconds),
	)
	start := time.Now()
	select {
	case <-ctx.Done():
		logger.Warn("Delay 节点在等待期间被取消",
			zap.String("node", nodeIDOf(ec)),
			zap.Int("原计划秒数", seconds),
		)
		return nil, ctx.Err()
	case <-time.After(time.Duration(seconds) * time.Second):
	}

	logger.Debug("Delay 节点等待结束",
		zap.String("node", nodeIDOf(ec)),
		zap.Int("实际休眠_毫秒", int(time.Since(start).Milliseconds())),
	)
	return map[string]any{
		"seconds":   seconds,
		"slept_ms":  time.Since(start).Milliseconds(),
		"resume_at": time.Now().Format("2006-01-02 15:04:05"),
	}, nil
}
