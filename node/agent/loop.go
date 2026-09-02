package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/example/flowgo/logger"

	"go.uber.org/zap"
)

// defaultAgentMaxIter agent 循环在未配置时的最大思考轮次（仅 agent 循环内部使用）。
const defaultAgentMaxIter = 5

// AgentToolCall AI 输出的一次工具调用，仅在 ai_agent 节点内部生效。
type AgentToolCall struct {
	ToolName string         `json:"tool_name"`
	Args     map[string]any `json:"args"`
}

// AgentResult 一次 ai_agent 节点内部循环的产出，供节点组装输出。
type AgentResult struct {
	// Answer 大模型给出的最终结论。
	Answer string
	// Model 实际使用的模型。
	Model string
	// Iterations 实际消耗的思考轮次。
	Iterations int
	// Exhausted 是否在达到最大轮次后仍未收敛。
	Exhausted bool
	// ToolCalls 本节点内发生过的全部工具调用记录。
	ToolCalls []map[string]any
	// Usage 累计的 token 消耗。
	Usage map[string]any
}

// AgentLoop 在 ai_agent 节点内部运行「思考 → 调用工具 → 观察结果」循环。
// 它的活动范围被严格限制在单个节点内，无法修改 DAG 结构。
type AgentLoop struct {
	// Client 大模型客户端。
	Client *llmClient
	// Tools 可用工具注册表。
	Tools *ToolRegistry
	// Model 实际使用的模型。
	Model string
	// MaxIter 思考 + 工具调用的最大轮次。
	MaxIter int
	// Temperature 采样温度。
	Temperature float64
	// MaxTokens 单次生成的最大 token 数。
	MaxTokens int
	// NativeTools 是否使用原生 function calling 协议（而非文本协议）。
	NativeTools bool
	// MaxToolOut 工具结果回灌前的最大长度，超出截断。
	MaxToolOut int
	// LogFields 附加到每条调试日志上的公共字段（如 node、model）。
	LogFields []zap.Field
}

// Run 执行循环直到大模型给出最终结论或达到最大轮次。
func (l *AgentLoop) Run(ctx context.Context, systemPrompt, userPrompt string) (*AgentResult, error) {
	if l.MaxIter <= 0 {
		l.MaxIter = defaultAgentMaxIter
	}
	if l.MaxTokens < 0 {
		l.MaxTokens = 0
	}

	res := &AgentResult{Model: l.Model}
	messages := []llmMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	var lastContent string
	// 每轮：调用大模型 → 解析其回复 → 若要求调用工具则执行并把结果回灌，直到模型给出最终结论或耗尽轮次。
	for iter := 1; iter <= l.MaxIter; iter++ {
		if err := ctx.Err(); err != nil {
			logger.Warn("ai_agent 循环被上下文取消，提前结束", l.LogFields...)
			return nil, err
		}

		logger.Debug("ai_agent 发起模型调用",
			append(append([]zap.Field{}, l.LogFields...),
				zap.Int("第几轮", iter),
				zap.Int("最大轮次", l.MaxIter),
			)...,
		)
		resp, err := l.Client.chat(ctx, llmRequest{
			Model:       l.Model,
			Messages:    messages,
			Temperature: l.Temperature,
			MaxTokens:   l.MaxTokens,
			Tools:       l.toolDefs(),
		})
		if err != nil {
			logger.Error("ai_agent 模型调用失败", append(append([]zap.Field{}, l.LogFields...),
				zap.Int("第几轮", iter),
				zap.Error(err),
			)...)
			return nil, err
		}

		msg := resp.Choices[0].Message
		res.Iterations = iter
		res.addUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
		if text := strings.TrimSpace(msg.Content); text != "" {
			lastContent = text
		}

		// 分支一：原生 function calling（native_tools=true）时，大模型直接返回结构化工具调用，按协议回传结果。
		if len(msg.ToolCalls) > 0 {
			messages = append(messages, llmMessage{Role: "assistant", Content: msg.Content, ToolCalls: msg.ToolCalls})
			for _, call := range msg.ToolCalls {
				args := decodeJSONObject(call.Function.Arguments)
				result, callErr := l.execTool(ctx, call.Function.Name, args)
				res.recordCall(call.Function.Name, args, result, callErr)
				messages = append(messages, llmMessage{
					Role:       "tool",
					ToolCallID: call.ID,
					Name:       call.Function.Name,
					Content:    l.toolFeedback(call.Function.Name, result, callErr),
				})
			}
			continue
		}

		// 分支二：文本协议（默认）时，从回复文本中解析 JSON 形式工具调用；解析不到则视为最终结论。
		calls := l.parseToolCalls(msg.Content)
		if len(calls) == 0 {
			res.Answer = strings.TrimSpace(msg.Content)
			return res, nil
		}

		messages = append(messages, llmMessage{Role: "assistant", Content: msg.Content})
		for _, call := range calls {
			result, callErr := l.execTool(ctx, call.ToolName, call.Args)
			res.recordCall(call.ToolName, call.Args, result, callErr)
			messages = append(messages, llmMessage{
				Role:    "user",
				Content: l.toolFeedback(call.ToolName, result, callErr),
			})
		}
	}

	// 达到最大轮次仍要求调用工具：以最后一轮文本作为降级结论，不判定节点失败。
	res.Exhausted = true
	res.Answer = lastContent
	logger.Warn("ai_agent 达到最大迭代次数，按降级结论返回", l.LogFields...)
	return res, nil
}

