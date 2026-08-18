// merge 阶段终审链路测试（FINDING-003）：team_mr_review 闸、Web 审批守卫、
// MarkMerged 停留 merged（FINDING-029：已合并待交付看板可见）与 deliver 人工确认推进。
// :memory: SQLite 实测，runner 用假实现不拉起 pi。
package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"control-api/internal/pipeline"
	"control-api/internal/store"
	"control-api/internal/tasks"
)

func mergePipeline() *pipeline.Pipeline {
	return &pipeline.Pipeline{Stages: []pipeline.Stage{
		{ID: "testing", Approval: "required", OnReject: "back_to_coding"},
		{ID: "merge", Approval: "team_mr_review", Webhook: "merge_event", DoneWhen: "merged_by_teammate"},
		{ID: "deliver", Approval: "auto"},
	}}
}

// fakeRunner 立即返回产物的阶段执行器（不拉起真实 pi 子进程）
type fakeRunner struct{ artifact string }

func (f fakeRunner) RunStage(m *tasks.Meta, stage, model string) (string, error) {
	return f.artifact, nil
}

// newMergeEngine 构建 merge 链路测试环境：指定 stage/status 的 task.md + :memory: store
func newMergeEngine(t *testing.T, stage, status string) (*Engine, *store.Store, *tasks.Meta) {
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
	content := fmt.Sprintf("---\ntask_id: TASK-001\ntitle: 测试任务\nrepo_key: demo\nstage: %s\nstatus: %s\nauthority: L1\n---\n\n# 测试任务\n", stage, status)
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := &Engine{P: mergePipeline(), St: st, TasksDir: dir}
	m, err := tasks.ParseFile(filepath.Join(taskDir, "task.md"))
	if err != nil {
		t.Fatal(err)
	}
	return eng, st, m
}

// testing 审批通过 → merge 阶段直接入 awaiting_approval（team_mr_review 无 agent 执行，
// 不经 running），待审批记录角色为 team
func TestApproveToMergeEntersTeamReviewWait(t *testing.T) {
	eng, st, m := newMergeEngine(t, "testing", "awaiting_approval")
	if err := st.NewApproval(m.TaskID, "testing", "tester", "测试报告"); err != nil {
		t.Fatal(err)
	}
	if err := eng.Approve(m, "通过", "tester"); err != nil {
		t.Fatal(err)
	}
	if m.Stage != "merge" || m.Status != "awaiting_approval" {
		t.Fatalf("stage=%s status=%s, want merge/awaiting_approval", m.Stage, m.Status)
	}
	got := eng.reload(m.TaskID) // 状态已回写权威 task.md
	if got.Stage != "merge" || got.Status != "awaiting_approval" {
		t.Fatalf("task.md: stage=%s status=%s", got.Stage, got.Status)
	}
	role, err := st.ApprovalRoleOf(m.TaskID)
	if err != nil || role != "team" {
		t.Fatalf("merge 待审批角色 = %q, want team (err=%v)", role, err)
	}
}

// merge 阶段不接受 Web 审批动作（approve/reject 均报错，状态不变）
func TestWebApprovalRejectedOnMergeStage(t *testing.T) {
	cases := []struct {
		name string
		call func(e *Engine, m *tasks.Meta) error
	}{
		{"approve", func(e *Engine, m *tasks.Meta) error { return e.Approve(m, "ok", "admin") }},
		{"reject", func(e *Engine, m *tasks.Meta) error { return e.Reject(m, "打回", "admin") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng, st, m := newMergeEngine(t, "merge", "awaiting_approval")
			if err := st.NewApproval(m.TaskID, "merge", "team", "MR diff"); err != nil {
				t.Fatal(err)
			}
			if err := c.call(eng, m); err == nil {
				t.Fatal("merge 阶段 Web 审批应被拒绝")
			}
			got := eng.reload(m.TaskID)
			if got.Stage != "merge" || got.Status != "awaiting_approval" {
				t.Fatalf("状态被改动: stage=%s status=%s", got.Stage, got.Status)
			}
		})
	}
}

