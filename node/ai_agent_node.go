package node

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/example/flowgo/config"
	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/node/agent"

	"go.uber.org/zap"
)

// ai_agent 节点的可调参数默认值与安全上限。
const (
	defaultAgentBaseURL     = "https://api.openai.com/v1"
	defaultAgentModel       = "gpt-4o-mini"
	defaultAgentTemperature = 0.2
	defaultAgentTimeoutSec  = 60
	defaultAgentMaxIter     = 5
	maxAgentMaxIter         = 20
	defaultAgentToolOutput  = 4000
	maxAgentContextChars    = 12000
)

// defaultAgentInstruction 未配置 system_prompt 时的兜底指令。
const defaultAgentInstruction = "你是工作流中的一个分析节点。请基于给定的输入数据完成分析、提取或判断，并给出简洁明确的结论。"

// AIAgentExecutor AI 增强节点执行器：
// 接收上游数据 → 大模型分析 → 调用已注册工具 → 输出结论交给 DAG 下一个节点。
// 它只能在本节点内部活动，无法增删节点或改变执行顺序。
type AIAgentExecutor struct{}

func init() { Register(&AIAgentExecutor{}) }

// Type 返回节点类型 ai_agent。
func (e *AIAgentExecutor) Type() string { return TypeAIAgent }

// MaskedFields 声明写入执行日志前需要脱敏的配置字段。
func (e *AIAgentExecutor) MaskedFields() []string { return []string{"api_key", "apiKey"} }

// Run 执行 ai_agent 节点，返回可供下游引用的输出变量。
// 流程：① 解析并校验节点参数（模型、密钥、迭代上限等，节点配置优先于全局配置）；
//       ② 构造 AgentLoop 循环调用大模型并按需调用工具；
//       ③ 将模型结论与统计信息组装为输出交给下游节点。
func (e *AIAgentExecutor) Run(ctx context.Context, cfg map[string]any, ec *Context) (map[string]any, error) {
	// 逐级兜底获取大模型连接参数：节点配置 → 全局配置 → 内置默认值。
	baseURL := firstNonEmpty(str(cfg, "base_url", ""), config.GetLLMBaseURL(), defaultAgentBaseURL)
	apiKey := firstNonEmpty(str(cfg, "api_key", ""), config.GetLLMAPIKey())
	model := firstNonEmpty(str(cfg, "model", ""), config.GetLLMModel(), defaultAgentModel)
	if apiKey == "" {
		return nil, fmt.Errorf("ai_agent node requires api_key (set it in node config or config/config.yaml -> llm.api_key)")
	}

	timeoutSec := intOr(cfg, "timeout", firstPositive(config.GetLLMTimeout(), defaultAgentTimeoutSec))
	if timeoutSec <= 0 {
		timeoutSec = defaultAgentTimeoutSec
	}
	maxIter := intOr(cfg, "max_iterations", defaultAgentMaxIter)
	if maxIter <= 0 {
		maxIter = defaultAgentMaxIter
	}
	if maxIter > maxAgentMaxIter {
		maxIter = maxAgentMaxIter
	}
	temperature := floatOr(cfg, "temperature", defaultAgentTemperature)
	maxTokens := intOr(cfg, "max_tokens", 0)
	maxToolOut := intOr(cfg, "max_tool_output", defaultAgentToolOutput)

	registry := agent.DefaultToolRegistry().Filter(strList(cfg, "tools"))
	if len(registry.Names()) == 0 {
		logger.Error("ai_agent 节点未配置可用工具，节点执行失败",
			zap.String("node", nodeIDOf(ec)),
		)
		return nil, fmt.Errorf("ai_agent node has no usable tools, check the tools field")
	}
	logger.Debug("ai_agent 节点初始化完成",
		zap.String("node", nodeIDOf(ec)),
		zap.String("模型", model),
		zap.Int("最大迭代", maxIter),
		zap.Float64("温度", temperature),
		zap.Int("最大令牌", maxTokens),
		zap.Strings("可用工具", registry.Names()),
		zap.Bool("原生工具调用", boolOr(cfg, "native_tools", false)),
	)

	loop := &agent.AgentLoop{
		Client:      agent.NewLLMClient(baseURL, apiKey, model, time.Duration(timeoutSec)*time.Second),
		Tools:       registry,
		Model:       model,
		MaxIter:     maxIter,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		NativeTools: boolOr(cfg, "native_tools", false),
		MaxToolOut:  maxToolOut,
		LogFields: []zap.Field{
			zap.String("node", nodeIDOf(ec)),
			zap.String("model", model),
		},
	}

	systemPrompt := buildAgentSystemPrompt(registry, str(cfg, "system_prompt", ""), maxIter)
	userPrompt := buildAgentUserPrompt(ec, str(cfg, "user_prompt", ""))

	// 记录开始时间并计算耗时，用于下面执行完成日志里的 duration_ms。
	start := time.Now()
	logger.Debug("ai_agent 节点开始执行",
		zap.String("node", nodeIDOf(ec)),
		zap.String("模型", model),
	)
	res, err := loop.Run(ctx, systemPrompt, userPrompt)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		logger.Error("ai_agent 节点执行出错",
			zap.String("node", nodeIDOf(ec)),
			zap.String("模型", model),
			zap.Int64("耗时_毫秒", elapsed),
			zap.Error(err),
		)
		return nil, err
	}

	out := map[string]any{
		"answer":                 res.Answer,
		"content":                res.Answer,
		"model":                  res.Model,
		"iterations":             res.Iterations,
		"max_iterations_reached": res.Exhausted,
		"tool_calls":             res.ToolCalls,
		"usage":                  res.Usage,
		"duration_ms":            elapsed,
	}

	logger.Info("ai_agent 节点执行完成",
		zap.String("node", nodeIDOf(ec)),
		zap.String("模型", res.Model),
		zap.Int("迭代次数", res.Iterations),
		zap.Int("工具调用次数", len(res.ToolCalls)),
		zap.Bool("达到最大迭代", res.Exhausted),
		zap.Int64("耗时_毫秒", elapsed),
	)
	return out, nil
}

