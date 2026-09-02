// Package dsl 实现 FlowGo 工作流的文档化定义（Flow Spec）。
//
// 设计目标：让工作流可以像写代码一样用 YAML 文档编写、走 Git 版本管理，
// 并与可视化表单互为无损的双向视图。
//
// 文档格式示例：
//
//	name: 示例：AI 巡检
//	description: 探测服务并让 AI 判断健康状态
//	enabled: false
//	group: demo
//
//	nodes:
//	  - id: n1
//	    type: http
//	    name: 探测服务
//	    config:
//	      url: https://httpbin.org/status/200
//	      timeout: 10
//	  - id: n2
//	    type: ai_agent
//	    name: AI 判断健康
//	    depends_on: [n1]
//	    config:
//	      system_prompt: |
//	        你是 SRE，判断服务是否健康。
//	      tools: [http-call]
//
// 关键约定：
//  1. depends_on 取代显式的 edges 列表，边由本包自动派生，降低编写负担；
//  2. 不写 depends_on 时，节点按文档中的书写顺序串行执行（执行引擎的既有语义）；
//  3. config 内容原样透传给节点执行器，不加约束。
package dsl

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/example/flowgo/model"
	"github.com/example/flowgo/node"

	"gopkg.in/yaml.v3"
)

// 节点配置在导出时的字段排列优先级，未列出的字段排在后面并按字典序输出。
// 目的是让导出的文档字段顺序符合阅读直觉（如 http 的 url 在 timeout 之前）。
var configFieldOrder = map[string][]string{
	node.TypeHTTP:    {"method", "url", "headers", "body", "timeout"},
	node.TypeShell:   {"command", "workdir", "timeout"},
	node.TypeDelay:   {"seconds"},
	node.TypeAIAgent: {"system_prompt", "user_prompt", "model", "base_url", "api_key", "temperature", "max_iterations", "timeout", "tools", "native_tools"},
}

// flowDoc 文档顶层结构，字段顺序即导出时的输出顺序。
type flowDoc struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description,omitempty"`
	Enabled     *bool     `yaml:"enabled,omitempty"`
	Cron        string    `yaml:"cron,omitempty"`
	Group       string    `yaml:"group,omitempty"`
	Nodes       []nodeDoc `yaml:"nodes"`
}

// nodeDoc 文档中的单个节点。
type nodeDoc struct {
	ID        string         `yaml:"id"`
	Type      string         `yaml:"type"`
	Name      string         `yaml:"name,omitempty"`
	DependsOn []string       `yaml:"depends_on,omitempty"`
	Config    map[string]any `yaml:"config,omitempty"`
}

// Parse 把 YAML 文档解析为工作流。
// 解析采用严格模式：出现未知字段会直接报错，避免拼写错误被静默忽略。
// 校验只覆盖文档结构本身，业务级校验（如节点类型是否注册）交由 service 统一完成。
func Parse(data []byte) (*model.Workflow, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var doc flowDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("YAML 解析失败：%w", err)
	}

	doc.Name = strings.TrimSpace(doc.Name)
	if doc.Name == "" {
		return nil, fmt.Errorf("缺少必填字段 name")
	}
	if len(doc.Nodes) == 0 {
		return nil, fmt.Errorf("至少需要定义一个节点（nodes）")
	}

	// 先建立 ID 集合，供 depends_on 的引用检查使用。
	known := make(map[string]bool, len(doc.Nodes))
	for _, n := range doc.Nodes {
		id := strings.TrimSpace(n.ID)
		if id == "" {
			return nil, fmt.Errorf("每个节点都必须填写 id")
		}
		if known[id] {
			return nil, fmt.Errorf("节点 id 重复：%q", id)
		}
		known[id] = true
	}

	nodes := make([]model.NodeDef, 0, len(doc.Nodes))
	edges := make([]model.EdgeDef, 0)

	for _, n := range doc.Nodes {
		id := strings.TrimSpace(n.ID)
		typ := strings.TrimSpace(n.Type)
		if typ == "" {
			return nil, fmt.Errorf("节点 %q 缺少 type", id)
		}
		if node.Get(typ) == nil {
			return nil, fmt.Errorf("节点 %q 的 type %q 不是已注册的节点类型，可选：%s",
				id, typ, strings.Join(node.Types(), ", "))
		}

		// depends_on 引用的节点必须存在，否则会派生出指向空节点的无效边。
		for _, dep := range n.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if !known[dep] {
				return nil, fmt.Errorf("节点 %q 的 depends_on 引用了不存在的节点 %q", id, dep)
			}
			if dep == id {
				return nil, fmt.Errorf("节点 %q 不能依赖自己", id)
			}
			edges = append(edges, model.EdgeDef{Source: dep, Target: id})
		}

		nodes = append(nodes, model.NodeDef{
			ID:     id,
			Type:   typ,
			Name:   strings.TrimSpace(n.Name),
			Config: normalizeConfig(n.Config),
		})
	}

	enabled := true
	if doc.Enabled != nil {
		enabled = *doc.Enabled
	}

	wf := &model.Workflow{
		Name:        doc.Name,
		Description: strings.TrimSpace(doc.Description),
		Enabled:     enabled,
		Cron:        strings.TrimSpace(doc.Cron),
		Group:       strings.TrimSpace(doc.Group),
	}
	if err := wf.SetGraph(&model.Graph{Nodes: nodes, Edges: edges}); err != nil {
		return nil, fmt.Errorf("图结构序列化失败：%w", err)
	}
	return wf, nil
}

