// Package scheduler 基于 Cron 表达式定时触发工作流。
package scheduler

import (
	"context"
	"fmt"
	"sync"

	"github.com/example/flowgo/engine"
	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/model"
	"github.com/example/flowgo/repository"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

// Scheduler 定时调度器，按工作流的 Cron 配置注册并管理定时任务。
type Scheduler struct {
	cron   *cron.Cron
	repo   repository.WorkflowRepository
	engine *engine.Engine

	mu      sync.Mutex
	entries map[uint]cron.EntryID
}

// NewScheduler 创建调度器实例，使用带秒位的 Cron 解析器（支持 6 段表达式）。
func NewScheduler(repo repository.WorkflowRepository, eng *engine.Engine) *Scheduler {
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	return &Scheduler{
		cron:    cron.New(cron.WithParser(parser), cron.WithLogger(cron.DiscardLogger)),
		repo:    repo,
		engine:  eng,
		entries: map[uint]cron.EntryID{},
	}
}

// Start 加载全部定时任务并启动调度循环。
func (s *Scheduler) Start() error {
	if err := s.Reload(); err != nil {
		return err
	}
	s.cron.Start()
	logger.Info("scheduler started")
	return nil
}

// Stop 停止调度循环，返回的 Context 可用于等待进行中的任务结束。
func (s *Scheduler) Stop() context.Context {
	ctx := s.cron.Stop()
	logger.Info("scheduler stopped")
	return ctx
}

// Reload 重新读取数据库中的工作流，刷新全部定时任务。
// 工作流新增、修改或删除后调用即可生效，无需重启进程。
func (s *Scheduler) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range s.entries {
		s.cron.Remove(id)
	}
	s.entries = map[uint]cron.EntryID{}

	workflows, err := s.repo.ListEnabled()
	if err != nil {
		return fmt.Errorf("failed to load workflows for scheduling: %w", err)
	}

	for _, wf := range workflows {
		if wf.Cron == "" {
			continue
		}
		wf := wf
		entryID, err := s.cron.AddFunc(wf.Cron, func() {
			s.runWorkflow(wf.ID)
		})
		if err != nil {
			logger.Error("invalid cron expression",
				zap.Uint("workflow_id", wf.ID),
				zap.String("cron", wf.Cron),
				zap.Error(err),
			)
			continue
		}
		s.entries[wf.ID] = entryID
		logger.Info("scheduled workflow",
			zap.Uint("workflow_id", wf.ID),
			zap.String("name", wf.Name),
			zap.String("cron", wf.Cron),
		)
	}

	logger.Info("scheduler reloaded", zap.Int("jobs", len(s.entries)))
	return nil
}

// runWorkflow 执行一次定时触发，失败只记录日志不影响后续调度。
func (s *Scheduler) runWorkflow(id uint) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("panic while running scheduled workflow",
				zap.Uint("workflow_id", id), zap.Any("recover", r))
		}
	}()

	wf, err := s.repo.GetByID(id)
	if err != nil || wf == nil {
		logger.Warn("scheduled workflow not found", zap.Uint("workflow_id", id))
		return
	}
	if !wf.Enabled {
		return
	}

	// 异步执行，避免长时间任务阻塞调度循环。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic in scheduled run", zap.Uint("workflow_id", id), zap.Any("recover", r))
			}
		}()
		if _, err := s.engine.Execute(context.Background(), wf, model.TriggerCron, ""); err != nil {
			logger.Error("scheduled run failed",
				zap.Uint("workflow_id", id),
				zap.String("name", wf.Name),
				zap.Error(err),
			)
		}
	}()
}

// Entries 返回当前已注册的定时任务数量，便于健康检查与前端展示。
func (s *Scheduler) Entries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
