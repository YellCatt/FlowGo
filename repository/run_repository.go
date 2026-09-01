package repository

import (
	"errors"

	"github.com/example/flowgo/model"
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
	return r.db.Create(run).Error
}

// GetByID 按主键查询运行记录，未找到返回 nil, nil。
func (r *runRepository) GetByID(id uint) (*model.Run, error) {
	var run model.Run
	err := r.db.First(&run, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// List 查询运行记录，workflowID 为 0 表示不限，limit 为 0 时使用默认 50。
func (r *runRepository) List(workflowID uint, limit int) ([]model.Run, error) {
	if limit <= 0 {
		limit = 50
	}
	query := r.db.Order("id DESC").Limit(limit)
	if workflowID > 0 {
		query = query.Where("workflow_id = ?", workflowID)
	}
	var list []model.Run
	err := query.Find(&list).Error
	return list, err
}

// Update 保存运行记录变更。
func (r *runRepository) Update(run *model.Run) error {
	return r.db.Save(run).Error
}

// Delete 删除指定运行记录。
func (r *runRepository) Delete(id uint) error {
	return r.db.Delete(&model.Run{}, id).Error
}

// CreateStep 新增一条节点执行日志。
func (r *runRepository) CreateStep(log *model.StepLog) error {
	return r.db.Create(log).Error
}

// UpdateStep 保存节点执行日志变更。
func (r *runRepository) UpdateStep(log *model.StepLog) error {
	return r.db.Save(log).Error
}

// ListSteps 按执行顺序查询某次运行的全部节点日志。
func (r *runRepository) ListSteps(runID uint) ([]model.StepLog, error) {
	var list []model.StepLog
	err := r.db.Where("run_id = ?", runID).Order("id ASC").Find(&list).Error
	return list, err
}

// DeleteStepsByRun 删除某次运行关联的全部节点日志。
func (r *runRepository) DeleteStepsByRun(runID uint) error {
	return r.db.Where("run_id = ?", runID).Delete(&model.StepLog{}).Error
}

// CountSteps 统计某次运行的节点日志数量。
func (r *runRepository) CountSteps(runID uint) (int64, error) {
	var count int64
	err := r.db.Model(&model.StepLog{}).Where("run_id = ?", runID).Count(&count).Error
	return count, err
}
