// Package repository 定义数据访问层，封装工作流与运行记录的数据库操作。
package repository

import (
	"errors"

	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// WorkflowRepository 工作流数据访问接口。
type WorkflowRepository interface {
	Create(wf *model.Workflow) error
	GetByID(id uint) (*model.Workflow, error)
	GetByWebhookKey(key string) (*model.Workflow, error)
	List() ([]model.Workflow, error)
	ListEnabled() ([]model.Workflow, error)
	Update(wf *model.Workflow) error
	Delete(id uint) error
}

// workflowRepository 基于 GORM 的工作流数据访问实现。
type workflowRepository struct {
	db *gorm.DB
}

// NewWorkflowRepository 创建工作流数据访问实例。
func NewWorkflowRepository(db *gorm.DB) WorkflowRepository {
	return &workflowRepository{db: db}
}

// Create 新增一条工作流记录。
func (r *workflowRepository) Create(wf *model.Workflow) error {
	logger.Debug("仓储：新增工作流记录",
		zap.String("名称", wf.Name),
		zap.Bool("是否启用", wf.Enabled),
		zap.String("分组", wf.Group),
	)
	return r.db.Create(wf).Error
}

// GetByID 按主键查询工作流，未找到返回 nil, nil。
func (r *workflowRepository) GetByID(id uint) (*model.Workflow, error) {
	logger.Debug("仓储：按 ID 查询工作流", zap.Uint("工作流ID", id))
	var wf model.Workflow
	err := r.db.First(&wf, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		logger.Debug("仓储：工作流不存在", zap.Uint("工作流ID", id))
		return nil, nil
	}
	if err != nil {
		logger.Error("仓储：查询工作流失败", zap.Uint("工作流ID", id), zap.Error(err))
		return nil, err
	}
	logger.Debug("仓储：查询到工作流",
		zap.Uint("工作流ID", id),
		zap.String("名称", wf.Name),
		zap.Bool("是否启用", wf.Enabled),
	)
	return &wf, nil
}

// GetByWebhookKey 按 Webhook 密钥查询工作流，未找到返回 nil, nil。
func (r *workflowRepository) GetByWebhookKey(key string) (*model.Workflow, error) {
	logger.Debug("仓储：按 Webhook 密钥查询工作流", zap.String("密钥", key))
	var wf model.Workflow
	err := r.db.Where("webhook_key = ?", key).First(&wf).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		logger.Debug("仓储：Webhook 密钥未匹配到工作流", zap.String("密钥", key))
		return nil, nil
	}
	if err != nil {
		logger.Error("仓储：按 Webhook 密钥查询失败", zap.String("密钥", key), zap.Error(err))
		return nil, err
	}
	logger.Debug("仓储：匹配到工作流", zap.Uint("工作流ID", wf.ID), zap.String("名称", wf.Name))
	return &wf, nil
}

// List 查询全部工作流，按更新时间倒序。
func (r *workflowRepository) List() ([]model.Workflow, error) {
	logger.Debug("仓储：查询全部工作流列表")
	var list []model.Workflow
	err := r.db.Order("updated_at DESC").Find(&list).Error
	if err != nil {
		logger.Error("仓储：查询工作流列表失败", zap.Error(err))
		return nil, err
	}
	logger.Debug("仓储：查询到工作流列表", zap.Int("数量", len(list)))
	return list, err
}

// ListEnabled 查询全部启用中的工作流。
func (r *workflowRepository) ListEnabled() ([]model.Workflow, error) {
	logger.Debug("仓储：查询已启用工作流列表")
	var list []model.Workflow
	err := r.db.Where("enabled = ?", true).Find(&list).Error
	if err != nil {
		logger.Error("仓储：查询已启用工作流失败", zap.Error(err))
		return nil, err
	}
	logger.Debug("仓储：查询到已启用工作流", zap.Int("数量", len(list)))
	return list, err
}

// Update 保存工作流变更。
func (r *workflowRepository) Update(wf *model.Workflow) error {
	logger.Debug("仓储：更新工作流记录",
		zap.Uint("工作流ID", wf.ID),
		zap.String("名称", wf.Name),
		zap.Bool("是否启用", wf.Enabled),
	)
	return r.db.Save(wf).Error
}

// Delete 删除指定工作流。
func (r *workflowRepository) Delete(id uint) error {
	logger.Debug("仓储：删除工作流记录", zap.Uint("工作流ID", id))
	return r.db.Delete(&model.Workflow{}, id).Error
}
