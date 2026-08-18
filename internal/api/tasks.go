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
	default:
		writeErr(w, 400, fmt.Errorf("未知 action: %s", req.Action))
		return
	}
	if err != nil {
		writeErr(w, 409, err)
		return
	}
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
		if _, err := fmt.Sscanf(e.Name(), "TASK-%03d", &n); err == nil && n >= next {
			next = n + 1
		}
	}
	for {
		id = fmt.Sprintf("TASK-%03d", next)
		dir = filepath.Join(root, id)
		if err := os.Mkdir(dir, 0o755); err == nil {
			return id, dir, nil
		} else if !os.IsExist(err) {
			return "", "", fmt.Errorf("创建任务目录 %s: %w", dir, err)
		}
		next++
	}
}
