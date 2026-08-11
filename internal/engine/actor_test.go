// work_log 操作人归因测试（FINDING-007）：人工 approve/reject/pause/resume 的
// 全部相关条目（审批动作 + 状态迁移）operator 记真实操作人；
// 自动路径（Advance/MarkMerged）operator 仍为 agent/webhook 不变。
package engine

import (
	"testing"

	"control-api/internal/store"
	"control-api/internal/tasks"
)

// assertOperators 断言该任务全部 work_log 条目 operator 均为 want
func assertOperators(t *testing.T, st *store.Store, taskID, want string) {
	t.Helper()
	logs, err := st.ListLogs(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("work_log 为空")
	}
	for _, l := range logs {
		if l.TaskID == taskID && l.Operator != want {
			t.Fatalf("action=%s operator=%q, want %q", l.Action, l.Operator, want)
		}
	}
}

// 人工动作：approve/reject/pause/resume 后 work_log 全部条目归因到操作人
func TestHumanActionAttribution(t *testing.T) {
	cases := []struct {
		name  string
		stage string
		setup func(t *testing.T, st *store.Store, m *tasks.Meta)
		call  func(e *Engine, m *tasks.Meta) error
	}{
		{"approve", "coding", func(t *testing.T, st *store.Store, m *tasks.Meta) {
			m.Status = "awaiting_approval"
			if err := st.NewApproval(m.TaskID, "coding", "customer", "报告"); err != nil {
				t.Fatal(err)
			}
		}, func(e *Engine, m *tasks.Meta) error { return e.Approve(m, "通过", "dev") }},
		{"reject", "testing", func(t *testing.T, st *store.Store, m *tasks.Meta) {
			m.Status = "awaiting_approval"
			if err := st.NewApproval(m.TaskID, "testing", "tester", "报告"); err != nil {
				t.Fatal(err)
			}
		}, func(e *Engine, m *tasks.Meta) error { return e.Reject(m, "不通过", "tester") }},
		{"pause", "coding", func(t *testing.T, st *store.Store, m *tasks.Meta) {
			m.Status = "running"
		}, func(e *Engine, m *tasks.Meta) error { return e.Pause(m, "ops") }},
		{"resume", "coding", func(t *testing.T, st *store.Store, m *tasks.Meta) {
			m.Status = "paused"
		}, func(e *Engine, m *tasks.Meta) error { return e.Resume(m, "ops") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng, st, m := newTestEngine(t)
			m.Stage = c.stage
			c.setup(t, st, m)
			if err := c.call(eng, m); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"approve": "dev", "reject": "tester", "pause": "ops", "resume": "ops",
			}[c.name]
			assertOperators(t, st, m.TaskID, want)
		})
	}
}

// 自动路径：Advance 入待审批的状态迁移条目 operator 仍记 agent
func TestAutoAdvanceAttribution(t *testing.T) {
	eng, st, m := newTestEngine(t)
	if err := eng.Advance(m, "编码报告"); err != nil {
		t.Fatal(err)
	}
	assertOperators(t, st, m.TaskID, "agent")
}

// MarkMerged：merged 与 merged→deliver 条目 operator 均记 webhook
func TestMarkMergedAttribution(t *testing.T) {
	eng, st, m := newMergeEngine(t, "merge", "awaiting_approval")
	if err := st.NewApproval(m.TaskID, "merge", "team", "MR diff"); err != nil {
		t.Fatal(err)
	}
	if err := eng.MarkMerged(m.TaskID, "MR !123 by @teammate"); err != nil {
		t.Fatal(err)
	}
	assertOperators(t, st, m.TaskID, "webhook")
}
