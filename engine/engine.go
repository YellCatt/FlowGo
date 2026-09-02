// Package engine 实现工作流的编排执行：拓扑排序、节点调度与日志落库。
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/example/flowgo/config"
	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/model"
	"github.com/example/flowgo/node"
	"github.com/example/flowgo/repository"

	"go.uber.org/zap"
)

// maskedValue 写入执行日志时替换敏感配置的占位符。
const maskedValue = "***"

// Engine 工作流执行引擎。
type Engine struct {
	runs repository.RunRepository

	// maxRunDuration 单次运行的最长持续时间，超时后整体中止，由 config.run.max_duration_min 提供。
	maxRunDuration time.Duration
}

// NewEngine 创建执行引擎实例，运行超时上限取自配置。
func NewEngine(runs repository.RunRepository) *Engine {
	return &Engine{runs: runs, maxRunDuration: config.GetMaxRunDuration()}
}

// Execute 同步触发一次工作流执行，直到全部节点结束后才返回：
// 1. 落库一条 running 状态的运行记录；
// 2. 对图做拓扑排序，按序串行执行节点；
// 3. 每个节点的输入、输出、耗时写入 step_logs；
// 4. 任一节点失败即终止，运行记录标记 failed。
func (e *Engine) Execute(ctx context.Context, wf *model.Workflow, trigger, payload string) (*model.Run, error) {
	run, err := e.createRun(wf, trigger, payload, model.RunStatusRunning)
	if err != nil {
		return nil, err
	}
	return run, e.runWorkflow(ctx, run, wf, trigger, payload)
}

// ExecuteAsync 异步触发一次工作流执行：
// 先落库一条 pending 运行记录并立即返回，节点执行交由后台 goroutine 完成，
// 调用方无需等待长耗时节点（如 ai_agent），可凭 run.ID 轮询进度。
func (e *Engine) ExecuteAsync(wf *model.Workflow, trigger, payload string) (*model.Run, error) {
	run, err := e.createRun(wf, trigger, payload, model.RunStatusPending)
	if err != nil {
		return nil, err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic in async run",
					zap.Uint("run_id", run.ID), zap.Any("recover", r))
				e.finishRun(run, model.RunStatusFailed, fmt.Sprintf("panic: %v", r))
			}
		}()
		// 后台执行不复用请求上下文，避免 HTTP 响应返回后运行被取消。
		if err := e.runWorkflow(context.Background(), run, wf, trigger, payload); err != nil {
			logger.Warn("async run failed", zap.Uint("run_id", run.ID), zap.Error(err))
		}
	}()

	return run, nil
}

// createRun 落库一条运行记录，status 为 running 时同时写入开始时间。
func (e *Engine) createRun(wf *model.Workflow, trigger, payload, status string) (*model.Run, error) {
	run := &model.Run{
		WorkflowID: wf.ID,
		Workflow:   wf.Name,
		Status:     status,
		Trigger:    trigger,
		Payload:    payload,
	}
	if status == model.RunStatusRunning {
		now := model.Now()
		run.StartedAt = &now
	}
	if err := e.runs.Create(run); err != nil {
		return nil, fmt.Errorf("failed to create run: %w", err)
	}
	return run, nil
}

// markRunning 将 pending 的运行记录置为 running，并补写开始时间。
func (e *Engine) markRunning(run *model.Run) {
	if run.StartedAt == nil {
		now := model.Now()
		run.StartedAt = &now
	}
	if run.Status == model.RunStatusRunning {
		return
	}
	run.Status = model.RunStatusRunning
	if err := e.runs.Update(run); err != nil {
		logger.Error("failed to mark run as running", zap.Uint("run_id", run.ID), zap.Error(err))
	}
}

