// KB grounding 测试（FINDING-016）：fake Searcher × warn/enforce/off，
// 断言继续/暂停、work_log detail、off 时 Searcher 零调用。:memory: SQLite 实测。
package engine

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"control-api/internal/kb"
	"control-api/internal/tasks"
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

// maybeRun 集成：enforce 无据在 agent 拉起前暂停（Runner 零调用）；
// off 时 Searcher 零调用且 Runner 正常拉起
func TestMaybeRunGrounding(t *testing.T) {
	t.Run("enforce 无据不拉起 agent", func(t *testing.T) {
		eng, _, m := newTestEngine(t)
		eng.Searcher = &fakeSearcher{} // 空结果
		eng.KBMode = "enforce"
		var ran atomic.Int32
		eng.Runner = runnerFunc(func(*tasks.Meta, string, string) (string, error) {
			ran.Add(1)
			return "x", nil
		})
		eng.maybeRun(m)
		time.Sleep(20 * time.Millisecond) // 给（不应存在的）goroutine 留窗口
		if ran.Load() != 0 {
			t.Fatal("enforce 无据不应拉起 agent")
		}
		if got := eng.reload(m.TaskID); got.Status != "paused" {
			t.Fatalf("status = %s, want paused", got.Status)
		}
	})

	t.Run("off 零调用正常执行", func(t *testing.T) {
		eng, _, m := newTestEngine(t)
		fs := &fakeSearcher{}
		eng.Searcher = fs
		eng.KBMode = "off"
		var ran atomic.Int32
		eng.Runner = runnerFunc(func(*tasks.Meta, string, string) (string, error) {
			ran.Add(1)
			return "x", nil
		})
		eng.maybeRun(m)
		deadline := time.Now().Add(2 * time.Second) // maybeRun 异步拉起，轮询确认
		for ran.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if ran.Load() != 1 {
			t.Fatal("off 模式 Runner 应正常拉起")
		}
		if fs.calls != 0 {
			t.Fatalf("off 模式 Searcher 调用 %d 次, want 0", fs.calls)
		}
	})
}

// runnerFunc 函数字面量适配 Runner 接口
type runnerFunc func(m *tasks.Meta, stage, model string) (string, error)

func (f runnerFunc) RunStage(m *tasks.Meta, stage, model string) (string, error) {
	return f(m, stage, model)
}
