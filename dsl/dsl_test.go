package dsl

import (
	"strings"
	"testing"

	"github.com/example/flowgo/model"
)

// parse 解析文档并在失败时终止测试。
func parse(t *testing.T, doc string) *model.Workflow {
	t.Helper()
	wf, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("解析失败：%v\n文档：\n%s", err, doc)
	}
	return wf
}

// graphOf 取出工作流的图结构。
func graphOf(t *testing.T, wf *model.Workflow) *model.Graph {
	t.Helper()
	g, err := wf.ParseGraph()
	if err != nil {
		t.Fatalf("图解析失败：%v", err)
	}
	return g
}

// TestParseLinear 未写 depends_on 时按书写顺序排列，且不应产生边。
func TestParseLinear(t *testing.T) {
	wf := parse(t, `
name: 线性流程
nodes:
  - id: n1
    type: http
    config:
      url: http://a
  - id: n2
    type: shell
    config:
      command: echo hi
`)
	g := graphOf(t, wf)
	if len(g.Nodes) != 2 || g.Nodes[0].ID != "n1" || g.Nodes[1].ID != "n2" {
		t.Fatalf("节点顺序应保持声明顺序，实际：%+v", g.Nodes)
	}
	// 无边时执行引擎按声明顺序串行，这是「不写依赖即顺序执行」约定的基础。
	if len(g.Edges) != 0 {
		t.Fatalf("未声明依赖时不应产生边，实际：%+v", g.Edges)
	}
	if got := g.Nodes[0].Config["url"]; got != "http://a" {
		t.Fatalf("config 应原样透传，实际 url=%v", got)
	}
}

// TestParseDependsOn depends_on 应派生出对应的边。
func TestParseDependsOn(t *testing.T) {
	wf := parse(t, `
name: 有依赖
nodes:
  - id: n1
    type: http
    config:
      url: http://a
  - id: n2
    type: shell
    depends_on: [n1]
    config:
      command: echo hi
  - id: n3
    type: delay
    depends_on: [n1, n2]
    config:
      seconds: 2
`)
	g := graphOf(t, wf)
	if len(g.Edges) != 3 {
		t.Fatalf("应派生 3 条边（n1→n2, n1→n3, n2→n3），实际 %d 条：%+v", len(g.Edges), g.Edges)
	}
	want := map[string]bool{"n1>n2": false, "n1>n3": false, "n2>n3": false}
	for _, e := range g.Edges {
		key := e.Source + ">" + e.Target
		if _, ok := want[key]; !ok {
			t.Fatalf("出现预期外的边：%s", key)
		}
		want[key] = true
	}
	for k, seen := range want {
		if !seen {
			t.Fatalf("缺少应有的边：%s", k)
		}
	}
}

// TestParseRejects 覆盖各类非法文档，确保错误在导入阶段就被拦截而非留到运行时。
func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"缺少 name", "nodes:\n  - id: n1\n    type: http\n", "name"},
		{"空 nodes", "name: t\nnodes: []\n", "节点"},
		{"未知字段", "name: t\nnodes:\n  - id: n1\n    type: http\n    typo_field: 1\n", "typo_field"},
		{"未知节点类型", "name: t\nnodes:\n  - id: n1\n    type: nope\n", "nope"},
		{"缺少 type", "name: t\nnodes:\n  - id: n1\n", "type"},
		{"重复 id", "name: t\nnodes:\n  - id: n1\n    type: http\n  - id: n1\n    type: http\n", "重复"},
		{"依赖不存在", "name: t\nnodes:\n  - id: n1\n    type: http\n    depends_on: [n9]\n", "n9"},
		{"自依赖", "name: t\nnodes:\n  - id: n1\n    type: http\n    depends_on: [n1]\n", "自己"},
		{"缺少 id", "name: t\nnodes:\n  - type: http\n", "id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.doc))
			if err == nil {
				t.Fatalf("应当报错但没有")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际：%v", c.want, err)
			}
		})
	}
}

// TestEnabledDefault enabled 缺省应为 true，显式声明 false 时保留。
func TestEnabledDefault(t *testing.T) {
	if wf := parse(t, "name: t\nnodes:\n  - id: n1\n    type: http\n"); !wf.Enabled {
		t.Fatal("未声明 enabled 时应默认为 true")
	}
	if wf := parse(t, "name: t\nenabled: false\nnodes:\n  - id: n1\n    type: http\n"); wf.Enabled {
		t.Fatal("显式声明 enabled: false 时应为 false")
	}
}

// TestRoundTrip 导出后再解析应得到完全等价的工作流，且二次导出结果稳定。
func TestRoundTrip(t *testing.T) {
	doc := `name: 巡检流程
description: 演示往返
enabled: true
cron: '@every 1m'
group: demo
nodes:
  - id: n1
    type: http
    name: 探测
    config:
      url: https://httpbin.org/status/200
      method: GET
      headers:
        X-Token: abc
      timeout: 10
  - id: n2
    type: ai_agent
    name: 判断
    depends_on:
      - n1
    config:
      system_prompt: |
        你是 SRE。
        请判断健康状态。
      tools:
        - http-call
      max_iterations: 5
      temperature: 0.2
`
	wf1 := parse(t, doc)

	data, err := Export(wf1)
	if err != nil {
		t.Fatalf("导出失败：%v", err)
	}
	wf2 := parse(t, string(data))

	if wf1.Name != wf2.Name || wf1.Cron != wf2.Cron || wf1.Group != wf2.Group ||
		wf1.Description != wf2.Description || wf1.Enabled != wf2.Enabled {
		t.Fatalf("基础字段往返不一致：%+v vs %+v", wf1, wf2)
	}
	g1, g2 := graphOf(t, wf1), graphOf(t, wf2)
	if len(g1.Nodes) != len(g2.Nodes) || len(g1.Edges) != len(g2.Edges) {
		t.Fatalf("图结构往返不一致：%+v vs %+v", g1, g2)
	}
	for i := range g1.Nodes {
		a, b := g1.Nodes[i], g2.Nodes[i]
		if a.ID != b.ID || a.Type != b.Type || a.Name != b.Name {
			t.Fatalf("节点 %d 往返不一致：%+v vs %+v", i, a, b)
		}
	}
	// 二次导出应稳定，说明导出结果是规范形式。
	data2, err := Export(wf2)
	if err != nil {
		t.Fatalf("二次导出失败：%v", err)
	}
	if string(data) != string(data2) {
		t.Fatalf("导出结果不稳定：\n%s\n---\n%s", data, data2)
	}
}