// runWorkflow 执行已落库的运行记录：拓扑排序后串行执行节点并写入节点日志，
// 任一节点失败即终止，运行记录标记 failed。
func (e *Engine) runWorkflow(ctx context.Context, run *model.Run, wf *model.Workflow, trigger, payload string) error {
	ctx, cancel := context.WithTimeout(ctx, e.maxRunDuration)
	defer cancel()

	e.markRunning(run)

	graph, err := wf.ParseGraph()
	if err != nil {
		e.finishRun(run, model.RunStatusFailed, err.Error())
		return err
	}

	order, err := topoSort(graph)
	if err != nil {
		e.finishRun(run, model.RunStatusFailed, err.Error())
		return err
	}
	if len(order) == 0 {
		e.finishRun(run, model.RunStatusSuccess, "")
		return nil
	}

	nodeByID := map[string]*model.NodeDef{}
	for i := range graph.Nodes {
		nodeByID[graph.Nodes[i].ID] = &graph.Nodes[i]
	}

	// 直接上游节点，供需要读取前置产出的节点（如 ai_agent）使用。
	preds := map[string][]string{}
	for _, e := range graph.Edges {
		preds[e.Target] = append(preds[e.Target], e.Source)
	}

	vars := map[string]any{
		"trigger": decodePayload(payload),
		"nodes":   map[string]any{},
		"workflow": map[string]any{
			"id":   wf.ID,
			"name": wf.Name,
		},
		"run": map[string]any{
			"id":      run.ID,
			"trigger": trigger,
		},
	}
	nodesOut := vars["nodes"].(map[string]any)

	logger.Info("run started",
		zap.Uint("run_id", run.ID),
		zap.Uint("workflow_id", wf.ID),
		zap.String("trigger", trigger),
		zap.Int("nodes", len(order)),
	)

	for _, id := range order {
		def := nodeByID[id]
		executor := node.Get(def.Type)
		if executor == nil {
			err := fmt.Errorf("unsupported node type %q on node %q", def.Type, id)
			e.logNodeFailure(run, def, nil, err)
			e.finishRun(run, model.RunStatusFailed, err.Error())
			return err
		}

		rendered, rerr := renderConfig(def.Config, vars)
		if rerr != nil {
			e.logNodeFailure(run, def, nil, rerr)
			e.finishRun(run, model.RunStatusFailed, rerr.Error())
			return rerr
		}

		started := model.Now()
		step := &model.StepLog{
			RunID:     run.ID,
			NodeID:    def.ID,
			NodeName:  def.Name,
			NodeType:  def.Type,
			Status:    model.RunStatusRunning,
			Input:     mustJSON(maskConfig(rendered, executor)),
			StartedAt: started,
		}
		if err := e.runs.CreateStep(step); err != nil {
			logger.Error("failed to create step log", zap.Error(err))
		}

		execStart := time.Now()
		out, execErr := executor.Run(ctx, rendered, &node.Context{
			Vars:     vars,
			NodeID:   def.ID,
			Upstream: preds[def.ID],
		})
		finished := model.Now()

		step.FinishedAt = finished
		step.Duration = time.Since(execStart).Milliseconds()

		if execErr != nil {
			step.Status = model.RunStatusFailed
			step.Error = execErr.Error()
			if out != nil {
				step.Output = mustJSON(out)
			}
			if err := e.runs.UpdateStep(step); err != nil {
				logger.Error("failed to update step log", zap.Error(err))
			}
			logger.Warn("node failed",
				zap.Uint("run_id", run.ID),
				zap.String("node", def.ID),
				zap.String("type", def.Type),
				zap.Error(execErr),
			)
			e.finishRun(run, model.RunStatusFailed,
				fmt.Sprintf("node %q (%s) failed: %v", def.Name, def.Type, execErr))
			return execErr
		}

		step.Status = model.RunStatusSuccess
		step.Output = mustJSON(out)
		if err := e.runs.UpdateStep(step); err != nil {
			logger.Error("failed to update step log", zap.Error(err))
		}

		nodesOut[def.ID] = out
		logger.Info("node finished",
			zap.Uint("run_id", run.ID),
			zap.String("node", def.ID),
			zap.String("type", def.Type),
			zap.Int64("duration_ms", step.Duration),
		)
	}

	e.finishRun(run, model.RunStatusSuccess, "")
	return nil
}

