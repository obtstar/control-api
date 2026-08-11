// KB grounding（18.3 依据检索，on_no_basis: pause，FINDING-016）：
// 阶段入口 agent 拉起前检索 KB 依据。Searcher nil 或 KBMode off = 完全跳过；
// warn = 仅记 work_log 继续；enforce = 无据/不可达 fail-closed 自动暂停。
package engine

import (
	"context"
	"fmt"
	"log"
	"strings"

	"control-api/internal/tasks"
)

// kbSearchLimit grounding 检索条数（取前几条判断是否"有据"即可）
const kbSearchLimit = 3

// grounded 在 agent 拉起前执行依据检索；返回 false 表示已暂停、不再执行本阶段。
// 无 agent 的阶段（merge 等 team_mr_review）不经 maybeRun，天然不做检索。
func (e *Engine) grounded(m *tasks.Meta) bool {
	if e.Searcher == nil || e.KBMode == "" || e.KBMode == "off" {
		return true
	}
	query := strings.TrimSpace(m.RepoKey + " " + m.Title)
	hits, err := e.Searcher.Search(context.Background(), query, kbSearchLimit)
	if err != nil {
		return e.noBasis(m, fmt.Sprintf("grounding: KB unreachable(%s): %v", e.KBMode, err),
			fmt.Sprintf("KB unreachable: %v（grounding enforce fail-closed，自动暂停）", err))
	}
	if len(hits) == 0 {
		return e.noBasis(m, fmt.Sprintf("grounding: NO_BASIS(%s) query=%q", e.KBMode, query),
			fmt.Sprintf("NO_BASIS query=%q（缺失依据，on_no_basis: pause，自动暂停）", query))
	}
	e.logGrounding(m, fmt.Sprintf("grounding: %d 条依据 query=%q", len(hits), query))
	return true
}

// noBasis 无据或 KB 不可达：warn 记日志继续；enforce 提交 paused 后返回 false
func (e *Engine) noBasis(m *tasks.Meta, warnDetail, enforceDetail string) bool {
	if e.KBMode == "enforce" {
		m.Status = "paused"
		if err := e.commit(m, "auto_pause", enforceDetail); err != nil {
			log.Printf("[engine] grounding pause 提交 %s 失败: %v", m.TaskID, err)
		}
		return false
	}
	e.logGrounding(m, warnDetail)
	return true
}

// logGrounding grounding 结论记 work_log（action=grounding，operator=agent）
func (e *Engine) logGrounding(m *tasks.Meta, detail string) {
	if err := e.St.Log(m.TaskID, m.Stage, "grounding", "agent", "", detail); err != nil {
		log.Printf("[engine] grounding work_log %s 失败: %v", m.TaskID, err)
	}
}
