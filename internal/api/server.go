// Package api HTTP 服务（stdlib mux；路由集中注册）
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"control-api/internal/agent"
	"control-api/internal/authn"
	"control-api/internal/config"
	"control-api/internal/engine"
	"control-api/internal/kb"
	"control-api/internal/pipeline"
	"control-api/internal/store"
	"control-api/internal/watcher"
)

type server struct {
	cfg  *config.Config
	st   *store.Store
	eng  *engine.Engine
	auth *authn.Auth
}

// route 一条路由注册项：pattern 为 Go 1.22 mux 模式（"METHOD /path"）
type route struct {
	pattern string
	handler http.HandlerFunc
}

// routes 路由表：Serve 遍历注册；契约对账测试（contract_test.go）直接枚举本表，
// 新增端点必须同步 docs/api/openapi.yaml，否则对账 FAIL。
func (s *server) routes() []route {
	return []route{
		{"GET /actuator/health", s.health}, // 探活端点，契约豁免（见 contract_test.go knownExemptions）
		{"POST /api/auth/login", s.login},
		{"GET /api/tasks", s.listTasks},
		{"POST /api/tasks", s.createTask},
		{"POST /api/tasks/{id}/action", s.taskAction},
		{"GET /api/approvals/pending", s.listPendingApprovals},
		{"GET /api/audit", s.listLogs},
		{"GET /api/findings", s.listFindings},
		{"POST /api/webhooks/merge-event", s.mergeEvent}, // 独立密钥认证，见 webhook.go
	}
}

func Serve(cfg *config.Config) error {
	st, err := store.Open(cfg.DB.Path)
	if err != nil {
		return err
	}
	defer st.Close()

	pl, err := pipeline.Load(filepath.Join(cfg.Paths.Home,
		"control-center", "orchestration", "workflows", "pipeline.yaml"))
	if err != nil {
		return err
	}
	s := &server{cfg: cfg, st: st, eng: &engine.Engine{P: pl, St: st}}
	s.auth = &authn.Auth{St: st}
	s.eng.SetTasksDir(cfg.Paths.TasksDir)
	s.eng.Runner = &agent.Runner{Cfg: cfg.Agent}

	// KB grounding（18.3，FINDING-016）：mode off 或 endpoint 空 = 不注入，零行为变化
	if cfg.KB.Mode != "" && cfg.KB.Mode != "off" && cfg.KB.Endpoint != "" {
		s.eng.Searcher = &kb.RESTSearcher{Endpoint: cfg.KB.Endpoint, APIKey: cfg.KB.APIKey}
		s.eng.KBMode = cfg.KB.Mode
	}

	// 任务目录索引：启动全量同步 + fsnotify 增量
	os.MkdirAll(cfg.Paths.TasksDir, 0o755)
	if err := watcher.Sync(cfg.Paths.TasksDir, st); err != nil {
		log.Printf("[api] 初始同步: %v", err)
	}
	done := make(chan struct{})
	defer close(done)
	if err := watcher.Watch(cfg.Paths.TasksDir, st, done); err != nil {
		log.Printf("[api] watcher 不可用: %v（退化为启动时同步）", err)
	}

	mux := http.NewServeMux()
	for _, r := range s.routes() {
		mux.HandleFunc(r.pattern, r.handler)
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("control-api listening on %s（pipeline: %d 阶段）", addr, len(pl.Stages))
	return http.ListenAndServe(addr, s.withAuth(mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	status := "UP"
	if err := s.st.Ping(); err != nil {
		status = "DOWN"
	}
	writeJSON(w, map[string]string{"status": status, "version": "0.1.0-dev"})
}

func (s *server) listTasks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.ListTasks()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, rows)
}

func (s *server) listLogs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.ListLogs(100)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, rows)
}

// ── 问题一览端点 ─────────────────────────────────────────────

type finding struct {
	ID         string `json:"id"`
	Date       string `json:"date"`
	Source     string `json:"source"`
	Phenomenon string `json:"phenomenon"`
	Evidence   string `json:"evidence"`
	Impact     string `json:"impact"`
	Status     string `json:"status"`
	Target     string `json:"target"`
}

// listFindings GET /api/findings：每次请求实时解析 FINDINGS.md 权威表（文件极小，不做缓存）
func (s *server) listFindings(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.cfg.Paths.Home, "control-center", "docs", "FINDINGS.md")
	data, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("读取 FINDINGS.md: %w", err))
		return
	}
	writeJSON(w, parseFindings(data))
}

// parseFindings 解析 Markdown 表格数据行；表头/分隔行/非 FINDING 开头行/列数不足行跳过
func parseFindings(data []byte) []finding {
	findings := make([]finding, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| FINDING-") {
			continue
		}
		cells := strings.Split(line, "|")
		// 整行形如 "| c1 | c2 | ... | c8 |"：首尾为空串，8 列需至少 10 段
		if len(cells) < 10 {
			continue
		}
		f := finding{}
		dst := []*string{&f.ID, &f.Date, &f.Source, &f.Phenomenon, &f.Evidence, &f.Impact, &f.Status, &f.Target}
		for i, p := range dst {
			*p = strings.TrimSpace(cells[i+1])
		}
		findings = append(findings, f)
	}
	return findings
}
