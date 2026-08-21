// RecoverOnBoot 启动回收（FINDING-043）：serve 重启后 running 任务状态回收。
// C1 执行层迁移（TASK-004）后无自动执行器，running 即"等待 DSH 会话执行"；
// 重启期间 DSH 会话可能已随进程终止，按 18 章"暂停最高权限"自动暂停留痕，
// 人工确认后 resume 恢复（resume 后置 running 重新等 DSH）。
package engine

import (
	"log"
	"os"
	"path/filepath"

	"control-api/internal/tasks"
)

// RecoverOnBoot 启动回收：running → paused（等人工 resume）
func (e *Engine) RecoverOnBoot() {
	if e.TasksDir == "" {
		return
	}
	entries, err := os.ReadDir(e.TasksDir)
	if err != nil {
		log.Printf("[engine] 启动回收读目录失败 %s: %v", e.TasksDir, err)
		return
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		m, err := tasks.ParseFile(filepath.Join(e.TasksDir, ent.Name(), "task.md"))
		if err != nil || m.Status != "running" {
			continue
		}
		m.Status = "paused"
		if err := e.commitAs(m, "boot_recover_pause", "agent",
			"serve 重启回收：原执行已随进程终止，人工确认后 resume 恢复"); err != nil {
			log.Printf("[engine] 启动回收 %s 失败: %v", ent.Name(), err)
			continue
		}
		log.Printf("[engine] 启动回收：%s running→paused（等人工 resume）", ent.Name())
	}
}