// finishRun 收尾运行记录，写入终态、结束时间与总耗时。
func (e *Engine) finishRun(run *model.Run, status, errMsg string) {
	now := model.Now()
	run.Status = status
	run.FinishedAt = &now
	run.Error = errMsg
	if run.StartedAt != nil {
		run.Duration = now.Sub(run.StartedAt.Time).Milliseconds()
	}
	if err := e.runs.Update(run); err != nil {
		logger.Error("failed to update run", zap.Uint("run_id", run.ID), zap.Error(err))
		return
	}
	logger.Info("run finished",
		zap.Uint("run_id", run.ID),
		zap.String("status", status),
		zap.Int64("duration_ms", run.Duration),
	)
}

// logNodeFailure 在节点执行前（如配置渲染失败）就发生错误时补记一条失败日志。
func (e *Engine) logNodeFailure(run *model.Run, def *model.NodeDef, cfg map[string]any, err error) {
	now := model.Now()
	step := &model.StepLog{
		RunID:      run.ID,
		NodeID:     def.ID,
		NodeName:   def.Name,
		NodeType:   def.Type,
		Status:     model.RunStatusFailed,
		Input:      mustJSON(cfg),
		Error:      err.Error(),
		StartedAt:  now,
		FinishedAt: now,
	}
	if cerr := e.runs.CreateStep(step); cerr != nil {
		logger.Error("failed to create failure step log", zap.Error(cerr))
	}
}

// SortNodes 对图做拓扑排序，返回节点执行顺序。
// 供执行引擎与保存校验复用，存在环时返回错误。
func SortNodes(g *model.Graph) ([]string, error) { return topoSort(g) }

// topoSort 对图做拓扑排序，返回节点执行顺序。
// 图中无边时按节点声明顺序执行，存在环则返回错误。
func topoSort(g *model.Graph) ([]string, error) {
	ids := make([]string, 0, len(g.Nodes))
	nodeSet := map[string]bool{}
	for _, n := range g.Nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("found a node without id")
		}
		if nodeSet[n.ID] {
			return nil, fmt.Errorf("duplicated node id %q", n.ID)
		}
		nodeSet[n.ID] = true
		ids = append(ids, n.ID)
	}

	// 无边时退化为声明顺序，便于只配置节点列表的简单流程。
	if len(g.Edges) == 0 {
		return ids, nil
	}

	indegree := map[string]int{}
	adj := map[string][]string{}
	for _, id := range ids {
		indegree[id] = 0
	}

	for _, e := range g.Edges {
		if !nodeSet[e.Source] || !nodeSet[e.Target] {
			return nil, fmt.Errorf("edge references unknown node: %s -> %s", e.Source, e.Target)
		}
		adj[e.Source] = append(adj[e.Source], e.Target)
		indegree[e.Target]++
	}

	queue := make([]string, 0, len(ids))
	for _, id := range ids {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}

	order := make([]string, 0, len(ids))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, next := range adj[cur] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(order) != len(ids) {
		return nil, fmt.Errorf("workflow graph contains a cycle, %d of %d nodes are unreachable",
			len(ids)-len(order), len(ids))
	}
	return order, nil
}

// maskConfig 按节点声明脱敏配置字段（如 api_key），避免密钥写入执行日志。
func maskConfig(cfg map[string]any, executor node.Executor) map[string]any {
	masker, ok := executor.(node.ConfigMasker)
	if !ok || len(cfg) == 0 {
		return cfg
	}
	fields := masker.MaskedFields()
	if len(fields) == 0 {
		return cfg
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range fields {
		if s, ok := out[f].(string); ok && s != "" {
			out[f] = maskedValue
		}
	}
	return out
}

// mustJSON 序列化配置或输出用于日志存储，失败时退化为字符串形式。
func mustJSON(v any) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}
