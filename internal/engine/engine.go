// Package engine 状态机动作处理：approve/reject/pause/resume + merge webhook（MarkMerged）
// 状态流转全部记 work_log（hash 链）；暂停为最高运行时权限（18 章）
package engine

import (
	"fmt"
	"path/filepath"

	"control-api/internal/kb"
	"control-api/internal/pipeline"
	"control-api/internal/store"
	"control-api/internal/tasks"
)

type Engine struct {
	P  *pipeline.Pipeline
	St *store.Store
	// TasksDir 任务目录（任务即文档，task.md frontmatter 为权威）
	TasksDir string
	// Searcher/KBMode 可选 KB grounding（18.3，FINDING-016）：
	// Searcher nil 或 KBMode off|"" = 完全跳过，零行为变化；
	// C1 执行层迁移（TASK-004）后检查点位于 Advance 入口
	// （DSH 声明阶段完成时校验依据；enforce 无据自动暂停）。
	Searcher kb.Searcher
	KBMode   string // off | warn | enforce
}

// reload 从权威源（task.md）重新读取最新状态
func (e *Engine) reload(taskID string) *tasks.Meta {
	m, err := tasks.ParseFile(filepath.Join(e.TasksDir, taskID, "task.md"))
	if err != nil {
		return nil
	}
	return m
}

// TasksDir 由 api 层注入
func (e *Engine) SetTasksDir(dir string) { e.TasksDir = dir }

// Advance 阶段完成（C1：由 DSH 会话经 advance webhook 声明）：
// 入口先做 KB grounding 检查（18.3，TASK-004 迁移：enforce 无据/不可达自动暂停并返回错误），
// 通过后：需要审批则入队 + awaiting_approval，否则直接进入下一阶段。
func (e *Engine) Advance(m *tasks.Meta, artifact string) error {
	if !e.grounded(m) {
		return fmt.Errorf("NO_BASIS：KB 依据缺失或不可达，任务已自动暂停（on_no_basis: pause，人工补齐依据后 resume）")
	}
	if m.Stage == "" {
		m.Stage = e.P.First()
	}
	if e.P.IsLast(m.Stage) {
		m.Status = "delivered"
		return e.commit(m, "deliver", artifact)
	}
	if e.P.NeedsApproval(m.Stage) {
		m.Status = "awaiting_approval"
		role := roleFor(m.Stage)
		if err := e.St.NewApproval(m.TaskID, m.Stage, role, artifact); err != nil {
			return err
		}
		return e.commit(m, "awaiting_approval", artifact)
	}
	next, err := e.P.Next(m.Stage)
	if err != nil {
		return err
	}
	return e.enterNextAs(m, next, "auto_advance", "agent", next)
}

// enterNextAs 进入下一阶段：team_mr_review 终审无执行，直接入待审批队列等
// 合并 webhook；其余阶段置 running 等待 DSH 会话执行（C1：advance webhook 回传，
// 无自动执行器；next=="" 即末阶段 → delivered）。
// operator 为本次流转的归因操作人（FINDING-007：人工动作记真实操作人）。
func (e *Engine) enterNextAs(m *tasks.Meta, next, action, operator, detail string) error {
	if next == "" {
		m.Status = "delivered"
		return e.commitAs(m, "deliver", operator, detail)
	}
	m.Stage = next
	if e.P.IsTeamReview(next) {
		m.Status = "awaiting_approval"
		if err := e.St.NewApproval(m.TaskID, next, roleFor(next), detail); err != nil {
			return err
		}
		return e.commitAs(m, action, operator, detail)
	}
	m.Status = "running"
	return e.commitAs(m, action, operator, detail)
}

// Approve 批准：记录裁决后推进到下一阶段。
// merge 阶段（team_mr_review 终审）不接受 Web 审批，等团队合并 webhook 回传。
func (e *Engine) Approve(m *tasks.Meta, comment, by string) error {
	if e.P.IsTeamReview(m.Stage) {
		return fmt.Errorf("%s 阶段为团队 MR 终审，Web 审批无效，等合并 webhook 回传", m.Stage)
	}
	if m.Status != "awaiting_approval" {
		return fmt.Errorf("任务不在待审批状态: %s", m.Status)
	}
	if err := e.St.Decide(m.TaskID, m.Stage, "approved", comment, by); err != nil {
		return err
	}
	if err := e.St.Log(m.TaskID, m.Stage, "approve", by, "", comment); err != nil {
		return err
	}
	next, err := e.P.Next(m.Stage)
	if err != nil {
		return err
	}
	return e.enterNextAs(m, next, "approved→"+next, by, comment)
}

