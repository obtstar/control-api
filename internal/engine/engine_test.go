// engine 状态机测试：Reject 状态校验（FINDING-011）、
// 非法流转覆盖（FINDING-012）。:memory: SQLite 实测，不 mock。
package engine

import (
	"os"
	"path/filepath"
	"testing"

	"control-api/internal/pipeline"
	"control-api/internal/store"
	"control-api/internal/tasks"
)

func testPipeline() *pipeline.Pipeline {
	return &pipeline.Pipeline{Stages: []pipeline.Stage{
		{ID: "requirements", Approval: "required", OnReject: "retry"},
		{ID: "coding", Approval: "required", OnReject: "retry"},
		{ID: "testing", Approval: "required", OnReject: "back_to_coding"},
		{ID: "deliver", Approval: "auto"},
	}}
}

// newTestEngine 构建 :memory: store + 临时目录 task.md 的测试环境（阶段 coding、状态 running）
func newTestEngine(t *testing.T) (*Engine, *store.Store, *tasks.Meta) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	st.SetMaxOpenConns(1) // :memory: 每连接一个库，限单连接保证一致
	t.Cleanup(func() { st.Close() })
	taskDir := filepath.Join(dir, "TASK-001")
	if err := os.MkdirAll(taskDir, 0o750); err != nil {
		t.Fatal(err)
	}
	content := "---\ntask_id: TASK-001\ntitle: 测试任务\nrepo_key: demo\nstage: coding\nstatus: running\nauthority: L1\n---\n\n# 测试任务\n"
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := &Engine{P: testPipeline(), St: st, TasksDir: dir}
	m, err := tasks.ParseFile(filepath.Join(taskDir, "task.md"))
	if err != nil {
		t.Fatal(err)
	}
	return eng, st, m
}

// 非法状态流转全部报错（Approve/Reject/Resume/Pause）
func TestIllegalTransitions(t *testing.T) {
	cases := []struct {
		name   string
		status string
		call   func(e *Engine, m *tasks.Meta) error
	}{
		{"approve: running", "running", func(e *Engine, m *tasks.Meta) error { return e.Approve(m, "ok", "u") }},
		{"approve: paused", "paused", func(e *Engine, m *tasks.Meta) error { return e.Approve(m, "ok", "u") }},
		{"reject: running", "running", func(e *Engine, m *tasks.Meta) error { return e.Reject(m, "原因", "u") }},
		{"reject: paused", "paused", func(e *Engine, m *tasks.Meta) error { return e.Reject(m, "原因", "u") }},
		{"reject: delivered", "delivered", func(e *Engine, m *tasks.Meta) error { return e.Reject(m, "原因", "u") }},
		{"reject: 空批注", "awaiting_approval", func(e *Engine, m *tasks.Meta) error { return e.Reject(m, "", "u") }},
		{"resume: running", "running", func(e *Engine, m *tasks.Meta) error { return e.Resume(m, "u") }},
		{"resume: awaiting_approval", "awaiting_approval", func(e *Engine, m *tasks.Meta) error { return e.Resume(m, "u") }},
		{"pause: 已暂停", "paused", func(e *Engine, m *tasks.Meta) error { return e.Pause(m, "u") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng, _, m := newTestEngine(t)
			m.Status = c.status
			if err := c.call(eng, m); err == nil {
				t.Fatalf("status=%s 应报错", c.status)
			}
		})
	}
}

// 合法驳回：打回 on_reject 目标阶段并置 running
func TestRejectToTarget(t *testing.T) {
	cases := []struct {
		stage      string
		wantTarget string
	}{
		{"testing", "coding"},            // on_reject: back_to_coding
		{"requirements", "requirements"}, // on_reject: retry（重做本阶段）
	}
	for _, c := range cases {
		t.Run(c.stage+"→"+c.wantTarget, func(t *testing.T) {
			eng, st, m := newTestEngine(t)
			m.Stage = c.stage
			m.Status = "awaiting_approval"
			if err := st.NewApproval(m.TaskID, c.stage, "tester", "报告"); err != nil {
				t.Fatal(err)
			}
			if err := eng.Reject(m, "不通过", "tester"); err != nil {
				t.Fatal(err)
			}
			if m.Stage != c.wantTarget || m.Status != "running" {
				t.Fatalf("stage=%s status=%s, want %s/running", m.Stage, m.Status, c.wantTarget)
			}
			got := eng.reload(m.TaskID) // 状态已回写权威 task.md
			if got.Stage != c.wantTarget || got.Status != "running" {
				t.Fatalf("task.md: stage=%s status=%s", got.Stage, got.Status)
			}
		})
	}
}
