// Package store SQLite 运行时层（WAL 模式；派生索引可重建，work_log 归档后可重置）
// 08 章数据模型修订：task 表降级为派生索引（权威在 Git tasks/），
// work_log 为运行期流水（归档进 Git 后可清空）
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	*sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	s := &Store{db}
	return s, s.migrate()
}

func (s *Store) migrate() error {
	_, err := s.Exec(`
CREATE TABLE IF NOT EXISTS task_index (           -- 派生：由 tasks/TASK-*/task.md 重建
  task_id     TEXT PRIMARY KEY,
  title       TEXT NOT NULL DEFAULT '',
  repo_key    TEXT,
  stage       TEXT NOT NULL DEFAULT 'requirements',
  status      TEXT NOT NULL DEFAULT 'pending',     -- pending/running/awaiting_approval/paused/merged/delivered
  authority   TEXT NOT NULL DEFAULT 'L1',
  path        TEXT NOT NULL,                       -- tasks/ 目录路径
  updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS work_log (             -- 运行期流水（归档后可清空，hash 链）
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id     TEXT,
  stage       TEXT,
  action      TEXT NOT NULL,
  operator    TEXT NOT NULL DEFAULT 'agent',       -- agent@role / 用户名
  model       TEXT,
  detail      TEXT,
  prev_hash   TEXT NOT NULL DEFAULT '',
  entry_hash  TEXT NOT NULL DEFAULT '',
  created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_work_log_task ON work_log(task_id, id);
CREATE TABLE IF NOT EXISTS approval (             -- 审批队列（按角色路由）
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id     TEXT NOT NULL,
  stage       TEXT NOT NULL,
  role        TEXT NOT NULL,                       -- designer/tester/customer/team
  artifact    TEXT,
  decision    TEXT,                                -- NULL=待审批 / approved / rejected
  comment     TEXT,
  decided_by  TEXT,
  decided_at  DATETIME,
  created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
`)
	return err
}
