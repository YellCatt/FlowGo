// Package scheduler 基于 Cron 表达式定时触发工作流。
package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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

	// running 记录正在执行的分组键，用于保证同一分组同时只有一个运行实例：
	// 同一 group 下的工作流串行执行，不同 group 之间并行执行；
	// 未设置 group 的工作流以自身 ID 作为分组键，即各自独立、互不阻塞。
	// 上一次触发尚未结束时，新的 cron 触发会被跳过，避免重复产生副作用（如重复发邮件、重复写库）。
	running sync.Map
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
	// 启动前先把数据库中启用的工作流注册为 cron 任务。
	if err := s.Reload(); err != nil {
		return err
	}
	s.cron.Start()
	logger.Info("定时调度器已启动")
	return nil
}

// Stop 停止调度循环，返回的 Context 可用于等待进行中的任务结束。
func (s *Scheduler) Stop() context.Context {
	ctx := s.cron.Stop()
	logger.Info("定时调度器已停止")
	return ctx
}

// Reload 重新读取数据库中的工作流，刷新全部定时任务。
// 工作流新增、修改或删除后调用即可生效，无需重启进程。
func (s *Scheduler) Reload() error {
	// 加锁保护 entries，避免与触发回调并发修改。
	s.mu.Lock()
	defer s.mu.Unlock()

	// 先清空旧任务，再根据当前数据库状态全量重建，保证与配置一致。
	for _, id := range s.entries {
		s.cron.Remove(id)
	}
	s.entries = map[uint]cron.EntryID{}

	// 只加载启用中的工作流，停用的工作流不参与调度。
	workflows, err := s.repo.ListEnabled()
	if err != nil {
		return fmt.Errorf("failed to load workflows for scheduling: %w", err)
	}

	logger.Debug("开始重新加载定时任务",
		zap.Int("启用中的工作流数", len(workflows)))

	for _, wf := range workflows {
		if wf.Cron == "" {
			logger.Debug("工作流未配置 cron，跳过调度",
				zap.Uint("工作流ID", wf.ID),
				zap.String("工作流名称", wf.Name),
			)
			continue
		}
		wf := wf
		entryID, err := s.cron.AddFunc(wf.Cron, func() {
			s.runWorkflow(wf.ID)
		})
		if err != nil {
			logger.Error("cron 表达式非法，该工作流未加入调度",
				zap.Uint("工作流ID", wf.ID),
				zap.String("工作流名称", wf.Name),
				zap.String("cron表达式", wf.Cron),
				zap.Error(err),
			)
			continue
		}
		s.entries[wf.ID] = entryID
		logger.Info("已注册定时任务",
			zap.Uint("工作流ID", wf.ID),
			zap.String("工作流名称", wf.Name),
			zap.String("cron表达式", wf.Cron),
		)
	}

	logger.Info("定时任务重新加载完成", zap.Int("任务数量", len(s.entries)))
	return nil
}

// runWorkflow 执行一次定时触发：查工作流、校验启用状态，再异步执行工作流本身。
// 失败或 panic 仅记录日志，不影响调度循环与后续触发。
func (s *Scheduler) runWorkflow(id uint) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("定时任务调度过程发生 panic",
				zap.Uint("工作流ID", id), zap.Any("异常信息", r))
		}
	}()

	wf, err := s.repo.GetByID(id)
	if err != nil || wf == nil {
		logger.Warn("定时任务触发的工作流不存在", zap.Uint("工作流ID", id))
		return
	}
	if !wf.Enabled {
		logger.Debug("工作流已停用，跳过本次定时触发",
			zap.Uint("工作流ID", id),
			zap.String("工作流名称", wf.Name),
		)
		return
	}

	// 同分组串行：以分组键在 running 中占位，已存在说明该分组上一次运行仍在执行，本次直接跳过，
	// 避免重复产生副作用（如重复发邮件、重复写库）。
	// 注意 LoadOrStore 必须在启动 goroutine 之前同步完成，覆盖「查询后到协程真正执行」之间的时间窗口。
	key := s.groupKey(wf)
	if _, loaded := s.running.LoadOrStore(key, struct{}{}); loaded {
		logger.Warn("同分组的上一次运行尚未结束，本次定时触发已跳过（分组内串行，避免重复执行副作用）",
			zap.Uint("工作流ID", id),
			zap.String("工作流名称", wf.Name),
			zap.String("分组", wf.Group),
		)
		return
	}

	logger.Info("定时任务触发工作流",
		zap.Uint("工作流ID", id),
		zap.String("工作流名称", wf.Name),
		zap.String("分组", wf.Group),
	)

	// 异步执行，避免长时间任务阻塞调度循环。
	go func() {
		// 运行结束（含 panic）立即清除占位，允许同分组下一次触发正常进入。
		defer s.running.Delete(key)
		defer func() {
			if r := recover(); r != nil {
				logger.Error("定时运行过程发生 panic",
					zap.Uint("工作流ID", id), zap.Any("异常信息", r))
			}
		}()
		run, err := s.engine.Execute(context.Background(), wf, model.TriggerCron, "")
		if err != nil {
			logger.Error("定时运行失败",
				zap.Uint("工作流ID", id),
				zap.String("工作流名称", wf.Name),
				zap.Error(err),
			)
			return
		}
		logger.Debug("定时运行已生成运行记录，前端轮询时可自动发现",
			zap.Uint("运行ID", run.ID),
			zap.Uint("工作流ID", id),
		)
	}()
}

// Entries 返回当前已注册的定时任务数量，便于健康检查与前端展示。
func (s *Scheduler) Entries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// groupKey 计算工作流在调度互斥中使用的分组键。
// 设置了 group 的工作流使用 "grp:<group>"，使同一分组下的工作流串行执行；
// 未设置 group 的工作流使用 "wf:<id>"，退化为「各自独立、互不阻塞」，保持此前 per-workflow 行为。
func (s *Scheduler) groupKey(wf *model.Workflow) string {
	if g := strings.TrimSpace(wf.Group); g != "" {
		return "grp:" + g
	}
	return "wf:" + strconv.FormatUint(uint64(wf.ID), 10)
}
