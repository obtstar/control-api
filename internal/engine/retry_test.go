// 熔断后自动重试（FINDING-027）：连败未达阈值置回 pending 后，
// 由 StartRetryLoop 周期扫描按退避表自动重跑；暂停/达阈值/无失败历史不动。
package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"control-api/internal/store"
	"control-api/internal/tasks"
)

// retryRunner 记录被调度的任务 ID（RunStage 立即成功）
type retryRunner struct{ calls chan string }

func (r *retryRunner) RunStage(m *tasks.Meta, _, _ string) (string, error) {
	r.calls <- m.TaskID
	return "ok", nil
}

// newRetryEnv 构建 :memory: store + 指定状态 task.md 的测试环境
func newRetryEnv(t *testing.T, status string) (*Engine, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	st.SetMaxOpenConns(1)
	t.Cleanup(func() { st.Close() })
	taskDir := filepath.Join(dir, "TASK-R1")
	if err := os.MkdirAll(taskDir, 0o750); err != nil {
		t.Fatal(err)
	}
	content := "---\ntask_id: TASK-R1\ntitle: 重试测试\nrepo_key: demo\nstage: coding\nstatus: " + status + "\nauthority: L1\n---\n\n# 重试测试\n"
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Engine{P: testPipeline(), St: st, TasksDir: dir}, st
}

// seedFailure 写一条 agent_error 并回退其 created_at 到 age 之前
func seedFailure(t *testing.T, st *store.Store, taskID string, age time.Duration) {
	t.Helper()
	if err := st.Log(taskID, "coding", store.ActionAgentError, "agent", "m", "boom"); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age).UTC().Format(time.RFC3339)
	if _, err := st.Exec(`UPDATE work_log SET created_at=? WHERE task_id=?`, when, taskID); err != nil {
		t.Fatal(err)
	}
}

func readStatus(t *testing.T, e *Engine) string {
	t.Helper()
	m, err := tasks.ParseFile(filepath.Join(e.TasksDir, "TASK-R1", "task.md"))
	if err != nil {
		t.Fatal(err)
	}
	return m.Status
}

func TestRetryOnce(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		failures   int
		age        time.Duration
		wantStatus string
		wantRun    bool
	}{
		{"退避到期自动重跑", "pending", 1, time.Hour, "running", true},
		{"退避未到期不动", "pending", 1, 0, "pending", false},
		{"达阈值留待人工", "pending", 3, time.Hour, "pending", false},
		{"暂停最高权限不动", "paused", 1, time.Hour, "paused", false},
		{"无失败历史不动", "pending", 0, 0, "pending", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			e, st := newRetryEnv(t, tt.status)
			for i := 0; i < tt.failures; i++ {
				seedFailure(t, st, "TASK-R1", tt.age)
			}
			rr := &retryRunner{calls: make(chan string, 1)}
			e.Runner = rr
			e.RetryOnce(time.Now())
			if got := readStatus(t, e); got != tt.wantStatus {
				t.Fatalf("status: got %s, want %s", got, tt.wantStatus)
			}
			select {
			case <-rr.calls:
				if !tt.wantRun {
					t.Fatal("Runner 不应被调用")
				}
			case <-time.After(2 * time.Second):
				if tt.wantRun {
					t.Fatal("Runner 应被调用")
				}
			}
		})
	}
}
