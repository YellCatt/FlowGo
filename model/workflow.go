package model

import (
	"encoding/json"
	"fmt"
)

// Workflow 工作流定义，Graph 字段以 JSON 文本保存节点与连线。
type Workflow struct {
	ID          uint   `json:"id" gorm:"primarykey"`
	Name        string `json:"name" gorm:"size:128;not null"`
	Description string `json:"description" gorm:"size:512"`
	Enabled     bool   `json:"enabled" gorm:"default:true"`
	Graph       string `json:"graph" gorm:"type:text"`
	Cron        string `json:"cron" gorm:"size:64"`
	WebhookKey  string `json:"webhook_key" gorm:"size:64;uniqueIndex"`

	CreatedAt Time `json:"created_at"`
	UpdatedAt Time `json:"updated_at"`
}

// NodeDef 图中的一个节点定义，Config 为各类型节点自定义参数的原始 JSON。
type NodeDef struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Config map[string]any `json:"config,omitempty"`
}

// EdgeDef 图中的一条连线，Direction 保留用于后续扩展条件分支。
type EdgeDef struct {
	ID        string `json:"id,omitempty"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	Direction string `json:"direction,omitempty"`
}

// Graph 工作流的图结构，由节点与有向边组成。
type Graph struct {
	Nodes []NodeDef `json:"nodes"`
	Edges []EdgeDef `json:"edges"`
}

// ParseGraph 解析工作流保存的图 JSON，空内容返回空图而非报错。
func (w *Workflow) ParseGraph() (*Graph, error) {
	g := &Graph{Nodes: []NodeDef{}, Edges: []EdgeDef{}}
	if w.Graph == "" {
		return g, nil
	}
	if err := json.Unmarshal([]byte(w.Graph), g); err != nil {
		return nil, fmt.Errorf("workflow %d has invalid graph: %w", w.ID, err)
	}
	if g.Nodes == nil {
		g.Nodes = []NodeDef{}
	}
	if g.Edges == nil {
		g.Edges = []EdgeDef{}
	}
	return g, nil
}

// SetGraph 将图结构序列化后写回工作流。
func (w *Workflow) SetGraph(g *Graph) error {
	if g == nil {
		w.Graph = ""
		return nil
	}
	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("failed to encode graph: %w", err)
	}
	w.Graph = string(data)
	return nil
}
