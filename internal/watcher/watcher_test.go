// watcher 监听测试：子目录 task.md 修改触发同步、新建任务目录动态纳入监听
// （FINDING-006：fsnotify 非递归缺陷）。:memory: SQLite 实测，不 mock；
// 异步等待用轮询（参照 engine/merge_test.go），不硬编码 sleep；
// sync 调用次数经 watch 的 onSync 回调用原子量观察。
package watcher

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"control-api/internal/store"
)

// newTestStore 返回 :memory: store（单连接保证一致性）
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	st.SetMaxOpenConns(1)
	t.Cleanup(func() { st.Close() })
	return st
}

// writeTask 写入 tasks/<id>/task.md（重复调用即修改 frontmatter 状态）
func writeTask(t *testing.T, dir, id, status string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, id), 0o750); err != nil {
		t.Fatal(err)
	}
	content := "---\ntask_id: " + id + "\ntitle: 测试\nrepo_key: demo\nstatus: " + status + "\n---\n\n# " + id + "\n"
	if err := os.WriteFile(filepath.Join(dir, id, "task.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// taskStatus 查 task_index 中任务状态；未索引返回 ("", false)
func taskStatus(t *testing.T, st *store.Store, id string) (string, bool) {
	t.Helper()
	rows, err := st.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.TaskID == id {
			return r.Status, true
		}
	}
	return "", false
}

// waitFor 轮询等待条件成立（2s 上限，超时即失败）
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startWatch 启动监听并在测试结束时关闭；返回防抖 sync 调用计数
func startWatch(t *testing.T, dir string, st *store.Store) *atomic.Int32 {
	t.Helper()
	var syncs atomic.Int32
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	if err := watch(dir, st, done, func() { syncs.Add(1) }); err != nil {
		t.Fatal(err)
	}
	return &syncs
}

// 场景一：修改已存在 TASK-xxx/task.md → 防抖后触发 sync，索引刷新
func TestWatchSubdirModify(t *testing.T) {
	dir := t.TempDir()
	st := newTestStore(t)
	writeTask(t, dir, "TASK-001", "pending")
	if err := Sync(dir, st); err != nil {
		t.Fatal(err)
	}
	syncs := startWatch(t, dir, st)

	writeTask(t, dir, "TASK-001", "running")
	waitFor(t, "TASK-001 状态刷新为 running", func() bool {
		s, ok := taskStatus(t, st, "TASK-001")
		return ok && s == "running"
	})
	if n := syncs.Load(); n < 1 {
		t.Fatalf("期望至少 1 次防抖 sync，实际 %d", n)
	}
}

// 场景二：监听期间新建 TASK-yyy/ → 动态纳入监听；随后改其 task.md 也触发 sync
func TestWatchNewTaskDir(t *testing.T) {
	dir := t.TempDir()
	st := newTestStore(t)
	startWatch(t, dir, st)

	writeTask(t, dir, "TASK-002", "pending")
	waitFor(t, "TASK-002 被索引", func() bool {
		_, ok := taskStatus(t, st, "TASK-002")
		return ok
	})

	writeTask(t, dir, "TASK-002", "running")
	waitFor(t, "TASK-002 状态刷新为 running", func() bool {
		s, ok := taskStatus(t, st, "TASK-002")
		return ok && s == "running"
	})
}

// 场景三：根目录新增普通文件（非子目录）→ 仍触发 sync 且不回归
func TestWatchRootFile(t *testing.T) {
	dir := t.TempDir()
	st := newTestStore(t)
	writeTask(t, dir, "TASK-001", "pending")
	if err := Sync(dir, st); err != nil {
		t.Fatal(err)
	}
	syncs := startWatch(t, dir, st)

	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("根目录便签"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "根目录文件变化触发 sync", func() bool { return syncs.Load() >= 1 })
	rows, err := st.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != "TASK-001" {
		t.Fatalf("索引被根目录文件污染: %+v", rows)
	}
}

// 场景四：目录删除后清理监听（watcher 与索引均不报错，后续事件仍正常处理）
func TestWatchTaskDirRemoved(t *testing.T) {
	dir := t.TempDir()
	st := newTestStore(t)
	writeTask(t, dir, "TASK-001", "pending")
	if err := Sync(dir, st); err != nil {
		t.Fatal(err)
	}
	syncs := startWatch(t, dir, st)

	if err := os.RemoveAll(filepath.Join(dir, "TASK-001")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "目录删除触发 sync", func() bool { return syncs.Load() >= 1 })

	// 监听集合已清理：删后新建另一任务目录仍被动态纳入
	writeTask(t, dir, "TASK-002", "pending")
	waitFor(t, "TASK-002 被索引", func() bool {
		_, ok := taskStatus(t, st, "TASK-002")
		return ok
	})
}

// 防抖语义：连续事件合并为一次 sync
func TestWatchDebounce(t *testing.T) {
	dir := t.TempDir()
	st := newTestStore(t)
	syncs := startWatch(t, dir, st)

	writeTask(t, dir, "TASK-003", "pending")
	writeTask(t, dir, "TASK-003", "running")
	waitFor(t, "连续写入触发首次 sync", func() bool { return syncs.Load() >= 1 })
	time.Sleep(2 * debounce) // 负向断言需静置一个防抖窗口以上
	if n := syncs.Load(); n != 1 {
		t.Fatalf("连续事件应合并为 1 次 sync，实际 %d", n)
	}
}

// Sync 全量逻辑回归（表驱动）
func TestSync(t *testing.T) {
	t.Run("目录不存在返回 nil", func(t *testing.T) {
		st := newTestStore(t)
		if err := Sync(filepath.Join(t.TempDir(), "nope"), st); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})
	t.Run("空目录无任务", func(t *testing.T) {
		st := newTestStore(t)
		if err := Sync(t.TempDir(), st); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		rows, err := st.ListTasks()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("期望 0 个任务，实际 %d", len(rows))
		}
	})
	t.Run("有效任务被索引且 frontmatter 为准", func(t *testing.T) {
		dir := t.TempDir()
		st := newTestStore(t)
		writeTask(t, dir, "TASK-001", "awaiting_approval")
		if err := Sync(dir, st); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		s, ok := taskStatus(t, st, "TASK-001")
		if !ok || s != "awaiting_approval" {
			t.Fatalf("期望 awaiting_approval，实际 %q (ok=%v)", s, ok)
		}
	})
	t.Run("非法 task.md 跳过不影响其他任务", func(t *testing.T) {
		dir := t.TempDir()
		st := newTestStore(t)
		writeTask(t, dir, "TASK-001", "pending")
		bad := filepath.Join(dir, "TASK-BAD")
		if err := os.MkdirAll(bad, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bad, "task.md"), []byte("没有 frontmatter"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Sync(dir, st); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if _, ok := taskStatus(t, st, "TASK-001"); !ok {
			t.Fatal("TASK-001 未被索引")
		}
		if _, ok := taskStatus(t, st, "TASK-BAD"); ok {
			t.Fatal("TASK-BAD 不应被索引")
		}
	})
}
