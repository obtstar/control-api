// taskAction 的 action 白名单测试 + createTask（FINDING-009 竞态/注入、FINDING-034 操作人）
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"control-api/internal/config"
	"control-api/internal/store"
	"control-api/internal/tasks"
)

// newCreateTestServer 临时目录 tasks + 临时文件库（并发用文件库，不用 :memory:）
func newCreateTestServer(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &server{cfg: &config.Config{Paths: config.PathsConfig{TasksDir: filepath.Join(dir, "tasks")}}, st: st}
}

func postCreate(t *testing.T, s *server, body, xUser string) (int, map[string]string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	if xUser != "" {
		r.Header.Set("X-User", xUser)
	}
	w := httptest.NewRecorder()
	s.createTask(w, r)
	if w.Code == 200 { // 契约校验：CreateTaskResponse
		contractSpec(t).validateJSON(t, http.MethodPost, "/api/tasks", 200, w.Body.Bytes())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应非 JSON: %s", w.Body.String())
	}
	return w.Code, resp
}

// 并发创建 N 个任务：ID 全唯一（FINDING-009：Mkdir EEXIST 原子占号）
func TestCreateTaskConcurrentUniqueIDs(t *testing.T) {
	s := newCreateTestServer(t)
	const n = 16
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"title":"并发任务%d","body":"正文"}`, i)
			r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
			w := httptest.NewRecorder()
			s.createTask(w, r)
			if w.Code != 200 {
				t.Errorf("status = %d, body=%s", w.Code, w.Body.String())
				return
			}
			var resp map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Errorf("响应非 JSON: %v", err)
				return
			}
			ids[i] = resp["task_id"]
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			t.Fatal("存在创建失败的 goroutine")
		}
		if seen[id] {
			t.Fatalf("ID 重复: %s", id)
		}
		seen[id] = true
	}
}

// title 含冒号/引号/换行：frontmatter 仍合法且字段无损（FINDING-009 YAML 注入）
func TestCreateTaskYAMLInjectionTitle(t *testing.T) {
	s := newCreateTestServer(t)
	title := "需求: 支持 \"quoted\" 值\n第二行"
	code, resp := postCreate(t, s,
		`{"title":"需求: 支持 \"quoted\" 值\n第二行","repo_key":"demo","body":"正文"}`, "")
	if code != 200 {
		t.Fatalf("status = %d, resp=%v", code, resp)
	}
	m, err := tasks.ParseFile(filepath.Join(resp["path"], "task.md"))
	if err != nil {
		t.Fatalf("task.md 解析失败（frontmatter 被注入破坏）: %v", err)
	}
	if m.Title != title {
		t.Errorf("Title = %q, want %q", m.Title, title)
	}
	if m.TaskID != resp["task_id"] || m.RepoKey != "demo" || m.Status != "pending" || m.Authority != "L1" {
		t.Errorf("字段有损: %+v", m)
	}
}

// operator 记 X-User 真实用户（FINDING-034）；缺省回落 human
func TestCreateTaskOperatorFromXUser(t *testing.T) {
	s := newCreateTestServer(t)
	if code, _ := postCreate(t, s, `{"title":"t1","body":"b"}`, "alice"); code != 200 {
		t.Fatalf("status = %d", code)
	}
	if code, _ := postCreate(t, s, `{"title":"t2","body":"b"}`, ""); code != 200 {
		t.Fatalf("status = %d", code)
	}
	rows, err := s.st.ListLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		if r.Action == "create" {
			got[r.TaskID] = r.Operator
		}
	}
	if got["TASK-001"] != "alice" {
		t.Errorf("TASK-001 operator = %q, want alice", got["TASK-001"])
	}
	if got["TASK-002"] != "human" {
		t.Errorf("TASK-002 operator = %q, want human（无 X-User 回落）", got["TASK-002"])
	}
}

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
