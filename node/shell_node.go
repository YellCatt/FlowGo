package node

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/example/flowgo/logger"

	"go.uber.org/zap"
)

// maxOutputLog stdout/stderr 写入日志的最大字节数。
const maxOutputLog = 8192

// ShellExecutor 执行系统命令的节点执行器。
type ShellExecutor struct{}

func init() { Register(&ShellExecutor{}) }

// Type 返回节点类型 shell。
func (e *ShellExecutor) Type() string { return TypeShell }

// Run 按配置执行 shell 命令，输出 exit_code、stdout 与 stderr。
func (e *ShellExecutor) Run(ctx context.Context, cfg map[string]any, ec *Context) (map[string]any, error) {
	command := str(cfg, "command", "")
	if strings.TrimSpace(command) == "" {
		logger.Error("Shell 节点缺少 command 配置，执行失败",
			zap.String("node", nodeIDOf(ec)))
		return nil, fmt.Errorf("shell node requires a non-empty command")
	}

	timeoutSec, err := intVal(cfg, "timeout", 60)
	if err != nil {
		return nil, err
	}
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}

	name, args := buildShellCommand(str(cfg, "shell", ""), command)
	cmd := exec.CommandContext(ctx, name, args...)
	if dir := str(cfg, "workdir", ""); dir != "" {
		cmd.Dir = dir
	}

	logger.Debug("Shell 节点开始执行命令",
		zap.String("node", nodeIDOf(ec)),
		zap.String("命令", command),
		zap.Int("超时_秒", timeoutSec),
		zap.String("工作目录", str(cfg, "workdir", "")),
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start).Milliseconds()

	out := map[string]any{
		"command":     command,
		"exit_code":   0,
		"stdout":      truncate(strings.TrimRight(stdout.String(), "\r\n"), maxOutputLog),
		"stderr":      truncate(strings.TrimRight(stderr.String(), "\r\n"), maxOutputLog),
		"duration_ms": elapsed,
	}

	if runErr != nil {
		code := -1
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		out["exit_code"] = code
		if ctx.Err() == context.DeadlineExceeded {
			logger.Warn("Shell 节点执行超时，已终止",
				zap.String("node", nodeIDOf(ec)),
				zap.String("命令", command),
				zap.Int("超时_秒", timeoutSec),
			)
			return out, fmt.Errorf("shell node timed out after %ds", timeoutSec)
		}
		if ctx.Err() != nil {
			logger.Warn("Shell 节点被上下文取消",
				zap.String("node", nodeIDOf(ec)),
				zap.String("命令", command),
			)
			return out, ctx.Err()
		}
		logger.Warn("Shell 节点执行失败",
			zap.String("node", nodeIDOf(ec)),
			zap.String("命令", command),
			zap.Int("退出码", code),
			zap.Int64("耗时_毫秒", elapsed),
		)
		return out, fmt.Errorf("shell node exited with code %d: %s", code, truncate(stderr.String(), 512))
	}

	logger.Debug("Shell 节点执行完成",
		zap.String("node", nodeIDOf(ec)),
		zap.String("命令", command),
		zap.Int64("耗时_毫秒", elapsed),
	)
	return out, nil
}

// buildShellCommand 根据平台与自定义 shell 组装命令参数。
// Windows 默认使用 PowerShell，其他平台默认使用 /bin/sh。
func buildShellCommand(customShell, command string) (string, []string) {
	if customShell != "" {
		parts := strings.Fields(customShell)
		if runtime.GOOS == "windows" {
			return parts[0], append(parts[1:], command)
		}
		return parts[0], append(parts[1:], "-c", command)
	}
	if runtime.GOOS == "windows" {
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-Command", command}
	}
	return "/bin/sh", []string{"-c", command}
}
