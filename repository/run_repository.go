package repository

import (
	"errors"

	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// RunRepository 运行记录数据访问接口。
type RunRepository interface {
	Create(run *model.Run) error
	GetByID(id uint) (*model.Run, error)
	List(workflowID uint, limit int) ([]model.Run, error)
	Update(run *model.Run) error
	Delete(id uint) error
	CreateStep(log *model.StepLog) error
	UpdateStep(log *model.StepLog) error
	ListSteps(runID uint) ([]model.StepLog, error)
	DeleteStepsByRun(runID uint) error
	CountSteps(runID uint) (int64, error)
}

// runRepository 基于 GORM 的运行记录数据访问实现。
type runRepository struct {
	db *gorm.DB
}

// NewRunRepository 创建运行记录数据访问实例。
func NewRunRepository(db *gorm.DB) RunRepository {
	return &runRepository{db: db}
}

// Create 新增一条运行记录。
func (r *runRepository) Create(run *model.Run) error {
	logger.Debug("仓储：新增运行记录",
		zap.Uint("工作流ID", run.WorkflowID),
		zap.String("触发方式", run.Trigger),
		zap.String("初始状态", run.Status),
	)
	return r.db.Create(run).Error
}

// GetByID 按主键查询运行记录，未找到返回 nil, nil。
func (r *runRepository) GetByID(id uint) (*model.Run, error) {
	logger.Debug("仓储：按 ID 查询运行记录", zap.Uint("运行ID", id))
	var run model.Run
	err := r.db.First(&run, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		logger.Debug("仓储：运行记录不存在", zap.Uint("运行ID", id))
		return nil, nil
	}
	if err != nil {
		logger.Error("仓储：查询运行记录失败", zap.Uint("运行ID", id), zap.Error(err))
		return nil, err
	}
	return &run, nil
}

// List 查询运行记录，workflowID 为 0 表示不限，limit 为 0 时使用默认 50。
func (r *runRepository) List(workflowID uint, limit int) ([]model.Run, error) {
	if limit <= 0 {
		limit = 50
	}
	logger.Debug("仓储：查询运行记录列表",
		zap.Uint("过滤工作流ID", workflowID),
		zap.Int("上限", limit),
	)
	query := r.db.Order("id DESC").Limit(limit)
	if workflowID > 0 {
		query = query.Where("workflow_id = ?", workflowID)
	}
	var list []model.Run
	err := query.Find(&list).Error
	if err != nil {
		logger.Error("仓储：查询运行记录列表失败", zap.Error(err))
		return nil, err
	}
	logger.Debug("仓储：查询到运行记录", zap.Int("数量", len(list)))
	return list, err
}

// Update 保存运行记录变更。
func (r *runRepository) Update(run *model.Run) error {
	logger.Debug("仓储：更新运行记录",
		zap.Uint("运行ID", run.ID),
		zap.String("状态", run.Status),
	)
	return r.db.Save(run).Error
}

// Delete 删除指定运行记录。
func (r *runRepository) Delete(id uint) error {
	logger.Debug("仓储：删除运行记录", zap.Uint("运行ID", id))
	return r.db.Delete(&model.Run{}, id).Error
}

// CreateStep 新增一条节点执行日志。
func (r *runRepository) CreateStep(log *model.StepLog) error {
	logger.Debug("仓储：新增节点执行日志",
		zap.Uint("运行ID", log.RunID),
		zap.String("节点", log.NodeID),
		zap.String("节点类型", log.NodeType),
	)
	return r.db.Create(log).Error
}

// UpdateStep 保存节点执行日志变更。
func (r *runRepository) UpdateStep(log *model.StepLog) error {
	logger.Debug("仓储：更新节点执行日志",
		zap.Uint("日志ID", log.ID),
		zap.String("节点", log.NodeID),
		zap.String("状态", log.Status),
	)
	return r.db.Save(log).Error
}

// ListSteps 按执行顺序查询某次运行的全部节点日志。
func (r *runRepository) ListSteps(runID uint) ([]model.StepLog, error) {
	logger.Debug("仓储：查询节点执行日志列表", zap.Uint("运行ID", runID))
	var list []model.StepLog
	err := r.db.Where("run_id = ?", runID).Order("id ASC").Find(&list).Error
	if err != nil {
		logger.Error("仓储：查询节点执行日志失败", zap.Uint("运行ID", runID), zap.Error(err))
		return nil, err
	}
	logger.Debug("仓储：查询到节点执行日志", zap.Uint("运行ID", runID), zap.Int("数量", len(list)))
	return list, err
}

// DeleteStepsByRun 删除某次运行关联的全部节点日志。
func (r *runRepository) DeleteStepsByRun(runID uint) error {
	logger.Debug("仓储：删除运行关联的全部节点日志", zap.Uint("运行ID", runID))
	return r.db.Where("run_id = ?", runID).Delete(&model.StepLog{}).Error
}

// CountSteps 统计某次运行的节点日志数量。
func (r *runRepository) CountSteps(runID uint) (int64, error) {
	logger.Debug("仓储：统计节点执行日志数量", zap.Uint("运行ID", runID))
	var count int64
	err := r.db.Model(&model.StepLog{}).Where("run_id = ?", runID).Count(&count).Error
	if err != nil {
		logger.Error("仓储：统计节点执行日志失败", zap.Uint("运行ID", runID), zap.Error(err))
		return 0, err
	}
	logger.Debug("仓储：节点执行日志数量", zap.Uint("运行ID", runID), zap.Int64("数量", count))
	return count, err
}
