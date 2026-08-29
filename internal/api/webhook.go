// merge webhook 端点（FINDING-003）：团队 MR 终审合并事件回传。
// 认证独立于 Bearer 会话：X-Webhook-Token 头与 server.webhook_secret 常量时间比较。
package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"control-api/internal/tasks"
)

type mergeEventReq struct {
	TaskID string `json:"task_id"`
	Event  string `json:"event"`  // 仅支持 merged
	Detail string `json:"detail"` // 如 "MR !123 by @teammate"
}

// mergeEvent POST /api/webhooks/merge-event：Git 平台合并事件 → MarkMerged
// 置 merged 停留待交付（FINDING-029），人工 action=deliver 确认后才推进 deliver
func (s *server) mergeEvent(w http.ResponseWriter, r *http.Request) {
	secret := s.cfg.Server.WebhookSecret
	if secret == "" {
		log.Printf("[api] merge webhook 未启用：server.webhook_secret 未配置（拒绝请求）")
		writeErr(w, 503, fmt.Errorf("merge webhook 未启用"))
		return
	}
	tok := r.Header.Get("X-Webhook-Token")
	if subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) != 1 {
		writeErr(w, 401, fmt.Errorf("webhook token 无效"))
		return
	}
	var req mergeEventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.TaskID == "" {
		writeErr(w, 400, fmt.Errorf("task_id 必填"))
		return
	}
	if req.Event != "merged" {
		writeErr(w, 400, fmt.Errorf("不支持的 event: %s", req.Event))
		return
	}
	if err := s.eng.MarkMerged(req.TaskID, req.Detail); err != nil {
		writeErr(w, 409, err)
		return
	}
	s.broadcastTask(req.TaskID, "merged") // TASK-007 SSE 推送
	writeJSON(w, map[string]string{"task_id": req.TaskID, "status": "merged"})
}

// advanceEvent POST /api/webhooks/advance：DSH 会话声明任务当前阶段完成 → engine.Advance
// （置 Stage=First() 若空、末阶段→delivered、需审批→awaiting_approval+审批队列、auto→下一阶段）。
// C1 执行层迁移（TASK-004）：pi 执行器已退役，任务阶段由 DSH 会话执行，
// 阶段产物落任务目录后经本通道回传进审批闸（Advance 入口含 18.3 KB grounding 检查）。
// 认证同 merge webhook（FINDING-003）：X-Webhook-Token 与 server.webhook_secret 常量时间比较；
// secret 未配置一律 503。
type advanceReq struct {
	TaskID   string `json:"task_id"`
	Artifact string `json:"artifact"` // 阶段产物说明（可选，记 work_log detail）
}

func (s *server) advanceEvent(w http.ResponseWriter, r *http.Request) {
	secret := s.cfg.Server.WebhookSecret
	if secret == "" {
		log.Printf("[api] advance webhook 未启用：server.webhook_secret 未配置（拒绝请求）")
		writeErr(w, 503, fmt.Errorf("advance webhook 未启用"))
		return
	}
	tok := r.Header.Get("X-Webhook-Token")
	if subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) != 1 {
		writeErr(w, 401, fmt.Errorf("webhook token 无效"))
		return
	}
	var req advanceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if strings.TrimSpace(req.TaskID) == "" {
		writeErr(w, 400, fmt.Errorf("task_id 必填"))
		return
	}
	m, err := tasks.ParseFile(filepath.Join(s.cfg.Paths.TasksDir, req.TaskID, "task.md"))
	if err != nil {
		writeErr(w, 404, fmt.Errorf("任务不存在: %s", req.TaskID))
		return
	}
	// 状态守卫：仅 pending/running 可声明阶段完成。paused 为最高优先级
	// （18 章：暂停期间禁止一切写操作）；awaiting_approval/delivered 重复推进无意义。
	switch m.Status {
	case "pending", "running":
	default:
		writeErr(w, 409, fmt.Errorf("任务状态 %s 不可 advance（仅 pending/running）", m.Status))
		return
	}
	if err := s.eng.Advance(m, req.Artifact); err != nil {
		writeErr(w, 409, err)
		return
	}
	s.broadcastTask(req.TaskID, "advance") // TASK-007 SSE 推送
	writeJSON(w, map[string]string{"task_id": req.TaskID, "stage": m.Stage, "status": m.Status})
}