// ValidateAgentTools 校验 ai_agent 节点的 tools 配置是否都是已注册工具。
// 未配置 tools 表示允许全部内置工具，直接通过。
func ValidateAgentTools(cfg map[string]any) error {
	names := strList(cfg, "tools")
	if len(names) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, n := range agent.ToolNames() {
		known[n] = true
	}
	unknown := make([]string, 0, len(names))
	for _, n := range names {
		if !known[n] {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown agent tools %v, available tools: %v", unknown, agent.ToolNames())
	}
	return nil
}

// buildAgentSystemPrompt 生成系统提示词：工具清单 + 调用协议 + 人的任务指令。
func buildAgentSystemPrompt(registry *agent.ToolRegistry, instruction string, maxIter int) string {
	if strings.TrimSpace(instruction) == "" {
		instruction = defaultAgentInstruction
	}

	var b strings.Builder
	b.WriteString("你是工作流中的一个 AI 分析节点，负责在当前节点内部完成分析、提取、判断或转换任务。\n")
	b.WriteString("重要约束：\n")
	b.WriteString("1. 你不能修改工作流的结构，不能新增、删除或跳过任何节点，只能在当前节点内完成任务。\n")
	b.WriteString("2. 你只能调用下面列出的工具，不能凭空编造工具。\n")
	b.WriteString(fmt.Sprintf("3. 思考 + 工具调用的总轮次最多 %d 轮，达到上限后必须直接给出结论。\n\n", maxIter))

	b.WriteString("可用工具：\n")
	specs := registry.Specs()
	if len(specs) == 0 {
		b.WriteString("（无可用工具，请直接输出分析结论）\n")
	}
	for _, s := range specs {
		params, _ := json.Marshal(s.Parameters)
		b.WriteString(fmt.Sprintf("- %s：%s\n  参数 schema：%s\n", s.Name, s.Description, string(params)))
	}

	b.WriteString("\n调用工具时，只输出一个 JSON 对象（可放在 ```json 代码块中），不要输出其他内容：\n")
	b.WriteString("{\"tool_name\":\"工具名\",\"args\":{...}}\n\n")
	b.WriteString("不需要再调用工具时，直接输出最终结论（纯文本，不要包裹成 JSON）。\n\n")
	b.WriteString("# 任务指令\n")
	b.WriteString(instruction)
	return b.String()
}

// buildAgentUserPrompt 拼装给大模型的用户输入：上游节点输出 + 触发数据 + 补充指令。
func buildAgentUserPrompt(ec *Context, extra string) string {
	upstream, trigger := splitContext(ec)

	var b strings.Builder
	b.WriteString("## 上游节点输出（JSON）\n")
	if len(upstream) == 0 {
		b.WriteString("{}\n（本节点没有上游节点，可参考下面的触发数据）\n")
	} else {
		b.WriteString(truncate(encodeJSON(upstream), maxAgentContextChars))
		b.WriteString("\n")
	}
	if trigger != nil {
		b.WriteString("\n## 触发数据（JSON）\n")
		b.WriteString(truncate(encodeJSON(trigger), maxAgentContextChars))
		b.WriteString("\n")
	}
	if strings.TrimSpace(extra) != "" {
		b.WriteString("\n## 补充指令\n")
		b.WriteString(extra)
		b.WriteString("\n")
	}
	b.WriteString("\n请基于以上数据完成任务：需要调用工具时只输出工具调用 JSON，任务完成时直接输出结论。")
	return b.String()
}

// splitContext 从执行上下文中取出上游节点输出与触发数据。
// 配置了连线时只取直接上游，否则退化为全部已执行节点。
func splitContext(ec *Context) (map[string]any, any) {
	upstream := map[string]any{}
	var trigger any
	if ec == nil {
		return upstream, trigger
	}
	if t := ec.Vars["trigger"]; !isEmptyValue(t) {
		trigger = t
	}

	nodes, _ := ec.Vars["nodes"].(map[string]any)
	ids := append([]string{}, ec.Upstream...)
	if len(ids) == 0 {
		for id := range nodes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	for _, id := range ids {
		if v, ok := nodes[id]; ok {
			upstream[id] = v
		}
	}
	return upstream, trigger
}

// isEmptyValue 判断触发数据是否为空，空的触发数据不进入提示词。
func isEmptyValue(v any) bool {
	switch val := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(val) == ""
	case map[string]any:
		return len(val) == 0
	case []any:
		return len(val) == 0
	}
	return false
}

// encodeJSON 序列化任意值，失败时退化为字符串形式。
func encodeJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// nodeIDOf 取当前节点 ID 用于日志，不存在时返回占位符。
func nodeIDOf(ec *Context) string {
	if ec == nil || ec.NodeID == "" {
		return "-"
	}
	return ec.NodeID
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// firstPositive 返回第一个大于 0 的整数，都不满足时返回 0。
func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// intOr 读取整数配置，缺省或非法时返回默认值。
func intOr(cfg map[string]any, key string, def int) int {
	v, ok := cfg[key]
	if !ok || v == nil {
		return def
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case float32:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	case string:
		var n int
		if _, err := fmt.Sscanf(val, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// floatOr 读取浮点配置，缺省或非法时返回默认值。
func floatOr(cfg map[string]any, key string, def float64) float64 {
	switch val := cfg[key].(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		var f float64
		if _, err := fmt.Sscanf(val, "%f", &f); err == nil {
			return f
		}
	}
	return def
}

// boolOr 读取布尔配置，缺省或非法时返回默认值。
func boolOr(cfg map[string]any, key string, def bool) bool {
	switch val := cfg[key].(type) {
	case bool:
		return val
	case string:
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
	case float64:
		return val != 0
	}
	return def
}

// strList 读取字符串数组配置，支持 JSON 数组与逗号分隔字符串。
func strList(cfg map[string]any, key string) []string {
	v, ok := cfg[key]
	if !ok || v == nil {
		return nil
	}
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		parts := strings.FieldsFunc(val, func(r rune) bool { return r == ',' || r == ';' })
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
