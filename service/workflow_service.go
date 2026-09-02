// Package service 定义业务逻辑层，编排仓储、引擎与调度器。
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/example/flowgo/engine"
	"github.com/example/flowgo/model"
	"github.com/example/flowgo/node"
	"github.com/example/flowgo/repository"
)

// ErrWorkflowNotFound 工作流不存在。
var ErrWorkflowNotFound = errors.New("workflow not found")

// WorkflowService 工作流业务逻辑接口。
type WorkflowService interface {
	List() ([]model.Workflow, error)
	Get(id uint) (*model.Workflow, error)
	Create(wf *model.Workflow) error
	Update(wf *model.Workflow) error
	Delete(id uint) error
	Trigger(ctx context.Context, id uint, trigger, payload string) (*model.Run, error)
	TriggerByWebhook(ctx context.Context, key, payload string) (*model.Run, error)
	NodeTypes() []string
	AgentTools() []string
}

// workflowService WorkflowService 的默认实现。
type workflowService struct {
	repo   repository.WorkflowRepository
	engine *engine.Engine
}

// NewWorkflowService 创建工作流业务逻辑实例。
func NewWorkflowService(repo repository.WorkflowRepository, eng *engine.Engine) WorkflowService {
	return &workflowService{repo: repo, engine: eng}
}

// List 查询全部工作流。
func (s *workflowService) List() ([]model.Workflow, error) {
	return s.repo.List()
}

// Get 按 ID 查询工作流，不存在返回 ErrWorkflowNotFound。
func (s *workflowService) Get(id uint) (*model.Workflow, error) {
	wf, err := s.repo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, ErrWorkflowNotFound
	}
	return wf, nil
}

// Create 校验并新建工作流，自动补全 Webhook 密钥。
func (s *workflowService) Create(wf *model.Workflow) error {
	if err := s.validate(wf); err != nil {
		return err
	}
	if wf.WebhookKey == "" {
		wf.WebhookKey = generateKey()
	}
	return s.repo.Create(wf)
}

// Update 校验并保存工作流变更，缺失的 Webhook 密钥自动补全。
func (s *workflowService) Update(wf *model.Workflow) error {
	existing, err := s.repo.GetByID(wf.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrWorkflowNotFound
	}
	if err := s.validate(wf); err != nil {
		return err
	}
	if wf.WebhookKey == "" {
		wf.WebhookKey = generateKey()
	}
	return s.repo.Update(wf)
}

// Delete 删除工作流。
func (s *workflowService) Delete(id uint) error {
	existing, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrWorkflowNotFound
	}
	return s.repo.Delete(id)
}

// Trigger 按 ID 触发一次执行，忽略工作流的启用状态（手动运行始终可用）。
func (s *workflowService) Trigger(ctx context.Context, id uint, trigger, payload string) (*model.Run, error) {
	wf, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	return s.engine.Execute(ctx, wf, trigger, payload)
}

// TriggerByWebhook 按 Webhook 密钥触发执行，仅启用中的工作流可被触发。
func (s *workflowService) TriggerByWebhook(ctx context.Context, key, payload string) (*model.Run, error) {
	wf, err := s.repo.GetByWebhookKey(key)
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, ErrWorkflowNotFound
	}
	if !wf.Enabled {
		return nil, errors.New("workflow is disabled")
	}
	return s.engine.Execute(ctx, wf, model.TriggerWebhook, payload)
}

// NodeTypes 返回支持的节点类型列表。
func (s *workflowService) NodeTypes() []string { return node.Types() }

// AgentTools 返回 ai_agent 节点内部可调用的工具名称列表。
func (s *workflowService) AgentTools() []string { return node.ToolNames() }

// validate 校验工作流名称与图结构的合法性。
func (s *workflowService) validate(wf *model.Workflow) error {
	wf.Name = strings.TrimSpace(wf.Name)
	if wf.Name == "" {
		return errors.New("workflow name is required")
	}
	if len(wf.Name) > 128 {
		return errors.New("workflow name must not exceed 128 characters")
	}

	graph, err := wf.ParseGraph()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, n := range graph.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return errors.New("every node requires an id")
		}
		if seen[n.ID] {
			return fmt.Errorf("duplicated node id %q", n.ID)
		}
		seen[n.ID] = true

		if strings.TrimSpace(n.Type) == "" {
			return fmt.Errorf("node %q requires a type", n.ID)
		}
		if node.Get(n.Type) == nil {
			return fmt.Errorf("unsupported node type %q on node %q", n.Type, n.ID)
		}
		if n.Name == "" {
			n.Name = n.Type
		}

		if n.Type == node.TypeAIAgent {
			if err := node.ValidateAgentTools(n.Config); err != nil {
				return fmt.Errorf("node %q: %w", n.ID, err)
			}
		}
	}
	for _, e := range graph.Edges {
		if !seen[e.Source] || !seen[e.Target] {
			return fmt.Errorf("edge references unknown node: %s -> %s", e.Source, e.Target)
		}
	}

	// 保存阶段即检测环路，避免运行时才暴露不可执行的图。
	if _, err := engine.SortNodes(graph); err != nil {
		return err
	}
	return nil
}

// generateKey 生成随机的 Webhook 密钥。
func generateKey() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("wf%d", timeNowNano())
	}
	return hex.EncodeToString(buf)
}
