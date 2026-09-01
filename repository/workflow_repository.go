// Package repository 定义数据访问层，封装工作流与运行记录的数据库操作。
package repository

import (
	"errors"

	"github.com/example/flowgo/model"
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
	return r.db.Create(wf).Error
}

// GetByID 按主键查询工作流，未找到返回 nil, nil。
func (r *workflowRepository) GetByID(id uint) (*model.Workflow, error) {
	var wf model.Workflow
	err := r.db.First(&wf, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wf, nil
}

// GetByWebhookKey 按 Webhook 密钥查询工作流，未找到返回 nil, nil。
func (r *workflowRepository) GetByWebhookKey(key string) (*model.Workflow, error) {
	var wf model.Workflow
	err := r.db.Where("webhook_key = ?", key).First(&wf).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wf, nil
}

// List 查询全部工作流，按更新时间倒序。
func (r *workflowRepository) List() ([]model.Workflow, error) {
	var list []model.Workflow
	err := r.db.Order("updated_at DESC").Find(&list).Error
	return list, err
}

// ListEnabled 查询全部启用中的工作流。
func (r *workflowRepository) ListEnabled() ([]model.Workflow, error) {
	var list []model.Workflow
	err := r.db.Where("enabled = ?", true).Find(&list).Error
	return list, err
}

// Update 保存工作流变更。
func (r *workflowRepository) Update(wf *model.Workflow) error {
	return r.db.Save(wf).Error
}

// Delete 删除指定工作流。
func (r *workflowRepository) Delete(id uint) error {
	return r.db.Delete(&model.Workflow{}, id).Error
}
