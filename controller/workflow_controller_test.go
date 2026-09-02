// Package controller_test 对文档的导入/导出做端到端验证：
// 经真实路由发起 HTTP 请求，串起 Router、Controller、DSL 与 Service 校验，
// 并用内存实现替换仓储与引擎，使测试不依赖数据库等外部环境。
package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/example/flowgo/controller"
	"github.com/example/flowgo/dsl"
	"github.com/example/flowgo/model"
	"github.com/example/flowgo/router"
	"github.com/example/flowgo/service"
)

// sampleDoc 导入、导出与往返共用的示例文档。
const sampleDoc = `name: 巡检流程
description: 端到端往返
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
      timeout: 10
  - id: n2
    type: ai_agent
    name: 判断
    depends_on: [n1]
    config:
      system_prompt: 你是 SRE。
      tools: [http-call]
      max_iterations: 5
`

// memService 用内存 map 替换仓储与引擎，让测试无需数据库。
// 内嵌真实的 service 实现，目的只有一个：复用与表单保存完全相同的 Validate，
// 从而证明「文档导入」与「表单保存」走的是同一套校验规则。
type memService struct {
	service.WorkflowService
	next uint
	data map[uint]*model.Workflow
}

// newMemService 创建内存服务实例。
func newMemService() *memService {
	return &memService{
		// 校验逻辑不访问仓储与引擎，因此这里传 nil 是安全的。
		WorkflowService: service.NewWorkflowService(nil, nil),
		data:            map[uint]*model.Workflow{},
	}
}

// Get 按 ID 取回工作流，不存在时返回与真实实现一致的错误。
func (m *memService) Get(id uint) (*model.Workflow, error) {
	wf, ok := m.data[id]
	if !ok {
		return nil, service.ErrWorkflowNotFound
	}
	return wf, nil
}

// Create 落库并回填自增 ID，模拟数据库的写入行为。
func (m *memService) Create(wf *model.Workflow) error {
	m.next++
	wf.ID = m.next
	if wf.WebhookKey == "" {
		wf.WebhookKey = "test-key"
	}
	m.data[wf.ID] = wf
	return nil
}

// newServer 用真实路由与内存服务组装出可直接发起请求的处理器。
// 其余 Controller 传 nil：被测用例不会触达它们的路由。
func newServer(t *testing.T) (*memService, http.Handler) {
	t.Helper()
	svc := newMemService()
	wf := controller.NewWorkflowController(svc, nil)
	return svc, router.NewRouter("test", wf, nil, nil, nil, nil)
}

// do 发起一次 HTTP 请求并返回记录到的响应。
func do(t *testing.T, h http.Handler, method, target, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// decode 把成功响应体解析到 dst。
// 本项目的响应契约是：成功时直接返回对象，仅失败时才返回 {"error": "..."} 信封，
// 因此这里先确认没有 error 字段，再按目标类型解码。
func decode(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	var env struct {
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，原文：%s", err, w.Body.String())
	}
	if env.Error != "" {
		t.Fatalf("响应返回了错误：%s", env.Error)
	}
	if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
		t.Fatalf("解析响应数据失败：%v，原文：%s", err, w.Body.String())
	}
}

// errOf 取出失败响应中的错误描述。
func errOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("错误响应不是合法 JSON：%v，原文：%s", err, w.Body.String())
	}
	return env.Error
}

// TestImportDryRun 校验模式只解析不落库，覆盖路由注册与「解析 + service 校验」链路。
func TestImportDryRun(t *testing.T) {
	svc, h := newServer(t)

	res := do(t, h, "POST", "/api/workflows/import?dry_run=1", "text/yaml", sampleDoc)
	if res.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", res.Code, res.Body.String())
	}

	var wf model.Workflow
	decode(t, res, &wf)
	if wf.Name != "巡检流程" || wf.Description != "端到端往返" ||
		wf.Cron != "@every 1m" || wf.Group != "demo" || !wf.Enabled {
		t.Fatalf("基础字段解析结果不符：%+v", wf)
	}
	g, err := wf.ParseGraph()
	if err != nil {
		t.Fatalf("图解析失败：%v", err)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("应有 2 个节点，实际 %d", len(g.Nodes))
	}
	// depends_on: [n1] 应派生出 n1 -> n2 这条边。
	if len(g.Edges) != 1 || g.Edges[0].Source != "n1" || g.Edges[0].Target != "n2" {
		t.Fatalf("依赖未正确转成连线，实际：%+v", g.Edges)
	}
	if len(svc.data) != 0 {
		t.Fatalf("dry_run 不应落库，实际存有 %d 条", len(svc.data))
	}
}

