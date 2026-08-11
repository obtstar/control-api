// merge webhook 端点（FINDING-003）：团队 MR 终审合并事件回传。
// 认证独立于 Bearer 会话：X-Webhook-Token 头与 server.webhook_secret 常量时间比较。
package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

type mergeEventReq struct {
	TaskID string `json:"task_id"`
	Event  string `json:"event"`  // 仅支持 merged
	Detail string `json:"detail"` // 如 "MR !123 by @teammate"
}

// mergeEvent POST /api/webhooks/merge-event：Git 平台合并事件 → MarkMerged → deliver
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
	writeJSON(w, map[string]string{"task_id": req.TaskID, "status": "merged"})
}
