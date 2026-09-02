// Package engine 实现工作流的编排执行：拓扑排序、节点调度与日志落库。
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	maxRunDuration := config.GetMaxRunDuration()
	logger.Debug("执行引擎初始化完成",
		zap.Duration("单次运行超时", maxRunDuration),
		zap.Int("超时配置_分钟", int(maxRunDuration.Minutes())),
	)
	return &Engine{runs: runs, maxRunDuration: maxRunDuration}
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
	logger.Debug("开始同步执行工作流，调用方将阻塞至流程结束",
		zap.Uint("工作流ID", wf.ID),
		zap.String("工作流名称", wf.Name),
		zap.String("触发方式", trigger),
	)
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
	logger.Debug("已创建待执行（pending）运行记录，HTTP 请求即将返回",
		zap.Uint("运行ID", run.ID),
		zap.Uint("工作流ID", wf.ID),
		zap.String("触发方式", trigger),
	)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("异步执行发生 panic，运行已标记失败",
					zap.Uint("运行ID", run.ID), zap.Any("异常信息", r))
				e.finishRun(run, model.RunStatusFailed, fmt.Sprintf("panic: %v", r))
			}
		}()
		logger.Debug("后台协程开始执行工作流",
			zap.Uint("运行ID", run.ID),
			zap.Uint("工作流ID", wf.ID),
		)
		// 后台执行不复用请求上下文，避免 HTTP 响应返回后运行被取消。
		if err := e.runWorkflow(context.Background(), run, wf, trigger, payload); err != nil {
			logger.Warn("异步运行执行失败",
				zap.Uint("运行ID", run.ID),
				zap.Uint("工作流ID", wf.ID),
				zap.Error(err),
			)
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
	logger.Debug("运行记录已落库",
		zap.Uint("运行ID", run.ID),
		zap.Uint("工作流ID", wf.ID),
		zap.String("初始状态", status),
		zap.Int("负载长度", len(payload)),
	)
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
		logger.Error("运行记录置为运行中失败", zap.Uint("运行ID", run.ID), zap.Error(err))
		return
	}
	logger.Debug("运行记录状态已置为运行中（running），前端可轮询到进度",
		zap.Uint("运行ID", run.ID),
	)
}

