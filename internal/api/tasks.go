// 任务创建与动作端点
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"control-api/internal/authn"
	"control-api/internal/tasks"
)

type createReq struct {
	Title   string `json:"title"`
	RepoKey string `json:"repo_key"`
	Domain  string `json:"domain"` // 可选：领域 skill（frontend-dev/backend-java/…）
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
	id, err := nextTaskID(s.cfg.Paths.TasksDir)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	dir := filepath.Join(s.cfg.Paths.TasksDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, 500, err)
		return
	}
	content := fmt.Sprintf(`---
task_id: %s
title: %s
repo_key: %s
domain: %s
stage: ""
status: pending
authority: L1
---

# %s

%s
`, id, req.Title, req.RepoKey, req.Domain, req.Title, req.Body)
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte(content), 0o644); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.st.Log(id, "", "create", "human", "", req.Title)
	writeJSON(w, map[string]string{"task_id": id, "path": dir})
}

type actionReq struct {
	Action   string `json:"action"` // approve/reject/pause/resume（advance 仅供内部自动流程，不经 HTTP）
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
		req.By = r.Header.Get("X-User")
		if req.By == "" {
			req.By = "human"
		}
	}
	// 审批动作的角色路由校验：approval.role 必须与用户角色匹配（admin 通吃）
	if req.Action == "approve" || req.Action == "reject" {
		role, err := s.st.ApprovalRoleOf(id)
		if err == nil && role != "" {
			u := &authn.User{Username: r.Header.Get("X-User"), Role: r.Header.Get("X-Role")}
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
	role := r.Header.Get("X-Role")
	rows, err := s.st.ListPendingApprovals(role)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, rows)
}

func nextTaskID(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return "TASK-001", nil
	}
	if err != nil {
		return "", err
	}
	max := 0
	for _, e := range entries {
		var n int
		if _, err := fmt.Sscanf(e.Name(), "TASK-%03d", &n); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("TASK-%03d", max+1), nil
}
