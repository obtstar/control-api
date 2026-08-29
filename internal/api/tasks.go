// 任务创建与动作端点
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"control-api/internal/authn"
	"control-api/internal/tasks"

	"gopkg.in/yaml.v3"
)

type createReq struct {
	Title   string `json:"title"`
	RepoKey string `json:"repo_key"`
	Domain  string `json:"domain"` // 可选：领域 skill（frontend-dev/backend-go/…）
	Body    string `json:"body"`   // 需求正文（L1，人写）
}

// createTask 创建任务 = 落一份 task.md（文档即任务）
func (s *server) createTask(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Body) == "" {
		writeErr(w, 400, fmt.Errorf("title/body 必填"))
		return
	}
	// repo_key 提供时须已登记注册表（FINDING-019/046：任务不得指向不存在的
	// 仓库登记，14.2）；空 repo_key 允许——未分配仓库的草稿任务，后续补登
	if req.RepoKey != "" && s.reg != nil && !s.reg.Registered(req.RepoKey) {
		writeErr(w, 400, fmt.Errorf("repo_key 未登记注册表: %s（登记见 control-center/registry/repos.yaml，14.2）", req.RepoKey))
		return
	}
	// 原子占号建目录（FINDING-009 竞态）：os.Mkdir 撞 EEXIST 才 +1 重试
	id, dir, err := createTaskDir(s.cfg.Paths.TasksDir)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// frontmatter 用 yaml.Marshal 序列化（与 tasks.WriteMeta 同口径），
	// title 含冒号/引号/换行不再产生非法 YAML（FINDING-009 注入）
	fm, err := yaml.Marshal(tasks.Meta{
		TaskID: id, Title: req.Title, RepoKey: req.RepoKey, Domain: req.Domain,
		Status: "pending", Authority: "L1",
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	content := fmt.Sprintf("---\n%s---\n\n# %s\n\n%s\n", fm, req.Title, req.Body)
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte(content), 0o644); err != nil {
		writeErr(w, 500, err)
		return
	}
	operator, _ := identity(r) // FINDING-034：记真实操作人（FINDING-026：经 context 传递）
	if operator == "" {
		operator = "human"
	}
	// work_log 失败不回败主流程：task.md（权威）已落盘，记日志留痕（FINDING-032）
	if err := s.st.Log(id, "", "create", operator, "", req.Title); err != nil {
		log.Printf("[api] work_log 写入失败 %s: %v", id, err)
	}
	writeJSON(w, map[string]string{"task_id": id, "path": dir})
}

type actionReq struct {
	Action   string `json:"action"` // approve/reject/pause/resume/deliver（advance 仅供内部自动流程，不经 HTTP）
	Comment  string `json:"comment"`
	By       string `json:"by"`
	Artifact string `json:"artifact"`
}

func (s *server) taskAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req actionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.By == "" {
		req.By, _ = identity(r)
		if req.By == "" {
			req.By = "human"
		}
	}
	// 审批动作的角色路由校验：approval.role 必须与用户角色匹配（admin 通吃）
	if req.Action == "approve" || req.Action == "reject" {
		role, err := s.st.ApprovalRoleOf(id)
		if err == nil && role != "" {
			uname, urole := identity(r)
			u := &authn.User{Username: uname, Role: urole}
			if !authn.CanDecide(u, role) {
				writeErr(w, 403, fmt.Errorf("该阶段审批权属于角色 %s", role))
				return
			}
		}
	}
	m, err := tasks.ParseFile(filepath.Join(s.cfg.Paths.TasksDir, id, "task.md"))
	if err != nil {
		writeErr(w, 404, fmt.Errorf("任务不存在: %s", id))
		return
	}
	switch req.Action {
	case "approve":
		err = s.eng.Approve(m, req.Comment, req.By)
	case "reject":
		err = s.eng.Reject(m, req.Comment, req.By)
	case "pause":
		err = s.eng.Pause(m, req.By)
	case "resume":
		err = s.eng.Resume(m, req.By)
	case "deliver": // FINDING-029 交付确认：仅 merge/merged 可触发
		err = s.eng.Deliver(m, req.By)
	case "archive": // TASK-000020 归档：仅 delivered
		err = s.eng.Archive(m, req.By)
	default:
		writeErr(w, 400, fmt.Errorf("未知 action: %s", req.Action))
		return
	}
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	s.broadcastTask(id, req.Action) // TASK-007：状态流转后 SSE 推送
	writeJSON(w, map[string]string{"task_id": id, "stage": m.Stage, "status": m.Status})
}

func (s *server) listPendingApprovals(w http.ResponseWriter, r *http.Request) {
	_, role := identity(r)
	rows, err := s.st.ListPendingApprovals(role)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, rows)
}

// createTaskDir 原子占用下一个任务 ID 并创建目录（FINDING-009 竞态）：
// 先扫目录取起始编号，os.Mkdir（非 MkdirAll）撞 EEXIST 则 +1 重试，
// 并发创建不会拿到同一 ID（Mkdir 的 EEXIST 即占号成功的判据）。
func createTaskDir(root string) (id, dir string, err error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", err
	}
	next := 1
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", "", err
	}
	for _, e := range entries {
		var n int
		if _, err := fmt.Sscanf(e.Name(), "TASK-%d", &n); err == nil && n >= next {
			next = n + 1
		}
	}
	for {
		id = fmt.Sprintf("TASK-%06d", next)
		dir = filepath.Join(root, id)
		if err := os.Mkdir(dir, 0o755); err == nil {
			return id, dir, nil
		} else if !os.IsExist(err) {
			return "", "", fmt.Errorf("创建任务目录 %s: %w", dir, err)
		}
		next++
	}
}

// ── SSE 任务事件流（TASK-007 实时通知，合并自 events.go 以守单包 ≤8 文件红线）──
// sseHub 连接注册表：add/remove 增删订阅 chan，broadcast 非阻塞群发。
type sseHub struct {
	mu    sync.Mutex
	conns map[chan string]struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{conns: make(map[chan string]struct{})}
}

func (h *sseHub) add() chan string {
	ch := make(chan string, 8)
	h.mu.Lock()
	h.conns[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *sseHub) remove(ch chan string) {
	h.mu.Lock()
	delete(h.conns, ch)
	h.mu.Unlock()
}

// broadcast 群发事件；任一连接缓冲满则丢弃（事件是投影，前端收到任意事件即重拉权威列表）
func (h *sseHub) broadcast(ev string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.conns {
		select {
		case ch <- ev:
		default:
		}
	}
}

const sseHeartbeat = 15 * time.Second

// streamEvents GET /api/events/stream?token=...：SSE 事件流（withAuth 豁免，见 auth.go）
func (s *server) streamEvents(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if _, err := s.auth.Authenticate(tok); err != nil {
		writeErr(w, 401, err)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, errString("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := s.hub.add()
	defer s.hub.remove(ch)
	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			fmt.Fprintf(w, "event: task\ndata: %s\n\n", ev)
			fl.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n") // 心跳注释行，防代理/浏览器断连
			fl.Flush()
		}
	}
}

// broadcastTask 状态流转成功后广播任务事件（api 层持有 hub，engine 保持纯净）
func (s *server) broadcastTask(taskID, action string) {
	if s.hub == nil {
		return // 单测夹具未装配 hub（对齐 reg 可置 nil 模式，见 server.go）
	}
	ev := fmt.Sprintf(`{"task_id":%q,"action":%q,"ts":%d}`, taskID, action, time.Now().Unix())
	s.hub.broadcast(ev)
}