// runWorkflow 执行已落库的运行记录：拓扑排序后串行执行节点并写入节点日志，
// 任一节点失败即终止，运行记录标记 failed。
func (e *Engine) runWorkflow(ctx context.Context, run *model.Run, wf *model.Workflow, trigger, payload string) error {
	ctx, cancel := context.WithTimeout(ctx, e.maxRunDuration)
	defer cancel()

	logger.Debug("流程执行已设置超时上限",
		zap.Uint("运行ID", run.ID),
		zap.Duration("超时时间", e.maxRunDuration),
	)

	e.markRunning(run)

	graph, err := wf.ParseGraph()
	if err != nil {
		logger.Error("工作流图解析失败，运行标记失败",
			zap.Uint("运行ID", run.ID), zap.Error(err))
		e.finishRun(run, model.RunStatusFailed, err.Error())
		return err
	}

	order, err := topoSort(graph)
	if err != nil {
		logger.Error("工作流拓扑排序失败（可能存在环路），运行标记失败",
			zap.Uint("运行ID", run.ID), zap.Error(err))
		e.finishRun(run, model.RunStatusFailed, err.Error())
		return err
	}
	if len(order) == 0 {
		logger.Debug("工作流没有可执行节点，按空流程直接成功结束",
			zap.Uint("运行ID", run.ID))
		e.finishRun(run, model.RunStatusSuccess, "")
		return nil
	}
	logger.Debug("拓扑排序完成，确定节点执行顺序",
		zap.Uint("运行ID", run.ID),
		zap.Strings("执行顺序", order),
	)

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

	logger.Info("运行开始",
		zap.Uint("运行ID", run.ID),
		zap.Uint("工作流ID", wf.ID),
		zap.String("工作流名称", wf.Name),
		zap.String("触发方式", trigger),
		zap.Int("节点数量", len(order)),
	)

	for idx, id := range order {
		def := nodeByID[id]
		executor := node.Get(def.Type)
		if executor == nil {
			err := fmt.Errorf("unsupported node type %q on node %q", def.Type, id)
			logger.Error("节点类型未注册，无法执行",
				zap.Uint("运行ID", run.ID),
				zap.String("节点ID", def.ID),
				zap.String("节点类型", def.Type),
			)
			e.logNodeFailure(run, def, nil, err)
			e.finishRun(run, model.RunStatusFailed, err.Error())
			return err
		}

		rendered, rerr := renderConfig(def.Config, vars)
		if rerr != nil {
			logger.Error("节点配置模板渲染失败",
				zap.Uint("运行ID", run.ID),
				zap.String("节点ID", def.ID),
				zap.Error(rerr),
			)
			e.logNodeFailure(run, def, nil, rerr)
			e.finishRun(run, model.RunStatusFailed, rerr.Error())
			return rerr
		}
		logger.Debug("节点配置渲染完成",
			zap.Uint("运行ID", run.ID),
			zap.String("节点ID", def.ID),
			zap.Strings("配置字段", sortedKeys(rendered)),
		)

		logger.Debug("节点开始执行",
			zap.Uint("运行ID", run.ID),
			zap.String("进度", fmt.Sprintf("%d/%d", idx+1, len(order))),
			zap.String("节点ID", def.ID),
			zap.String("节点名称", def.Name),
			zap.String("节点类型", def.Type),
			zap.Strings("上游节点", preds[def.ID]),
		)

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
			logger.Error("节点日志写入失败",
				zap.Uint("运行ID", run.ID),
				zap.String("节点ID", def.ID),
				zap.Error(err),
			)
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
				logger.Error("节点失败日志更新失败",
					zap.Uint("运行ID", run.ID),
					zap.String("节点ID", def.ID),
					zap.Error(err),
				)
			}
			logger.Warn("节点执行失败，流程中断",
				zap.Uint("运行ID", run.ID),
				zap.String("节点ID", def.ID),
				zap.String("节点名称", def.Name),
				zap.String("节点类型", def.Type),
				zap.Bool("上下文已取消", ctx.Err() != nil),
				zap.Error(execErr),
			)
			e.finishRun(run, model.RunStatusFailed,
				fmt.Sprintf("node %q (%s) failed: %v", def.Name, def.Type, execErr))
			return execErr
		}

		step.Status = model.RunStatusSuccess
		step.Output = mustJSON(out)
		if err := e.runs.UpdateStep(step); err != nil {
			logger.Error("节点成功日志更新失败",
				zap.Uint("运行ID", run.ID),
				zap.String("节点ID", def.ID),
				zap.Error(err),
			)
		}

		nodesOut[def.ID] = out
		logger.Info("节点执行完成",
			zap.Uint("运行ID", run.ID),
			zap.String("节点ID", def.ID),
			zap.String("节点名称", def.Name),
			zap.String("节点类型", def.Type),
			zap.Int64("耗时_毫秒", step.Duration),
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
		logger.Error("运行记录收尾更新失败",
			zap.Uint("运行ID", run.ID), zap.Error(err))
		return
	}
	if status == model.RunStatusFailed {
		logger.Warn("运行结束（失败）",
			zap.Uint("运行ID", run.ID),
			zap.String("最终状态", status),
			zap.Int64("总耗时_毫秒", run.Duration),
			zap.String("错误信息", errMsg),
		)
		return
	}
	logger.Info("运行结束",
		zap.Uint("运行ID", run.ID),
		zap.String("最终状态", status),
		zap.Int64("总耗时_毫秒", run.Duration),
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
		logger.Error("节点失败日志写入失败",
			zap.Uint("运行ID", run.ID),
			zap.String("节点ID", def.ID),
			zap.Error(cerr),
		)
		return
	}
	logger.Debug("已补记节点执行前失败日志",
		zap.Uint("运行ID", run.ID),
		zap.String("节点ID", def.ID),
	)
}

// sortedKeys 按字典序返回配置字段名，仅用于调试日志输出，保证顺序稳定可读。
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
