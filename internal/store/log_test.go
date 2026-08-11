// work_log hash 链测试（FINDING-005）：并发写不分叉、篡改检测、创世行续接、空表
package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// openMemStore :memory: 库（每连接一个库，限单连接保证一致）
func openMemStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	st.SetMaxOpenConns(1)
	t.Cleanup(func() { st.Close() })
	return st
}

// TestLogConcurrentChainIntact 并发写后链必须完整、无分叉。
// 用临时文件库 + 多连接真实并发（:memory: 限单连接无法复现读改写竞态）。
func TestLogConcurrentChainIntact(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	const writers, perWriter = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				err := st.Log(fmt.Sprintf("TASK-%03d", w), "coding", "run",
					"agent", "", fmt.Sprintf("iter %d", i))
				if err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发写失败: %v", err)
	}

	res, err := st.VerifyLog()
	if err != nil {
		t.Fatalf("并发写后链校验失败: %v", err)
	}
	if want := writers * perWriter; res.Total != want {
		t.Fatalf("总条数 = %d, 期望 %d", res.Total, want)
	}
	// 无分叉：每个非空 prev_hash 至多被一条后继引用
	var forks int
	err = st.QueryRow(`SELECT COUNT(*) FROM (
	  SELECT prev_hash FROM work_log WHERE prev_hash != ''
	  GROUP BY prev_hash HAVING COUNT(*) > 1)`).Scan(&forks)
	if err != nil {
		t.Fatal(err)
	}
	if forks != 0 {
		t.Fatalf("存在 %d 个被多条记录引用的 prev_hash（分叉链）", forks)
	}
}

// TestVerifyLogTamper 篡改任一字段或 entry_hash，VerifyLog 必须报出该行
func TestVerifyLogTamper(t *testing.T) {
	cases := []struct {
		name   string
		update string
		wantID string
	}{
		{"detail 被改", `UPDATE work_log SET detail='篡改' WHERE id=2`, "id=2"},
		{"entry_hash 被改", `UPDATE work_log SET entry_hash='deadbeef' WHERE id=3`, "id=3"},
		{"prev_hash 被改", `UPDATE work_log SET prev_hash='cafe' WHERE id=4`, "id=4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openMemStore(t)
			for i := 0; i < 5; i++ {
				if err := st.Log("TASK-001", "coding", "run", "agent", "",
					fmt.Sprintf("detail %d", i)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.Exec(tc.update); err != nil {
				t.Fatal(err)
			}
			_, err := st.VerifyLog()
			if err == nil {
				t.Fatal("篡改后 VerifyLog 未报错")
			}
			if !strings.Contains(err.Error(), tc.wantID) {
				t.Fatalf("错误未指向 %s: %v", tc.wantID, err)
			}
		})
	}
}

// TestVerifyLogGenesisExternalPrev 首条带非空 prev_hash（模拟上期归档续接）不判错
func TestVerifyLogGenesisExternalPrev(t *testing.T) {
	st := openMemStore(t)
	archiveTail := strings.Repeat("ab", 32)
	if _, err := st.Exec(`
INSERT INTO work_log (task_id, stage, action, operator, model, detail, prev_hash, entry_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"TASK-000", "deliver", "archive", "agent", "", "上期归档尾", archiveTail,
		entryHash(archiveTail, "TASK-000", "deliver", "archive", "agent", "", "上期归档尾")); err != nil {
		t.Fatal(err)
	}
	if err := st.Log("TASK-001", "requirements", "create", "human", "", "新周期首条"); err != nil {
		t.Fatal(err)
	}
	res, err := st.VerifyLog()
	if err != nil {
		t.Fatalf("创世行续接被判错: %v", err)
	}
	if res.Total != 2 {
		t.Fatalf("总条数 = %d, 期望 2", res.Total)
	}
	if res.GenesisPrev != archiveTail {
		t.Fatalf("GenesisPrev = %q, 期望归档尾哈希", res.GenesisPrev)
	}
}

// TestVerifyLogEmpty 空表校验通过（0 条）
func TestVerifyLogEmpty(t *testing.T) {
	st := openMemStore(t)
	res, err := st.VerifyLog()
	if err != nil {
		t.Fatalf("空表校验失败: %v", err)
	}
	if res.Total != 0 {
		t.Fatalf("空表 Total = %d", res.Total)
	}
}
