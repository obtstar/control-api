// findings 与 merge webhook 端点测试（合并自原 findings_test.go / webhook_test.go，
// 为契约测试层腾包文件位）：FINDINGS.md 解析、webhook 独立共享密钥认证（FINDING-003）。
// 200 响应均接入契约 schema 校验（contract_schema_test.go）。
package api

import (
	"encoding/json"
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

// ── findings ────────────────────────────────────────────────

const findingsFixture = `# FINDINGS — 平台发现问题一览

| ID | 日期 | 来源 | 现象 | 证据 | 影响 | 状态 | 去向 |
|----|------|------|------|------|------|------|------|
| FINDING-001 | 2026-08-09 | 架构评审 | 现象甲 | 证据甲 | 影响甲 | open | |
| FINDING-002 | 2026-08-09 | web 核查 | 现象乙 | | 影响乙 | fixed | control-web abc1234 |

## 已修复（留存痕）

- 2026-08-09：非表格行，应忽略
`

func writeFindingsFile(t *testing.T, home string, content string) {
	t.Helper()
	dir := filepath.Join(home, "control-center", "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "FINDINGS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseFindings(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"正常两行", findingsFixture, 2},
		{"空内容", "", 0},
		{"仅表头与分隔行", "| ID | 日期 |\n|----|------|\n", 0},
		{"非 FINDING 开头行跳过", "| TASK-001 | a | b | c | d | e | f | g |\n", 0},
		{"列数不足跳过", "| FINDING-001 | 只有两列 |\n", 0},
		{"混杂行只取数据行", "前言\n" + findingsFixture + "\n尾注\n", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseFindings([]byte(c.input)); len(got) != c.want {
				t.Fatalf("len = %d, want %d", len(got), c.want)
			}
		})
	}
}

// 字段级断言：列映射正确，状态值原样透传，空单元格保留为空串
func TestParseFindingsFields(t *testing.T) {
	got := parseFindings([]byte(findingsFixture))
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	f0 := got[0]
	if f0.ID != "FINDING-001" || f0.Date != "2026-08-09" || f0.Source != "架构评审" ||
		f0.Phenomenon != "现象甲" || f0.Evidence != "证据甲" || f0.Impact != "影响甲" ||
		f0.Status != "open" || f0.Target != "" {
		t.Fatalf("字段映射错误: %+v", f0)
	}
	if got[1].Status != "fixed" || got[1].Target != "control-web abc1234" || got[1].Evidence != "" {
		t.Fatalf("状态透传/空列错误: %+v", got[1])
	}
}

// FINDING-028：单元格内 `\|` 转义不得参与切分（还原为字面 |），列对齐不变
func TestParseFindingsEscapedPipe(t *testing.T) {
	row := "| FINDING-028 | 2026-08-09 | 问题一览 | 含 `\\|` 转义的单元格 | findings.go | 解析错位 | open | |\n"
	got := parseFindings([]byte(row))
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	f := got[0]
	if f.Phenomenon != "含 `|` 转义的单元格" {
		t.Errorf("Phenomenon 转义还原错误: %q", f.Phenomenon)
	}
	if f.Evidence != "findings.go" || f.Impact != "解析错位" || f.Status != "open" {
		t.Errorf("列错位: %+v", f)
	}
}

func TestListFindings(t *testing.T) {
	newServer := func(home string) *server {
		return &server{cfg: &config.Config{Paths: config.PathsConfig{
			Home:         home,
			FindingsPath: filepath.Join(home, "control-center", "docs", "FINDINGS.md"),
		}}}
	}

	t.Run("文件缺失返回 500", func(t *testing.T) {
		s := newServer(t.TempDir())
		w := httptest.NewRecorder()
		s.listFindings(w, httptest.NewRequest(http.MethodGet, "/api/findings", nil))
		if w.Code != 500 {
			t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("正常解析返回 200", func(t *testing.T) {
		home := t.TempDir()
		writeFindingsFile(t, home, findingsFixture)
		s := newServer(home)
		w := httptest.NewRecorder()
		s.listFindings(w, httptest.NewRequest(http.MethodGet, "/api/findings", nil))
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
		}
		contractSpec(t).validateJSON(t, http.MethodGet, "/api/findings", 200, w.Body.Bytes())
		var got []finding
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].ID != "FINDING-001" || got[1].Status != "fixed" {
			t.Fatalf("响应内容错误: %s", w.Body.String())
		}
	})

	t.Run("无数据行返回空数组而非 null", func(t *testing.T) {
		home := t.TempDir()
		writeFindingsFile(t, home, "# 空表\n")
		s := newServer(home)
		w := httptest.NewRecorder()
		s.listFindings(w, httptest.NewRequest(http.MethodGet, "/api/findings", nil))
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var got []finding
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("响应不是 JSON 数组: %s", w.Body.String())
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("got = %v, want 空数组", got)
		}
	})
}

// ── merge webhook ───────────────────────────────────────────

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
			if w.Code == 200 {
				contractSpec(t).validateJSON(t, http.MethodPost, "/api/webhooks/merge-event", 200, w.Body.Bytes())
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

// ── openapi.yaml 自指端点 ───────────────────────────────────

// GET /api/openapi.yaml：200 返回契约本体（ContractPath 直指仓内契约文件，上两级即仓根）
func TestServeOpenAPI(t *testing.T) {
	t.Run("200 返回契约文件原文", func(t *testing.T) {
		s := &server{cfg: &config.Config{Paths: config.PathsConfig{
			ContractPath: "../../docs/api/openapi.yaml",
		}}}
		w := httptest.NewRecorder()
		s.serveOpenAPI(w, httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil))
		if w.Code != 200 {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/yaml") {
			t.Fatalf("Content-Type = %s, want text/yaml", ct)
		}
		if !strings.Contains(w.Body.String(), "openapi: 3.1.0") {
			t.Fatalf("响应应含契约头 openapi: 3.1.0: %.80s", w.Body.String())
		}
	})

	t.Run("契约文件缺失返回 500", func(t *testing.T) {
		s := &server{cfg: &config.Config{Paths: config.PathsConfig{
			ContractPath: filepath.Join(t.TempDir(), "none.yaml"),
		}}}
		w := httptest.NewRecorder()
		s.serveOpenAPI(w, httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil))
		if w.Code != 500 {
			t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
		}
	})
}
