// taskAction 的 action 白名单测试
package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"control-api/internal/config"
	"control-api/internal/store"
	"control-api/internal/tasks"
)

// advance 已从 HTTP 动作集移除（审批闸后门 FINDING-001）：
// action=advance 与未知动作必须返回 400，且不得改动任务状态
func TestTaskActionRejectsUnknownActions(t *testing.T) {
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "TASK-001")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntask_id: TASK-001\ntitle: 测试任务\nstage: design\nstatus: awaiting_approval\nauthority: L1\n---\n\n# 测试任务\n"
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// eng 为 nil：合法白名单外的 action 必须在进入状态机前被 400 拦截
	s := &server{cfg: &config.Config{Paths: config.PathsConfig{TasksDir: dir}}, st: st}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"advance 不允许经 HTTP 调用", `{"action":"advance","artifact":"x.md"}`, 400},
		{"未知动作", `{"action":"teleport"}`, 400},
		{"空动作", `{}`, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/tasks/TASK-001/action", strings.NewReader(c.body))
			r.SetPathValue("id", "TASK-001")
			w := httptest.NewRecorder()
			s.taskAction(w, r)
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, c.want, w.Body.String())
			}
			m, err := tasks.ParseFile(filepath.Join(taskDir, "task.md"))
			if err != nil {
				t.Fatal(err)
			}
			if m.Status != "awaiting_approval" || m.Stage != "design" {
				t.Fatalf("任务状态被改动: stage=%s status=%s", m.Stage, m.Status)
			}
		})
	}
}
