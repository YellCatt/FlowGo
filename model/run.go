package model

// 运行实例与节点日志的状态取值。
const (
	RunStatusPending = "pending"
	RunStatusRunning = "running"
	RunStatusSuccess = "success"
	RunStatusFailed  = "failed"

	// TriggerManual 手动触发。
	TriggerManual = "manual"
	// TriggerWebhook Webhook 触发。
	TriggerWebhook = "webhook"
	// TriggerCron 定时调度触发。
	TriggerCron = "cron"
)

// Run 一次工作流执行实例。
type Run struct {
	ID         uint   `json:"id" gorm:"primarykey"`
	WorkflowID uint   `json:"workflow_id" gorm:"index"`
	Workflow   string `json:"workflow_name" gorm:"size:128"`
	Status     string `json:"status" gorm:"size:16;index"`
	Trigger    string `json:"trigger" gorm:"size:16"`
	Payload    string `json:"payload" gorm:"type:text"`
	Error      string `json:"error,omitempty" gorm:"type:text"`
	StartedAt  *Time  `json:"started_at,omitempty"`
	FinishedAt *Time  `json:"finished_at,omitempty"`
	Duration   int64  `json:"duration_ms"`

	CreatedAt Time `json:"created_at"`
	UpdatedAt Time `json:"updated_at"`
}

// StepLog 单个节点的执行日志。
type StepLog struct {
	ID         uint   `json:"id" gorm:"primarykey"`
	RunID      uint   `json:"run_id" gorm:"index"`
	NodeID     string `json:"node_id" gorm:"size:64"`
	NodeName   string `json:"node_name" gorm:"size:128"`
	NodeType   string `json:"node_type" gorm:"size:32"`
	Status     string `json:"status" gorm:"size:16"`
	Input      string `json:"input,omitempty" gorm:"type:text"`
	Output     string `json:"output,omitempty" gorm:"type:text"`
	Error      string `json:"error,omitempty" gorm:"type:text"`
	StartedAt  Time   `json:"started_at"`
	FinishedAt Time   `json:"finished_at"`
	Duration   int64  `json:"duration_ms"`
}

// RunDetail 运行详情，附加该次运行的全部节点日志。
type RunDetail struct {
	Run
	Steps []StepLog `json:"steps" gorm:"-"`
}
