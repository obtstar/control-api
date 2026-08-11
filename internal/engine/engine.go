// Package engine 状态机动作处理：approve/reject/pause/resume + merge webhook（MarkMerged）
// 状态流转全部记 work_log（hash 链）；暂停为最高运行时权限（18 章）
package engine

import (
	"fmt"
	"log"
	"path/filepath"

	"control-api/internal/kb"
	"control-api/internal/pipeline"
	"control-api/internal/store"
	"control-api/internal/tasks"
)

type Engine struct {
	P  *pipeline.Pipeline
	St *store.Store
	// Runner 可选：设置后进入 running 状态自动执行阶段（pi 驱动）
	Runner interface {
		RunStage(m *tasks.Meta, stage, model string) (string, error)
	}
	TasksDir string
	// Searcher/KBMode 可选 KB grounding（18.3，FINDING-016）：
	// Searcher nil 或 KBMode off|"" = 完全跳过，零行为变化
	Searcher kb.Searcher
	KBMode   string // off | warn | enforce
}

// maybeRun running 状态下异步执行当前阶段，产物就绪后自动 Advance
func (e *Engine) maybeRun(m *tasks.Meta) {
	if e.Runner == nil || m.Status != "running" || m.Stage == "merge" {
		return // merge 阶段等待团队 webhook，不由 agent 执行
	}
	if !e.grounded(m) {
		return // enforce 无据/不可达：已自动暂停
	}
	meta := *m
	model := e.P.Model(meta.Stage) // 按 pipeline.yaml 声明选模型别名
	log.Printf("[engine] maybeRun %s stage=%s status=%s model=%s runner=%v", meta.TaskID, meta.Stage, meta.Status, model, e.Runner != nil)
	go func() {
		artifact, err := e.Runner.RunStage(&meta, meta.Stage, model)
		if err != nil {
			log.Printf("[engine] agent_error %s: %v", meta.TaskID, err)
			e.handleRunFailure(&meta, model, err)
			return
		}
		fresh := e.reload(meta.TaskID)
		if fresh == nil || fresh.Status == "paused" {
			return // 暂停中不自动推进（人恢复后重新执行）
		}
		e.Advance(fresh, artifact)
	}()
}

// handleRunFailure 阶段执行失败：记 work_log 后按连败熔断策略处理（18 章暂停最高权限）。
// 连续失败（含本次）达阈值 → auto pause；否则置回 pending 等待重试。
// 连败计数依赖 work_log 中连续前缀的失败条目，故重试状态回写不再写 work_log。
func (e *Engine) handleRunFailure(m *tasks.Meta, model string, runErr error) {
	failures, err := e.St.ConsecutiveFailures(m.TaskID)
	if err != nil {
		log.Printf("[engine] 连败计数失败 %s: %v（连同本次按熔断处理）", m.TaskID, err)
		failures = e.P.FailureThreshold() - 1
	}
	failures++ // 含本次
	detail := fmt.Sprintf("第 %d 次失败: %v", failures, runErr)
	// 失败处理路径无法上抛错误：连败计数可能漏记一次，记日志留痕（FINDING-032）
	if err := e.St.Log(m.TaskID, m.Stage, store.ActionAgentError, "agent", model, detail); err != nil {
		log.Printf("[engine] work_log 写入失败 %s: %v", m.TaskID, err)
	}
	fresh := e.reload(m.TaskID)
	if fresh == nil {
		return
	}
	if failures >= e.P.FailureThreshold() {
		fresh.Status = "paused" // 连败熔断自动暂停
		e.commit(fresh, "auto_pause", detail)
		if e.P.CircuitBreaker.Action == "auto_pause_and_notify" {
			log.Printf("[engine] notify: 任务 %s 连败 %d 次已自动暂停", m.TaskID, failures)
		}
		return
	}
	fresh.Status = "pending" // 未达阈值：可重试（状态回写权威 task.md，索引派生同步）
	if err := tasks.WriteMeta(fresh); err != nil {
		log.Printf("[engine] 回写 %s 失败: %v", m.TaskID, err)
		return
	}
	if err := e.St.UpsertTask(fresh); err != nil {
		log.Printf("[engine] 索引同步 %s 失败: %v", m.TaskID, err)
	}
}

func (e *Engine) reload(taskID string) *tasks.Meta {
	// 从权威源（task.md）重新读取最新状态
	m, err := tasks.ParseFile(filepath.Join(e.TasksDir, taskID, "task.md"))
	if err != nil {
		return nil
	}
	return m
}

// TasksDir 由 api 层注入
func (e *Engine) SetTasksDir(dir string) { e.TasksDir = dir }

// Advance 阶段完成：需要审批则入队 + awaiting_approval，否则直接进入下一阶段
func (e *Engine) Advance(m *tasks.Meta, artifact string) error {
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

// enterNext 自动路径（agent 执行）进入下一阶段，work_log operator 记 agent
func (e *Engine) enterNext(m *tasks.Meta, next, action, detail string) error {
	return e.enterNextAs(m, next, action, "agent", detail)
}

// enterNextAs 进入下一阶段：team_mr_review 终审无 agent 执行，直接入待审批队列等
// 合并 webhook；其余阶段置 running 并交由 maybeRun 驱动（next=="" 即末阶段 → delivered）。
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
	if err := e.commitAs(m, action, operator, detail); err != nil {
		return err
	}
	e.maybeRun(m)
	return nil
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

// Resume 恢复（仅人可操作；恢复后按当前 stage 重新执行）
func (e *Engine) Resume(m *tasks.Meta, by string) error {
	if m.Status != "paused" {
		return fmt.Errorf("任务未暂停: %s", m.Status)
	}
	m.Status = "running"
	if err := e.commitAs(m, "resume", by, "by "+by); err != nil {
		return err
	}
	e.maybeRun(m) // 恢复后重新执行当前阶段
	return nil
}

// MarkMerged 团队 MR 终审合并回传（merge webhook，FINDING-003）：
// 仅 merge 阶段 awaiting_approval 可流转；状态置 merged（operator 记 webhook）
// 后按现有 auto 机制推进 deliver（deliver 为 auto 阶段，maybeRun 拉起 agent 走完即 delivered）。
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
	if err := e.commitAs(m, "merged", "webhook", detail); err != nil {
		return err
	}
	next, err := e.P.Next(m.Stage)
	if err != nil {
		return err
	}
	return e.enterNextAs(m, next, "merged→"+next, "webhook", detail)
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
