// merge webhook 端点测试（FINDING-003）：独立共享密钥认证，表驱动覆盖
// 503 未启用 / 401 token 错误 / 400 event 非法 / 409 状态冲突 / 200 合法。
package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"control-api/internal/config"
	"control-api/internal/engine"
	"control-api/internal/pipeline"
	"control-api/internal/store"
)

func mergeTestPipeline() *pipeline.Pipeline {
	return &pipeline.Pipeline{Stages: []pipeline.Stage{
		{ID: "testing", Approval: "required", OnReject: "back_to_coding"},
		{ID: "merge", Approval: "team_mr_review", Webhook: "merge_event"},
		{ID: "deliver", Approval: "auto"},
	}}
}

// newWebhookServer 构建 webhook 测试服务：指定 secret + 单个指定 stage/status 的任务
func newWebhookServer(t *testing.T, secret, stage, status string) *server {
	t.Helper()
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "TASK-001")
	if err := os.MkdirAll(taskDir, 0o750); err != nil {
		t.Fatal(err)
	}
	content := "---\ntask_id: TASK-001\ntitle: 测试任务\nstage: " + stage + "\nstatus: " + status + "\nauthority: L1\n---\n\n# 测试任务\n"
	if err := os.WriteFile(filepath.Join(taskDir, "task.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	st.SetMaxOpenConns(1)
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		Server: config.ServerConfig{WebhookSecret: secret},
		Paths:  config.PathsConfig{TasksDir: dir},
	}
	return &server{cfg: cfg, st: st, eng: &engine.Engine{P: mergeTestPipeline(), St: st, TasksDir: dir}}
}

func TestMergeEventWebhook(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		token  string
		body   string
		stage  string
		status string
		want   int
	}{
		{"未配置 secret 一律 503", "", "", `{"task_id":"TASK-001","event":"merged"}`, "merge", "awaiting_approval", 503},
		{"token 错误 401", "s3cret", "wrong", `{"task_id":"TASK-001","event":"merged"}`, "merge", "awaiting_approval", 401},
		{"缺 token 401", "s3cret", "", `{"task_id":"TASK-001","event":"merged"}`, "merge", "awaiting_approval", 401},
		{"event 非 merged 400", "s3cret", "s3cret", `{"task_id":"TASK-001","event":"closed"}`, "merge", "awaiting_approval", 400},
		{"缺 task_id 400", "s3cret", "s3cret", `{"event":"merged"}`, "merge", "awaiting_approval", 400},
		{"任务不在 merge 等待态 409", "s3cret", "s3cret", `{"task_id":"TASK-001","event":"merged"}`, "merge", "running", 409},
		{"任务不存在 409", "s3cret", "s3cret", `{"task_id":"TASK-999","event":"merged"}`, "merge", "awaiting_approval", 409},
		{"合法 merged 200", "s3cret", "s3cret", `{"task_id":"TASK-001","event":"merged","detail":"MR !123 by @teammate"}`, "merge", "awaiting_approval", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newWebhookServer(t, c.secret, c.stage, c.status)
			if c.status == "awaiting_approval" {
				if err := s.st.NewApproval("TASK-001", c.stage, "team", "artifact"); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest(http.MethodPost, "/api/webhooks/merge-event", strings.NewReader(c.body))
			if c.token != "" {
				r.Header.Set("X-Webhook-Token", c.token)
			}
			w := httptest.NewRecorder()
			s.mergeEvent(w, r)
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, c.want, w.Body.String())
			}
		})
	}
}

// webhook 端点不经 Bearer 会话拦截（withAuth 放行 /api/webhooks/ 前缀，
// 由 handler 内自验共享密钥；未配置 secret 时 503 即证明未被 401 拦下）
func TestWebhookBypassesBearerAuth(t *testing.T) {
	s := newWebhookServer(t, "", "merge", "awaiting_approval")
	r := httptest.NewRequest(http.MethodPost, "/api/webhooks/merge-event",
		strings.NewReader(`{"task_id":"TASK-001","event":"merged"}`))
	w := httptest.NewRecorder()
	s.withAuth(http.HandlerFunc(s.mergeEvent)).ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("webhook 应走独立密钥认证（不受 Bearer 拦截），status = %d", w.Code)
	}
}
