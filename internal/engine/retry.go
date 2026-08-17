// 熔断后自动重试（FINDING-027）：连败未达阈值被置回 pending 的任务，
// 由 RetryLoop 周期扫描（权威 task.md + work_log 连败时间），退避到期自动重跑。
// 红线：仅碰 status=pending 且有连败记录的任务；paused 一律不动（18 章暂停最高权限）。
package engine

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"control-api/internal/tasks"
)

// retryTick 自动重试扫描周期
const retryTick = 15 * time.Second

// StartRetryLoop 周期扫描直到 done 关闭（随 serve 生命周期结束）
func (e *Engine) StartRetryLoop(done <-chan struct{}) {
	t := time.NewTicker(retryTick)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			e.RetryOnce(time.Now())
		}
	}
}

// RetryOnce 单趟扫描：对「pending + 连败 1..阈值-1 + 退避到期」的任务置 running 并拉起执行。
func (e *Engine) RetryOnce(now time.Time) {
	if e.Runner == nil || e.TasksDir == "" {
		return
	}
	entries, err := os.ReadDir(e.TasksDir)
	if err != nil {
		log.Printf("[engine] 重试扫描读目录失败 %s: %v", e.TasksDir, err)
		return
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		e.maybeRetry(ent.Name(), now)
	}
}

// maybeRetry 判定并执行单个任务的重试
func (e *Engine) maybeRetry(taskID string, now time.Time) {
	m, err := tasks.ParseFile(filepath.Join(e.TasksDir, taskID, "task.md"))
	if err != nil || m.Status != "pending" || m.Stage == "merge" {
		return // merge 等团队 webhook；非 pending 不归重试管
	}
	failures, err := e.St.ConsecutiveFailures(taskID)
	if err != nil || failures == 0 || failures >= e.P.FailureThreshold() {
		return // 无失败历史（未起跑）或已达阈值（已/待熔断）都不动
	}
	last, err := e.St.LastFailureAt(taskID)
	if err != nil || now.Sub(last) < e.P.RetryBackoff(failures) {
		return // 退避未到期
	}
	m.Status = "running"
	detail := fmt.Sprintf("连败 %d 次退避 %v 到期，自动重试", failures, e.P.RetryBackoff(failures))
	if err := e.commitAs(m, "retry", "agent", detail); err != nil {
		log.Printf("[engine] 重试回写 %s 失败: %v", taskID, err)
		return
	}
	log.Printf("[engine] %s", detail)
	e.maybeRun(m)
}
