// KB grounding 测试（FINDING-016）：fake Searcher × warn/enforce/off，
// 断言继续/暂停、work_log detail、off 时 Searcher 零调用。:memory: SQLite 实测。
package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"control-api/internal/kb"
)

// fakeSearcher 记录调用参数的可控 Searcher
type fakeSearcher struct {
	hits      []kb.Hit
	err       error
	calls     int
	lastQuery string
	lastLimit int
}

func (f *fakeSearcher) Search(_ context.Context, query string, limit int) ([]kb.Hit, error) {
	f.calls++
	f.lastQuery = query
	f.lastLimit = limit
	return f.hits, f.err
}

// hasLogDetail 查 work_log 是否含指定 detail 子串的条目（可选限定 action）
func hasLogDetail(t *testing.T, eng *Engine, action, sub string) bool {
	t.Helper()
	logs, err := eng.St.ListLogs(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if action != "" && l.Action != action {
			continue
		}
		if strings.Contains(l.Detail, sub) {
			return true
		}
	}
	return false
}

func TestGrounded(t *testing.T) {
	hits := []kb.Hit{{ID: "wiki/a.md", Path: "wiki/a.md", Title: "A"}}
	cases := []struct {
		name       string
		mode       string
		hits       []kb.Hit
		err        error
		wantOK     bool
		wantStatus string // enforce 暂停后为 paused，其余保持 running
		wantLog    string // work_log 期望子串（action=grounding）
		wantAction string // 期望的日志 action；空 = 不校验
	}{
		{"off 跳过", "off", hits, nil, true, "running", "", ""},
		{"warn 有据", "warn", hits, nil, true, "running", "grounding: 1 条依据", "grounding"},
		{"warn 无据", "warn", nil, nil, true, "running", "grounding: NO_BASIS(warn)", "grounding"},
		{"warn 不可达", "warn", nil, fmt.Errorf("connection refused"), true, "running",
			"grounding: KB unreachable(warn)", "grounding"},
		{"enforce 有据", "enforce", hits, nil, true, "running", "grounding: 1 条依据", "grounding"},
		{"enforce 无据暂停", "enforce", nil, nil, false, "paused", "NO_BASIS", "auto_pause"},
		{"enforce 不可达暂停", "enforce", nil, fmt.Errorf("connection refused"), false, "paused",
			"KB unreachable", "auto_pause"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng, _, m := newTestEngine(t)
			fs := &fakeSearcher{hits: c.hits, err: c.err}
			eng.Searcher = fs
			eng.KBMode = c.mode
			ok := eng.grounded(m)
			if ok != c.wantOK {
				t.Fatalf("grounded = %v, want %v", ok, c.wantOK)
			}
			if c.mode == "off" && fs.calls != 0 {
				t.Fatalf("off 模式 Searcher 调用 %d 次, want 0", fs.calls)
			}
			if c.mode != "off" {
				if fs.calls != 1 {
					t.Fatalf("Searcher 调用 %d 次, want 1", fs.calls)
				}
				if fs.lastQuery != "demo 测试任务" || fs.lastLimit != kbSearchLimit {
					t.Fatalf("query=%q limit=%d", fs.lastQuery, fs.lastLimit)
				}
			}
			if got := eng.reload(m.TaskID); got.Status != c.wantStatus {
				t.Fatalf("status = %s, want %s", got.Status, c.wantStatus)
			}
			if c.wantLog != "" && !hasLogDetail(t, eng, c.wantAction, c.wantLog) {
				t.Fatalf("work_log 缺少 action=%s detail 含 %q 的条目", c.wantAction, c.wantLog)
			}
		})
	}
}

// Searcher 为 nil 时（未装配）完全跳过
func TestGroundedNilSearcher(t *testing.T) {
	eng, _, m := newTestEngine(t)
	eng.KBMode = "enforce"
	if !eng.grounded(m) {
		t.Fatal("nil Searcher 不应暂停")
	}
	if got := eng.reload(m.TaskID); got.Status != "running" {
		t.Fatalf("status = %s, want running", got.Status)
	}
}

// Advance 入口 grounding 集成（TASK-004 迁移：检查点从执行前 maybeRun 迁至 Advance）：
// Advance 入口 grounding（TASK-004 迁移：检查点从执行前 maybeRun 迁至 Advance）：
// enforce 无据/不可达 → Advance 返回错误且任务自动暂停；warn → 正常推进且 work_log
// 记 grounding；off → Searcher 零调用。
func TestAdvanceGroundingEnforceNoBasis(t *testing.T) {
	eng, _, m := newTestEngine(t)
	eng.Searcher = &fakeSearcher{} // 空结果
	eng.KBMode = "enforce"
	if err := eng.Advance(m, "report.md"); err == nil {
		t.Fatal("enforce 无据 Advance 应返回错误")
	}
	if got := eng.reload(m.TaskID); got.Status != "paused" {
		t.Fatalf("status = %s, want paused", got.Status)
	}
}

func TestAdvanceGroundingEnforceUnreachable(t *testing.T) {
	eng, _, m := newTestEngine(t)
	eng.Searcher = &fakeSearcher{err: fmt.Errorf("KB unreachable")}
	eng.KBMode = "enforce"
	if err := eng.Advance(m, "report.md"); err == nil {
		t.Fatal("enforce 不可达 Advance 应返回错误")
	}
	if got := eng.reload(m.TaskID); got.Status != "paused" {
		t.Fatalf("status = %s, want paused", got.Status)
	}
}

func TestAdvanceGroundingWarn(t *testing.T) {
	eng, st, m := newTestEngine(t)
	eng.Searcher = &fakeSearcher{} // 空结果
	eng.KBMode = "warn"
	if err := eng.Advance(m, "report.md"); err != nil {
		t.Fatalf("warn 无据不应阻断: %v", err)
	}
	if got := eng.reload(m.TaskID); got.Status != "awaiting_approval" {
		t.Fatalf("status = %s, want awaiting_approval", got.Status)
	}
	logs, err := st.ListLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if l.Action == "grounding" && strings.Contains(l.Detail, "NO_BASIS") {
			return
		}
	}
	t.Fatal("warn 无据应记 grounding work_log")
}

func TestAdvanceGroundingOff(t *testing.T) {
	eng, _, m := newTestEngine(t)
	fs := &fakeSearcher{}
	eng.Searcher = fs
	eng.KBMode = "off"
	if err := eng.Advance(m, "report.md"); err != nil {
		t.Fatal(err)
	}
	if fs.calls != 0 {
		t.Fatalf("off 模式 Searcher 调用 %d 次, want 0", fs.calls)
	}
	if got := eng.reload(m.TaskID); got.Status != "awaiting_approval" {
		t.Fatalf("status = %s, want awaiting_approval", got.Status)
	}
}