// TestExportOmitDefaults 导出应省略等于默认值的 name，让文档保持简洁。
func TestExportOmitDefaults(t *testing.T) {
	wf := parse(t, "name: t\nnodes:\n  - id: n1\n    type: http\n    config:\n      url: http://a\n")
	data, err := Export(wf)
	if err != nil {
		t.Fatalf("导出失败：%v", err)
	}
	out := string(data)
	if strings.Contains(out, "name: http") {
		t.Fatalf("节点名与类型相同时应省略 name 字段，实际输出：\n%s", out)
	}
	if !strings.Contains(out, "url: http://a") {
		t.Fatalf("应保留 config 内容，实际输出：\n%s", out)
	}
}

// TestRoundTripFormGraph 模拟「表单→文档→表单」的真实往返：
// 表单把图存成 JSON 文本（含显式 edges），Export 必须反解出 depends_on，
// 再 Parse 回来时根据 depends_on 派生出等价但顺序可能不同的边。
func TestRoundTripFormGraph(t *testing.T) {
	// 1) 构造一个表单保存形态的工作流（graph 为 JSON 文本，含 edges）。
	wf := &model.Workflow{
		Name:        "表单巡检",
		Description: "由可视化表单保存",
		Enabled:     true,
		Cron:        "0 8 * * *",
		Group:       "demo",
	}
	if err := wf.SetGraph(&model.Graph{
		Nodes: []model.NodeDef{
			{ID: "n1", Type: "http", Name: "探测", Config: map[string]any{
				"url": "https://httpbin.org/status/200", "method": "GET", "timeout": 10}},
			{ID: "n2", Type: "ai_agent", Name: "判断", Config: map[string]any{
				"system_prompt": "你是 SRE。", "tools": []any{"http-call"}, "max_iterations": 5, "temperature": 0.2}},
			{ID: "n3", Type: "shell", Name: "通知", Config: map[string]any{
				"command": "echo done"}},
		},
		Edges: []model.EdgeDef{
			{Source: "n1", Target: "n2"},
			{Source: "n1", Target: "n3"},
			{Source: "n2", Target: "n3"},
		},
	}); err != nil {
		t.Fatalf("设置图失败：%v", err)
	}

	// 2) 导出为文档（edges → depends_on）。
	data, err := Export(wf)
	if err != nil {
		t.Fatalf("导出失败：%v", err)
	}

	// 3) 文档再解析回工作流。
	wf2, err := Parse(data)
	if err != nil {
		t.Fatalf("解析导出文档失败：%v\n文档：\n%s", err, data)
	}

	// 4) 校验基础字段与节点顺序一致。
	if wf.Name != wf2.Name || wf.Description != wf2.Description ||
		wf.Cron != wf2.Cron || wf.Group != wf2.Group || wf.Enabled != wf2.Enabled {
		t.Fatalf("基础字段往返不一致：%+v vs %+v", wf, wf2)
	}
	g1, _ := wf.ParseGraph()
	g2, _ := wf2.ParseGraph()
	if len(g1.Nodes) != len(g2.Nodes) {
		t.Fatalf("节点数量往返不一致：%d vs %d", len(g1.Nodes), len(g2.Nodes))
	}
	for i := range g1.Nodes {
		a, b := g1.Nodes[i], g2.Nodes[i]
		if a.ID != b.ID || a.Type != b.Type || a.Name != b.Name {
			t.Fatalf("节点 %d 往返不一致：%+v vs %+v", i, a, b)
		}
	}

	// 5) 边应等价（由 depends_on 派生）。用邻接集合比较，不受顺序影响。
	want := edgeSet(g1.Edges)
	got := edgeSet(g2.Edges)
	if len(want) != len(got) {
		t.Fatalf("边数量不一致：%+v vs %+v", g1.Edges, g2.Edges)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("缺少应有的边 %s（原始边 %+v，往返后 %+v）", k, g1.Edges, g2.Edges)
		}
	}

	// 6) 导出结果应规范且稳定：表单原始图与「解析后的文档」导出结果相同。
	data2, err := Export(wf2)
	if err != nil {
		t.Fatalf("二次导出失败：%v", err)
	}
	if string(data) != string(data2) {
		t.Fatalf("表单图与文档图导出结果不一致：\n%s\n---\n%s", data, data2)
	}
}

// edgeSet 把边列表转成 "source>target" 集合，便于忽略顺序比较。
func edgeSet(edges []model.EdgeDef) map[string]bool {
	m := make(map[string]bool, len(edges))
	for _, e := range edges {
		m[e.Source+">"+e.Target] = true
	}
	return m
}
