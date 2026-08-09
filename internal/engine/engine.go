// Package engine 状态机动作处理：approve/reject/pause/resume
// 状态流转全部记 work_log（hash 链）；暂停为最高运行时权限（18 章）
package engine

import (
	"fmt"
	"path/filepath"

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
}

// maybeRun running 状态下异步执行当前阶段，产物就绪后自动 Advance
func (e *Engine) maybeRun(m *tasks.Meta) {
	if e.Runner == nil || m.Status != "running" || m.Stage == "merge" {
		return // merge 阶段等待团队 webhook，不由 agent 执行
	}
	meta := *m
	model := e.P.Model(meta.Stage) // 按 pipeline.yaml 声明选模型别名
	go func() {
		artifact, err := e.Runner.RunStage(&meta, meta.Stage, model)
		if err != nil {
			e.St.Log(meta.TaskID, meta.Stage, "agent_error", "agent", model, err.Error())
			fresh := e.reload(meta.TaskID)
			if fresh != nil {
				fresh.Status = "paused" // 执行失败自动暂停（熔断语义）
				e.commit(fresh, "auto_pause", err.Error())
			}
			return
		}
		fresh := e.reload(meta.TaskID)
		if fresh == nil || fresh.Status == "paused" {
			return // 暂停中不自动推进（人恢复后重新执行）
		}
		e.Advance(fresh, artifact)
	}()
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
	m.Stage = next
	m.Status = "running"
	return e.commit(m, "auto_advance", next)
}

// Approve 批准：记录裁决后推进到下一阶段
func (e *Engine) Approve(m *tasks.Meta, comment, by string) error {
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
	if next == "" {
		m.Status = "delivered"
		return e.commit(m, "deliver", comment)
	}
	m.Stage = next
	m.Status = "running"
	if err := e.commit(m, "approved→"+next, comment); err != nil {
		return err
	}
	e.maybeRun(m) // 批准后自动执行新阶段
	return nil
}

// Reject 驳回（必附批注）：按 on_reject 回退
func (e *Engine) Reject(m *tasks.Meta, comment, by string) error {
	if comment == "" {
		return fmt.Errorf("驳回必须附批注")
	}
	if err := e.St.Decide(m.TaskID, m.Stage, "rejected", comment, by); err != nil {
		return err
	}
	target := e.P.RejectTarget(m.Stage)
	m.Stage = target
	m.Status = "running"
	return e.commit(m, "reject→"+target, comment)
}

// Pause 暂停（最高权限，任何状态可触发，停写）
func (e *Engine) Pause(m *tasks.Meta, by string) error {
	if m.Status == "paused" {
		return fmt.Errorf("任务已暂停")
	}
	m.Status = "paused"
	return e.commit(m, "pause", "by "+by)
}

// Resume 恢复（仅人可操作；恢复后按当前 stage 重新执行）
func (e *Engine) Resume(m *tasks.Meta, by string) error {
	if m.Status != "paused" {
		return fmt.Errorf("任务未暂停: %s", m.Status)
	}
	m.Status = "running"
	if err := e.commit(m, "resume", "by "+by); err != nil {
		return err
	}
	e.maybeRun(m) // 恢复后重新执行当前阶段
	return nil
}

func (e *Engine) commit(m *tasks.Meta, action, detail string) error {
	if err := tasks.WriteMeta(m); err != nil { // frontmatter 为权威
		return err
	}
	if err := e.St.UpsertTask(m); err != nil { // 索引为派生
		return err
	}
	return e.St.Log(m.TaskID, m.Stage, action, detailActor(detail), "", detail)
}

func detailActor(detail string) string { return "agent" }

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
