package service

import (
	"errors"

	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/model"
	"github.com/example/flowgo/repository"

	"go.uber.org/zap"
)

// ErrRunNotFound 运行记录不存在。
var ErrRunNotFound = errors.New("run not found")

// RunService 运行记录业务逻辑接口。
type RunService interface {
	List(workflowID uint, limit int) ([]model.Run, error)
	GetDetail(id uint) (*model.RunDetail, error)
	Delete(id uint) error
}

// runService RunService 的默认实现。
type runService struct {
	repo repository.RunRepository
}

// NewRunService 创建运行记录业务逻辑实例。
func NewRunService(repo repository.RunRepository) RunService {
	return &runService{repo: repo}
}

// List 查询运行记录，workflowID 为 0 表示不限工作流。
func (s *runService) List(workflowID uint, limit int) ([]model.Run, error) {
	logger.Debug("查询运行记录", zap.Uint("过滤工作流ID", workflowID), zap.Int("上限", limit))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	return s.repo.List(workflowID, limit)
}

// GetDetail 查询运行详情及其全部节点日志。
func (s *runService) GetDetail(id uint) (*model.RunDetail, error) {
	logger.Debug("查询运行详情", zap.Uint("运行ID", id))
	run, err := s.repo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrRunNotFound
	}
	steps, err := s.repo.ListSteps(id)
	if err != nil {
		return nil, err
	}
	return &model.RunDetail{Run: *run, Steps: steps}, nil
}

// Delete 删除运行记录及其节点日志。
func (s *runService) Delete(id uint) error {
	logger.Debug("删除运行记录", zap.Uint("运行ID", id))
	run, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}
	if run == nil {
		return ErrRunNotFound
	}
	if err := s.repo.DeleteStepsByRun(id); err != nil {
		return err
	}
	return s.repo.Delete(id)
}
