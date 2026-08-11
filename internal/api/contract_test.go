// 契约对账测试：路由表（server.go routes()）与 docs/api/openapi.yaml 双向相等，
// 及各端点 200 响应的 schema 校验（findings/webhook/create 的 200 校验挂钩在
// endpoints_test.go / tasks_test.go 的既有用例中）。
package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"control-api/internal/authn"
	"control-api/internal/config"
	"control-api/internal/engine"
	"control-api/internal/store"
	"control-api/internal/tasks"
)

// knownExemptions 实现有、契约没有的历史豁免路由（显式列出，不允许隐式漏）：
// GET /actuator/health 为运维探活端点，早于契约存在，web 端不消费，不进 openapi.yaml。
var knownExemptions = map[string]bool{
	"GET /actuator/health": true,
}

// operations 从契约 paths 展开 {METHOD /api<path>} 集合（前缀取 servers[0].url）
func (c *contract) operations() map[string]bool {
	prefix := ""
	if servers, ok := c.doc["servers"].([]any); ok && len(servers) > 0 {
		prefix, _ = servers[0].(map[string]any)["url"].(string)
	}
	out := map[string]bool{}
	paths, _ := c.doc["paths"].(map[string]any)
	for p, raw := range paths {
		item, _ := raw.(map[string]any)
		for m := range item {
			switch m {
			case "get", "post", "put", "delete", "patch", "head", "options":
				out[strings.ToUpper(m)+" "+prefix+p] = true
			}
		}
	}
	return out
}

// 双向对账：契约有的必须有实现，实现有的必须在契约（豁免项除外）。
// 挡住“实现先行、契约漂移”与“契约承诺、实现缺失”两个方向。
func TestRouteReconciliation(t *testing.T) {
	spec := contractSpec(t)
	contractRoutes := spec.operations()
	if len(contractRoutes) == 0 {
		t.Fatal("契约 paths 解析为空")
	}
	implRoutes := map[string]bool{}
	for _, r := range (&server{}).routes() {
		implRoutes[r.pattern] = true
	}
	for route := range contractRoutes {
		if !implRoutes[route] {
			t.Errorf("契约有、实现无: %s（在 server.go routes() 补实现或修正契约）", route)
		}
	}
	for route := range implRoutes {
		if !contractRoutes[route] && !knownExemptions[route] {
			t.Errorf("实现有、契约无: %s（同步 docs/api/openapi.yaml 或加入 knownExemptions 并注释原因）", route)
		}
	}
}

func openMemoryStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	st.SetMaxOpenConns(1)
	t.Cleanup(func() { st.Close() })
	return st
}

// POST /api/auth/login 200 → LoginResponse
func TestContractLoginResponse(t *testing.T) {
	st := openMemoryStore(t)
	a := &authn.Auth{St: st}
	if err := a.CreateUser("alice", "password123", "admin"); err != nil {
		t.Fatal(err)
	}
	s := &server{st: st, auth: a}
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"alice","password":"password123"}`))
	w := httptest.NewRecorder()
	s.login(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	contractSpec(t).validateJSON(t, http.MethodPost, "/api/auth/login", 200, w.Body.Bytes())
}

// GET /api/tasks 200 → Task 数组
func TestContractListTasksResponse(t *testing.T) {
	st := openMemoryStore(t)
	m := &tasks.Meta{TaskID: "TASK-001", Title: "契约任务", Stage: "design",
		Status: "awaiting_approval", Authority: "L1"}
	if err := st.UpsertTask(m); err != nil {
		t.Fatal(err)
	}
	s := &server{st: st}
	w := httptest.NewRecorder()
	s.listTasks(w, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	contractSpec(t).validateJSON(t, http.MethodGet, "/api/tasks", 200, w.Body.Bytes())
}

// GET /api/approvals/pending 200 → PendingApproval 数组
func TestContractPendingApprovalsResponse(t *testing.T) {
	st := openMemoryStore(t)
	m := &tasks.Meta{TaskID: "TASK-001", Title: "契约任务", Stage: "design",
		Status: "awaiting_approval", Authority: "L1"}
	if err := st.UpsertTask(m); err != nil {
		t.Fatal(err)
	}
	if err := st.NewApproval("TASK-001", "design", "designer", "design.md"); err != nil {
		t.Fatal(err)
	}
	s := &server{st: st}
	r := httptest.NewRequest(http.MethodGet, "/api/approvals/pending", nil)
	r.Header.Set("X-Role", "designer")
	w := httptest.NewRecorder()
	s.listPendingApprovals(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	contractSpec(t).validateJSON(t, http.MethodGet, "/api/approvals/pending", 200, w.Body.Bytes())
}

// GET /api/audit 200 → AuditLog 数组
func TestContractAuditResponse(t *testing.T) {
	st := openMemoryStore(t)
	if err := st.Log("TASK-001", "design", "create", "alice", "", "详情"); err != nil {
		t.Fatal(err)
	}
	s := &server{st: st}
	w := httptest.NewRecorder()
	s.listLogs(w, httptest.NewRequest(http.MethodGet, "/api/audit", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	contractSpec(t).validateJSON(t, http.MethodGet, "/api/audit", 200, w.Body.Bytes())
}

// POST /api/tasks/{id}/action 200 → ActionResponse（pause：任何状态可触发，不经角色路由）
func TestContractTaskActionResponse(t *testing.T) {
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "TASK-001")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntask_id: TASK-001\ntitle: 契约任务\nstage: design\nstatus: running\nauthority: L1\n---\n\n# 契约任务\n"
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	st := openMemoryStore(t)
	s := &server{
		cfg: &config.Config{Paths: config.PathsConfig{TasksDir: dir}},
		st:  st,
		eng: &engine.Engine{P: mergeTestPipeline(), St: st, TasksDir: dir},
	}
	r := httptest.NewRequest(http.MethodPost, "/api/tasks/TASK-001/action",
		strings.NewReader(`{"action":"pause"}`))
	r.SetPathValue("id", "TASK-001")
	r.Header.Set("X-User", "alice")
	w := httptest.NewRecorder()
	s.taskAction(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	contractSpec(t).validateJSON(t, http.MethodPost, "/api/tasks/{id}/action", 200, w.Body.Bytes())
	if got := fmt.Sprintf("%s", w.Body); !strings.Contains(got, `"status":"paused"`) {
		t.Fatalf("pause 后状态应 paused: %s", got)
	}
}
