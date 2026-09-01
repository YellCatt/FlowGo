// Package engine 实现工作流的编排执行：拓扑排序、节点调度与日志落库。
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/model"
	"github.com/example/flowgo/node"
	"github.com/example/flowgo/repository"

	"go.uber.org/zap"
)

// maxRunDuration 单次运行的最长持续时间，超时后整体中止。
const maxRunDuration = 30 * time.Minute

// Engine 工作流执行引擎。
type Engine struct {
	runs repository.RunRepository
}

// NewEngine 创建执行引擎实例。
func NewEngine(runs repository.RunRepository) *Engine {
	return &Engine{runs: runs}
}

// Execute 触发一次工作流执行：
// 1. 落库一条 running 状态的运行记录；
// 2. 对图做拓扑排序，按序串行执行节点；
// 3. 每个节点的输入、输出、耗时写入 step_logs；
// 4. 任一节点失败即终止，运行记录标记 failed。
func (e *Engine) Execute(ctx context.Context, wf *model.Workflow, trigger, payload string) (*model.Run, error) {
	ctx, cancel := context.WithTimeout(ctx, maxRunDuration)
	defer cancel()

	now := model.Now()
	run := &model.Run{
		WorkflowID: wf.ID,
		Workflow:   wf.Name,
		Status:     model.RunStatusRunning,
		Trigger:    trigger,
		Payload:    payload,
		StartedAt:  &now,
	}
	if err := e.runs.Create(run); err != nil {
		return nil, fmt.Errorf("failed to create run: %w", err)
	}

	graph, err := wf.ParseGraph()
	if err != nil {
		e.finishRun(run, model.RunStatusFailed, err.Error())
		return run, err
	}

	order, err := topoSort(graph)
	if err != nil {
		e.finishRun(run, model.RunStatusFailed, err.Error())
		return run, err
	}
	if len(order) == 0 {
		e.finishRun(run, model.RunStatusSuccess, "")
		return run, nil
	}

	nodeByID := map[string]*model.NodeDef{}
	for i := range graph.Nodes {
		nodeByID[graph.Nodes[i].ID] = &graph.Nodes[i]
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
			return run, err
		}

		rendered, rerr := renderConfig(def.Config, vars)
		if rerr != nil {
			e.logNodeFailure(run, def, nil, rerr)
			e.finishRun(run, model.RunStatusFailed, rerr.Error())
			return run, rerr
		}

		started := model.Now()
		step := &model.StepLog{
			RunID:     run.ID,
			NodeID:    def.ID,
			NodeName:  def.Name,
			NodeType:  def.Type,
			Status:    model.RunStatusRunning,
			Input:     mustJSON(rendered),
			StartedAt: started,
		}
		if err := e.runs.CreateStep(step); err != nil {
			logger.Error("failed to create step log", zap.Error(err))
		}

		execStart := time.Now()
		out, execErr := executor.Run(ctx, rendered, &node.Context{Vars: vars})
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
			return run, execErr
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
	return run, nil
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