// MarkMerged 状态守卫：仅 merge 阶段 awaiting_approval 可流转；
// 合法流转后停留 merge/merged（FINDING-029：已合并待交付，看板可感知，
// 不再同请求内瞬态推进 deliver），work_log 留有 operator=webhook 的 merged 条目
func TestMarkMergedTransitions(t *testing.T) {
	cases := []struct {
		name    string
		stage   string
		status  string
		wantErr bool
	}{
		{"merge 等待态可标记合并", "merge", "awaiting_approval", false},
		{"merge running 不可", "merge", "running", true},
		{"非 merge 阶段不可", "testing", "awaiting_approval", true},
		{"已交付不可", "deliver", "delivered", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng, st, m := newMergeEngine(t, c.stage, c.status)
			if c.status == "awaiting_approval" {
				if err := st.NewApproval(m.TaskID, c.stage, "team", "artifact"); err != nil {
					t.Fatal(err)
				}
			}
			err := eng.MarkMerged(m.TaskID, "MR !123 by @teammate")
			if c.wantErr {
				if err == nil {
					t.Fatal("应报错")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := eng.reload(m.TaskID)
			if got.Stage != "merge" || got.Status != "merged" {
				t.Fatalf("MarkMerged 后应停留 merge/merged: stage=%s status=%s", got.Stage, got.Status)
			}
			logs, err := st.ListLogs(10)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, l := range logs {
				if l.Action == "merged" && l.Operator == "webhook" {
					found = true
				}
			}
			if !found {
				t.Fatal("work_log 缺少 operator=webhook 的 merged 条目")
			}
		})
	}
}

// 任务不存在时 MarkMerged 报错
func TestMarkMergedUnknownTask(t *testing.T) {
	eng, _, _ := newMergeEngine(t, "merge", "awaiting_approval")
	if err := eng.MarkMerged("TASK-999", "x"); err == nil {
		t.Fatal("任务不存在应报错")
	}
}

// Deliver 状态守卫（FINDING-029）：仅 merge 阶段 merged 状态可确认交付
func TestDeliverGuard(t *testing.T) {
	cases := []struct {
		name   string
		stage  string
		status string
	}{
		{"merge 等待审批不可", "merge", "awaiting_approval"},
		{"merge running 不可", "merge", "running"},
		{"testing 阶段不可", "testing", "awaiting_approval"},
		{"已交付不可重复", "deliver", "delivered"},
		{"暂停中不可（暂停为最高运行时权限）", "merge", "paused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng, _, m := newMergeEngine(t, c.stage, c.status)
			if err := eng.Deliver(m, "alice"); err == nil {
				t.Fatal("应报错")
			}
			got := eng.reload(m.TaskID)
			if got.Stage != c.stage || got.Status != c.status {
				t.Fatalf("状态被改动: stage=%s status=%s", got.Stage, got.Status)
			}
		})
	}
}

// merged 停留后人工 Deliver 确认 → deliver（auto）由 fakeRunner 立即完成 → delivered；
// work_log 依次留 merged(webhook)、deliver(alice) 两条归因条目
func TestDeliverAfterMerged(t *testing.T) {
	eng, st, m := newMergeEngine(t, "merge", "awaiting_approval")
	if err := st.NewApproval(m.TaskID, "merge", "team", "MR diff"); err != nil {
		t.Fatal(err)
	}
	eng.Runner = fakeRunner{artifact: "交付归档"}
	if err := eng.MarkMerged(m.TaskID, "MR !123 by @teammate"); err != nil {
		t.Fatal(err)
	}
	if got := eng.reload(m.TaskID); got.Stage != "merge" || got.Status != "merged" {
		t.Fatalf("MarkMerged 后应停留 merge/merged: %+v", got)
	}
	m = eng.reload(m.TaskID) // MarkMerged 内部自 reload 推进，持最新状态再 Deliver（同 handler 先解析再调用的口径）
	if err := eng.Deliver(m, "alice"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second) // maybeRun 异步执行，轮询等终态
	for {
		got := eng.reload(m.TaskID)
		if got != nil && got.Stage == "deliver" && got.Status == "delivered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deliver 未完成: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	logs, err := st.ListLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	var mergedOp, deliverOp string
	for _, l := range logs {
		switch l.Action {
		case "merged":
			mergedOp = l.Operator
		case "deliver":
			deliverOp = l.Operator
		}
	}
	if mergedOp != "webhook" || deliverOp != "alice" {
		t.Fatalf("归因错误: merged=%q deliver=%q, want webhook/alice", mergedOp, deliverOp)
	}
}