// execTool 执行单次工具调用，结果过长时截断，避免撑爆上下文。
func (l *AgentLoop) execTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if l.Tools == nil {
		return "", fmt.Errorf("tool %q is unavailable: empty tool registry", name)
	}

	start := time.Now()
	logger.Debug("ai_agent 即将调用工具",
		append(append([]zap.Field{}, l.LogFields...),
			zap.String("工具", name),
		)...,
	)
	result, err := l.Tools.Call(ctx, name, args)
	elapsed := time.Since(start).Milliseconds()
	result = truncate(result, l.MaxToolOut)

	fields := append([]zap.Field{}, l.LogFields...)
	fields = append(fields,
		zap.String("工具", name),
		zap.Int64("耗时_毫秒", elapsed),
	)
	if err != nil {
		fields = append(fields, zap.Error(err))
		logger.Warn("ai_agent 工具调用失败", fields...)
		return result, err
	}
	logger.Info("ai_agent 工具调用完成", fields...)
	return result, nil
}

// toolFeedback 把工具结果包装成回传给大模型的消息。
func (l *AgentLoop) toolFeedback(name, result string, err error) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("工具 %s 的返回结果：\n", name))
	if strings.TrimSpace(result) == "" {
		b.WriteString("(空结果)")
	} else {
		b.WriteString(result)
	}
	if err != nil {
		b.WriteString("\n调用失败原因：" + err.Error())
	}
	return b.String()
}

// toolDefs 生成下发给大模型的工具定义，未开启原生工具调用时返回 nil。
func (l *AgentLoop) toolDefs() []llmToolDef {
	if !l.NativeTools || l.Tools == nil {
		return nil
	}
	specs := l.Tools.Specs()
	if len(specs) == 0 {
		return nil
	}
	defs := make([]llmToolDef, 0, len(specs))
	for _, s := range specs {
		defs = append(defs, llmToolDef{Type: "function", Function: s})
	}
	return defs
}

// recordCall 记录一次工具调用，供节点输出与执行日志查看。
func (r *AgentResult) recordCall(name string, args map[string]any, result string, err error) {
	entry := map[string]any{
		"tool_name": name,
		"args":      args,
		"result":    result,
		"ok":        err == nil,
	}
	if err != nil {
		entry["error"] = err.Error()
	}
	r.ToolCalls = append(r.ToolCalls, entry)
}

// addUsage 累加 token 消耗。
func (r *AgentResult) addUsage(prompt, completion, total int) {
	if r.Usage == nil {
		r.Usage = map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	}
	r.Usage["prompt_tokens"] = r.Usage["prompt_tokens"].(int) + prompt
	r.Usage["completion_tokens"] = r.Usage["completion_tokens"].(int) + completion
	r.Usage["total_tokens"] = r.Usage["total_tokens"].(int) + total
}

// parseToolCalls 解析回复中的工具调用，只保留看起来确实是指令的片段，
// 避免把「恰好是 JSON 的最终结论」误判成工具调用。
func (l *AgentLoop) parseToolCalls(text string) []AgentToolCall {
	return parseAgentToolCalls(text, func(name string) bool {
		return l.Tools != nil && l.Tools.Get(name) != nil
	})
}

// parseAgentToolCalls 从大模型回复文本中提取工具调用指令。
// 支持裸 JSON、```json 代码块以及一次输出多个调用。
// isTool 用于判断名称是否为已注册工具；命中已注册工具直接采纳，
// 未命中时只有在显式带有 args/arguments/parameters 字段时才当作调用（让 AI 看到「工具不存在」的反馈）。
func parseAgentToolCalls(text string, isTool func(string) bool) []AgentToolCall {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if strings.Contains(trimmed, "```") {
		trimmed = stripCodeFences(trimmed)
	}

	var calls []AgentToolCall
	for _, raw := range extractJSONObjects(trimmed) {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			continue
		}
		name := firstString(obj, "tool_name", "tool", "name")
		if name == "" {
			continue
		}
		if isTool == nil || !isTool(name) {
			_, hasArgs := obj["args"]
			_, hasArguments := obj["arguments"]
			_, hasParams := obj["parameters"]
			if !hasArgs && !hasArguments && !hasParams {
				continue
			}
		}
		call := AgentToolCall{ToolName: name, Args: map[string]any{}}
		switch args := obj["args"].(type) {
		case map[string]any:
			call.Args = args
		default:
			if v, ok := obj["arguments"]; ok {
				if m, ok := v.(map[string]any); ok {
					call.Args = m
				} else if decoded := decodeJSONObject(fmt.Sprint(v)); len(decoded) > 0 {
					call.Args = decoded
				}
			} else if v, ok := obj["parameters"]; ok {
				if m, ok := v.(map[string]any); ok {
					call.Args = m
				}
			}
		}
		calls = append(calls, call)
	}
	return calls
}

// stripCodeFences 去掉 Markdown 代码块围栏，只保留其中的内容。
func stripCodeFences(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			b.WriteString("\n")
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// extractJSONObjects 扫描文本中所有顶层 JSON 对象片段，返回原始字符串。
func extractJSONObjects(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		if s[i] != '{' {
			i++
			continue
		}
		depth := 0
		inStr := false
		escaped := false
		end := -1
		for j := i; j < len(s); j++ {
			c := s[j]
			if inStr {
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					inStr = false
				}
				continue
			}
			switch c {
			case '"':
				inStr = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = j + 1
				}
			}
			if end > 0 {
				break
			}
		}
		if end < 0 {
			break
		}
		out = append(out, s[i:end])
		i = end
	}
	return out
}

// decodeJSONObject 解析可能是 JSON 的字符串，失败时返回空对象。
func decodeJSONObject(raw string) map[string]any {
	out := map[string]any{}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return out
	}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return map[string]any{}
	}
	return out
}

// firstString 按候选键顺序取出第一个非空字符串值。
func firstString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := obj[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// truncate 截断超长输出，避免工具结果撑爆模型上下文。
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("... (truncated, total %d bytes)", len(s))
}