// TestImportInvalid 各类非法文档都应返回 400 且不落库。
func TestImportInvalid(t *testing.T) {
	cases := map[string]string{
		"空文档":     "",
		"缺少 name": "nodes:\n  - id: n1\n    type: http\n",
		"未知节点类型":  "name: t\nnodes:\n  - id: n1\n    type: nope\n",
		"依赖不存在":   "name: t\nnodes:\n  - id: n1\n    type: http\n    depends_on: [n9]\n",
		"未知字段":    "name: t\nnodes:\n  - id: n1\n    type: http\n    typo_field: 1\n",
		"语法错误":    "name: t\nnodes: [\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, h := newServer(t)
			res := do(t, h, "POST", "/api/workflows/import?dry_run=1", "text/yaml", doc)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("期望 400，实际 %d：%s", res.Code, res.Body.String())
			}
			if errOf(t, res) == "" {
				t.Fatalf("错误响应应带上 error 描述，实际：%s", res.Body.String())
			}
			if len(svc.data) != 0 {
				t.Fatal("失败的导入不应落库")
			}
		})
	}
}

// TestImportCreates 不带 dry_run 时应真正创建工作流并回填 ID。
func TestImportCreates(t *testing.T) {
	svc, h := newServer(t)

	res := do(t, h, "POST", "/api/workflows/import", "text/yaml", sampleDoc)
	if res.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d：%s", res.Code, res.Body.String())
	}
	var wf model.Workflow
	decode(t, res, &wf)
	if wf.ID == 0 {
		t.Fatal("创建成功后应带回 ID")
	}
	if got := svc.data[wf.ID]; got == nil || got.Name != "巡检流程" {
		t.Fatalf("工作流未正确落库：%+v", got)
	}
}

// TestRenderDocRoundTrip 覆盖未保存内容的「表单 JSON → 文档 → 表单」往返。
// 这是前端「从表单生成 / 应用到表单」按钮所依赖的接口。
func TestRenderDocRoundTrip(t *testing.T) {
	_, h := newServer(t)

	// 1) 构造表单保存形态的工作流：图以 JSON 文本存放，连线显式声明。
	src := &model.Workflow{
		Name:        "表单巡检",
		Description: "由可视化表单保存",
		Enabled:     true,
		Cron:        "0 8 * * *",
		Group:       "demo",
	}
	if err := src.SetGraph(&model.Graph{
		Nodes: []model.NodeDef{
			{ID: "n1", Type: "http", Name: "探测", Config: map[string]any{
				"url": "https://httpbin.org/status/200", "method": "GET", "timeout": 10}},
			{ID: "n2", Type: "shell", Name: "通知", Config: map[string]any{
				"command": "echo done"}},
		},
		Edges: []model.EdgeDef{{Source: "n1", Target: "n2"}},
	}); err != nil {
		t.Fatalf("构造图失败：%v", err)
	}
	body, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("序列化请求体失败：%v", err)
	}

	// 2) 渲染为文档。
	res := do(t, h, "POST", "/api/workflows/export", "application/json", string(body))
	if res.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d：%s", res.Code, res.Body.String())
	}
	if ct := res.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Fatalf("响应类型应为 YAML，实际 %q", ct)
	}

	// 3) 文档解析回来应与原工作流等价：edges 被反解成 depends_on 再还原。
	back, err := dsl.Parse(res.Body.Bytes())
	if err != nil {
		t.Fatalf("解析导出的文档失败：%v\n文档：\n%s", err, res.Body.String())
	}
	assertSameWorkflow(t, src, back)

	// 4) 把解析结果再次导出，内容应完全一致，说明导出结果是规范形式。
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("序列化往返结果失败：%v", err)
	}
	res2 := do(t, h, "POST", "/api/workflows/export", "application/json", string(again))
	if res2.Code != http.StatusOK {
		t.Fatalf("二次渲染失败，实际 %d：%s", res2.Code, res2.Body.String())
	}
	if res.Body.String() != res2.Body.String() {
		t.Fatalf("导出结果不稳定：\n%s\n---\n%s", res.Body.String(), res2.Body.String())
	}
}