// Reject 驳回（必附批注）：按 on_reject 回退；仅 awaiting_approval 状态可驳回。
// merge 阶段（team_mr_review 终审）不接受 Web 驳回；其未声明 on_reject，
// 走 RejectTarget 默认（重做本阶段）也无意义，故直接拒绝。
func (e *Engine) Reject(m *tasks.Meta, comment, by string) error {
	if e.P.IsTeamReview(m.Stage) {
		return fmt.Errorf("%s 阶段为团队 MR 终审，Web 驳回无效，请在 Git 平台评审", m.Stage)
	}
	if m.Status != "awaiting_approval" {
		return fmt.Errorf("任务不在待审批状态: %s", m.Status)
	}
	if comment == "" {
		return fmt.Errorf("驳回必须附批注")
	}
	if err := e.St.Decide(m.TaskID, m.Stage, "rejected", comment, by); err != nil {
		return err
	}
	target := e.P.RejectTarget(m.Stage)
	m.Stage = target
	m.Status = "running"
	return e.commitAs(m, "reject→"+target, by, comment)
}

// Pause 暂停（最高权限，任何状态可触发，停写）
func (e *Engine) Pause(m *tasks.Meta, by string) error {
	if m.Status == "paused" {
		return fmt.Errorf("任务已暂停")
	}
	m.Status = "paused"
	return e.commitAs(m, "pause", by, "by "+by)
}

// Resume 恢复（仅人可操作；恢复后置 running 等待 DSH 会话按当前 stage 重新执行）
func (e *Engine) Resume(m *tasks.Meta, by string) error {
	if m.Status != "paused" {
		return fmt.Errorf("任务未暂停: %s", m.Status)
	}
	m.Status = "running"
	return e.commitAs(m, "resume", by, "by "+by)
}

// MarkMerged 团队 MR 终审合并回传（merge webhook，FINDING-003）：
// 仅 merge 阶段 awaiting_approval 可流转；状态置 merged（operator 记 webhook）
// 并停留——merged 为稳定态（FINDING-029：已合并待交付，看板可感知），
// 由人工 Deliver 确认后才推进 deliver。
func (e *Engine) MarkMerged(taskID, detail string) error {
	m := e.reload(taskID)
	if m == nil {
		return fmt.Errorf("任务不存在: %s", taskID)
	}
	if m.Stage != "merge" || m.Status != "awaiting_approval" {
		return fmt.Errorf("任务不在 merge 等待态: stage=%s status=%s", m.Stage, m.Status)
	}
	if err := e.St.Decide(m.TaskID, m.Stage, "approved", detail, "webhook"); err != nil {
		return err
	}
	m.Status = "merged"
	return e.commitAs(m, "merged", "webhook", detail)
}

// Deliver 交付确认（FINDING-029）：仅 merge 阶段 merged 状态可触发（人工动作），
// 推进进入 deliver 阶段（auto；C1 下由 DSH 会话执行交付清理后 advance → IsLast → delivered）。
func (e *Engine) Deliver(m *tasks.Meta, by string) error {
	if m.Stage != "merge" || m.Status != "merged" {
		return fmt.Errorf("任务不在已合并待交付态: stage=%s status=%s", m.Stage, m.Status)
	}
	next, err := e.P.Next(m.Stage)
	if err != nil {
		return err
	}
	return e.enterNextAs(m, next, "deliver", by, "交付确认 by "+by)
}

// commit 自动路径落盘：agent 驱动的状态迁移，work_log operator 恒记 agent
func (e *Engine) commit(m *tasks.Meta, action, detail string) error {
	return e.commitAs(m, action, "agent", detail)
}

// commitAs 状态落盘三件套：task.md（权威）→ task_index（派生）→ work_log（hash 链）
func (e *Engine) commitAs(m *tasks.Meta, action, operator, detail string) error {
	if err := tasks.WriteMeta(m); err != nil { // frontmatter 为权威
		return err
	}
	if err := e.St.UpsertTask(m); err != nil { // 索引为派生
		return err
	}
	return e.St.Log(m.TaskID, m.Stage, action, operator, "", detail)
}

func roleFor(stage string) string {
	switch stage {
	case "design":
		return "designer"
	case "testing":
		return "tester"
	case "merge":
		return "team"
	default:
		return "customer"
	}
}
