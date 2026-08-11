// Package api HTTP 服务（stdlib mux；路由集中注册）
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

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
	mux.HandleFunc("GET /actuator/health", s.health)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("POST /api/tasks/{id}/action", s.taskAction)
	mux.HandleFunc("GET /api/approvals/pending", s.listPendingApprovals)
	mux.HandleFunc("GET /api/audit", s.listLogs)
	mux.HandleFunc("GET /api/findings", s.listFindings)
	mux.HandleFunc("POST /api/webhooks/merge-event", s.mergeEvent) // 独立密钥认证，见 webhook.go

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