// Export 把已有工作流导出为 YAML 文档，与 Parse 互为逆操作。
// 导出时会把内部的 edges 反解回 depends_on，并省略等于默认值的字段，让文档保持简洁。
func Export(wf *model.Workflow) ([]byte, error) {
	if wf == nil {
		return nil, fmt.Errorf("工作流为空，无法导出")
	}
	graph, err := wf.ParseGraph()
	if err != nil {
		return nil, err
	}

	// 反查每个节点的上游，保持边的原始声明顺序。
	deps := map[string][]string{}
	for _, e := range graph.Edges {
		deps[e.Target] = append(deps[e.Target], e.Source)
	}

	nodes := make([]nodeDoc, 0, len(graph.Nodes))
	for _, n := range graph.Nodes {
		doc := nodeDoc{
			ID:   n.ID,
			Type: n.Type,
			Name: n.Name,
		}
		// 名称与类型相同时视为默认名，省略以保持文档整洁。
		if doc.Name == doc.Type {
			doc.Name = ""
		}
		if len(deps[n.ID]) > 0 {
			doc.DependsOn = deps[n.ID]
		}
		if len(n.Config) > 0 {
			doc.Config = n.Config
		}
		nodes = append(nodes, doc)
	}

	enabled := wf.Enabled
	doc := flowDoc{
		Name:        wf.Name,
		Description: wf.Description,
		Enabled:     &enabled,
		Cron:        wf.Cron,
		Group:       wf.Group,
		Nodes:       nodes,
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(orderedFlow(doc)); err != nil {
		return nil, fmt.Errorf("YAML 序列化失败：%w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// normalizeConfig 递归规整节点配置，确保其中所有嵌套 map 的键都是字符串。
// YAML 允许非字符串键，而 JSON 不允许；统一成 map[string]any 后才能安全落库与序列化。
func normalizeConfig(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = normalizeValue(v)
	}
	return out
}

// normalizeValue 递归规整任意值，把 map[any]any 之类的类型统一为 map[string]any。
func normalizeValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return normalizeConfig(val)
	case map[any]any:
		out := make(map[string]any, len(val))
		for k, vv := range val {
			out[fmt.Sprint(k)] = normalizeValue(vv)
		}
		return out
	case []any:
		out := make([]any, 0, len(val))
		for _, item := range val {
			out = append(out, normalizeValue(item))
		}
		return out
	default:
		return v
	}
}

// orderedFlow 把 flowDoc 包装成可自定义输出顺序的结构，
// 主要目的是让 config 字段按 configFieldOrder 排列，提升文档可读性。
func orderedFlow(doc flowDoc) *yaml.Node {
	root := &yaml.Node{Kind: yaml.MappingNode}

	addStr := func(key, value string, omitEmpty bool) {
		if omitEmpty && value == "" {
			return
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Value: value},
		)
	}

	addStr("name", doc.Name, false)
	addStr("description", doc.Description, true)
	if doc.Enabled != nil {
		b := *doc.Enabled
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "enabled"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprint(b), Tag: "!!bool"},
		)
	}
	addStr("cron", doc.Cron, true)
	addStr("group", doc.Group, true)

	// nodes 序列。
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, n := range doc.Nodes {
		seq.Content = append(seq.Content, orderedNode(n))
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "nodes"},
		seq,
	)
	return root
}

// orderedNode 生成单个节点的映射节点，config 按预设顺序输出。
func orderedNode(n nodeDoc) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}

	addStr := func(key, value string, omitEmpty bool) {
		if omitEmpty && value == "" {
			return
		}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Value: value},
		)
	}

	addStr("id", n.ID, false)
	addStr("type", n.Type, false)
	addStr("name", n.Name, true)

	if len(n.DependsOn) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, d := range n.DependsOn {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: d})
		}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "depends_on"},
			seq,
		)
	}

	if len(n.Config) > 0 {
		value := &yaml.Node{}
		if err := value.Encode(orderedConfig(n.Type, n.Config)); err == nil {
			m.Content = append(m.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "config"},
				value,
			)
		}
	}
	return m
}

// orderedConfig 按节点类型推荐的字段顺序输出配置，未预设的字段按字典序排在最后。
func orderedConfig(nodeType string, cfg map[string]any) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}

	appendKV := func(k string) {
		value := &yaml.Node{}
		if err := value.Encode(cfg[k]); err != nil {
			return
		}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k},
			value,
		)
	}

	used := map[string]bool{}
	for _, k := range configFieldOrder[nodeType] {
		if _, ok := cfg[k]; ok {
			appendKV(k)
			used[k] = true
		}
	}

	rest := make([]string, 0, len(cfg))
	for k := range cfg {
		if !used[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		appendKV(k)
	}
	return m
}