// TestExportByIDRoundTrip 覆盖已保存工作流的「落库 → 导出 → 再导入」往返。
func TestExportByIDRoundTrip(t *testing.T) {
	svc, h := newServer(t)

	// 先经导入接口落库，拿到 ID。
	created := do(t, h, "POST", "/api/workflows/import", "text/yaml", sampleDoc)
	if created.Code != http.StatusCreated {
		t.Fatalf("导入失败，实际 %d：%s", created.Code, created.Body.String())
	}
	var wf model.Workflow
	decode(t, created, &wf)

	// 按 ID 导出，再解析回来应与库中记录等价。
	res := do(t, h, "GET", "/api/workflows/"+strconv.FormatUint(uint64(wf.ID), 10)+"/export", "", "")
	if res.Code != http.StatusOK {
		t.Fatalf("导出失败，实际 %d：%s", res.Code, res.Body.String())
	}
	back, err := dsl.Parse(res.Body.Bytes())
	if err != nil {
		t.Fatalf("解析导出文档失败：%v\n文档：\n%s", err, res.Body.String())
	}
	assertSameWorkflow(t, svc.data[wf.ID], back)
}

// TestExportNotFound 导出不存在的工作流应返回 404。
func TestExportNotFound(t *testing.T) {
	_, h := newServer(t)
	res := do(t, h, "GET", "/api/workflows/999/export", "", "")
	if res.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d：%s", res.Code, res.Body.String())
	}
}

// assertSameWorkflow 校验两个工作流的字段与图结构等价。
// 边用集合比较：由 depends_on 派生出的边顺序可能与原始声明不同。
func assertSameWorkflow(t *testing.T, want, got *model.Workflow) {
	t.Helper()
	if want.Name != got.Name || want.Description != got.Description ||
		want.Cron != got.Cron || want.Group != got.Group || want.Enabled != got.Enabled {
		t.Fatalf("基础字段不一致：\n%+v\n%+v", want, got)
	}
	a, err := want.ParseGraph()
	if err != nil {
		t.Fatalf("解析原图失败：%v", err)
	}
	b, err := got.ParseGraph()
	if err != nil {
		t.Fatalf("解析往返图失败：%v", err)
	}
	if len(a.Nodes) != len(b.Nodes) {
		t.Fatalf("节点数量不一致：%d vs %d", len(a.Nodes), len(b.Nodes))
	}
	for i := range a.Nodes {
		if a.Nodes[i].ID != b.Nodes[i].ID ||
			a.Nodes[i].Type != b.Nodes[i].Type ||
			a.Nodes[i].Name != b.Nodes[i].Name {
			t.Fatalf("节点 %d 不一致：%+v vs %+v", i, a.Nodes[i], b.Nodes[i])
		}
	}
	if len(a.Edges) != len(b.Edges) {
		t.Fatalf("边数量不一致：%+v vs %+v", a.Edges, b.Edges)
	}
	wantEdges := make(map[string]bool, len(a.Edges))
	for _, e := range a.Edges {
		wantEdges[e.Source+">"+e.Target] = true
	}
	for _, e := range b.Edges {
		if !wantEdges[e.Source+">"+e.Target] {
			t.Fatalf("出现预期外的边 %s->%s（原始 %+v）", e.Source, e.Target, a.Edges)
		}
	}
}
