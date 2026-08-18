// Package api HTTP 服务（stdlib mux；路由集中注册）
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"control-api/internal/agent"
	"control-api/internal/authn"
	"control-api/internal/config"
	"control-api/internal/engine"
	"control-api/internal/kb"
	"control-api/internal/pipeline"
	"control-api/internal/registry"
	"control-api/internal/store"
	"control-api/internal/watcher"
)

type server struct {
	cfg  *config.Config
	st   *store.Store
	eng  *engine.Engine
	auth *authn.Auth
	// reg 仓库注册表内存缓存（14.2 启动时读取；FINDING-019/046 createTask 校验
	// repo_key）。Serve 启动必加载、失败即退出；单测夹具可置 nil（不启用校验）
	reg *registry.Registry
	// searcher KB 检索（web 检索视图）：kb.endpoint 非空即装配，不受 grounding mode 门控
	searcher kb.Searcher
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
		{"GET /api/kb/search", s.searchKB},
		{"GET /api/openapi.yaml", s.serveOpenAPI},
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
	// 注册表启动时读取（14.2，FINDING-019/046）：缺失/解析失败即启动失败，不降级放行
	reg, err := registry.Load(cfg.Paths.RegistryPath)
	if err != nil {
		return err
	}
	s := &server{cfg: cfg, st: st, eng: &engine.Engine{P: pl, St: st}, reg: reg}
	s.auth = &authn.Auth{St: st}
	s.eng.SetTasksDir(cfg.Paths.TasksDir)
	s.eng.Runner = &agent.Runner{Cfg: cfg.Agent}

	// KB 检索：endpoint 非空即装配（web 检索视图，与 mode 无关）；
	// KB grounding（18.3，FINDING-016）：mode off = 不注入 engine，零行为变化
	if cfg.KB.Endpoint != "" {
		rs := &kb.RESTSearcher{Endpoint: cfg.KB.Endpoint, APIKey: cfg.KB.APIKey}
		s.searcher = rs
		if cfg.KB.Mode != "" && cfg.KB.Mode != "off" {
			s.eng.Searcher = rs
			s.eng.KBMode = cfg.KB.Mode
		}
	}

	// 任务目录索引：启动全量同步 + fsnotify 增量
	if err := os.MkdirAll(cfg.Paths.TasksDir, 0o755); err != nil {
		return fmt.Errorf("创建任务目录 %s: %w", cfg.Paths.TasksDir, err)
	}
	if err := watcher.Sync(cfg.Paths.TasksDir, st); err != nil {
		log.Printf("[api] 初始同步: %v", err)
	}
	// 启动回收僵尸 running 任务（FINDING-043）：自动暂停留痕，人工 resume 恢复
	s.eng.RecoverOnBoot()
	done := make(chan struct{})
	defer close(done)
	if err := watcher.Watch(cfg.Paths.TasksDir, st, done); err != nil {
		log.Printf("[api] watcher 不可用: %v（退化为启动时同步）", err)
	}
	// 熔断后自动重试扫描（FINDING-027）：pending + 连败未达阈值 + 退避到期 → 重跑
	go s.eng.StartRetryLoop(done)

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
	data, err := os.ReadFile(s.cfg.Paths.FindingsPath)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("读取 FINDINGS.md: %w", err))
		return
	}
	writeJSON(w, parseFindings(data))
}

// parseFindings 解析 Markdown 表格数据行；表头/分隔行/非 FINDING 开头行/列数不足行跳过。
// 单元格内 `\|` 转义不参与切分，还原为字面 |（FINDING-028）
func parseFindings(data []byte) []finding {
	findings := make([]finding, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| FINDING-") {
			continue
		}
		cells := splitRow(line)
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

// splitRow 按未转义的 | 切分表格行；`\|` 还原为字面 |
func splitRow(line string) []string {
	cells := []string{}
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(line[i])
	}
	return append(cells, cur.String())
}

// ── KB 检索端点 ─────────────────────────────────────────────

// searchKB GET /api/kb/search：代理 PieKBS REST 检索（web 检索视图）。
// endpoint 未配置 503；PieKBS 非 200/超时/解析错 502 并带原因摘要；q 空/limit 非法 400。
func (s *server) searchKB(w http.ResponseWriter, r *http.Request) {
	if s.searcher == nil {
		writeErr(w, 503, errString("kb.endpoint 未配置，KB 检索不可用"))
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, 400, errString("缺少检索词 q"))
		return
	}
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErr(w, 400, fmt.Errorf("limit 非法: %q", v))
			return
		}
		limit = n
	}
	hits, err := s.searcher.Search(r.Context(), q, limit)
	if err != nil {
		writeErr(w, 502, fmt.Errorf("KB 检索失败: %w", err))
		return
	}
	if hits == nil {
		hits = []kb.Hit{}
	}
	writeJSON(w, hits)
}

// serveOpenAPI GET /api/openapi.yaml：自指端点，服务契约文件本体（文档即实现）
func (s *server) serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	path := s.cfg.Paths.ContractPath
	data, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("读取 openapi.yaml: %w", err))
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Write(data)
}
