// store 领域方法：task_index 同步、审批队列、work_log hash 链
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"control-api/internal/tasks"
)

// UpsertTask 同步任务索引（派生数据，以 frontmatter 为准）
func (s *Store) UpsertTask(m *tasks.Meta) error {
	_, err := s.Exec(`
INSERT INTO task_index (task_id, title, repo_key, stage, status, authority, path, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(task_id) DO UPDATE SET
  title=excluded.title, repo_key=excluded.repo_key, stage=excluded.stage,
  status=excluded.status, authority=excluded.authority,
  path=excluded.path, updated_at=excluded.updated_at`,
		m.TaskID, m.Title, m.RepoKey, m.Stage, m.Status, m.Authority, m.Path,
		time.Now().Format(time.DateTime))
	return err
}

type TaskRow struct {
	TaskID    string `json:"task_id"`
	Title     string `json:"title"`
	RepoKey   string `json:"repo_key"`
	Stage     string `json:"stage"`
	Status    string `json:"status"`
	Authority string `json:"authority"`
	Path      string `json:"path"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Store) ListTasks() ([]TaskRow, error) {
	rows, err := s.Query(`SELECT task_id,title,repo_key,stage,status,authority,path,updated_at
	                      FROM task_index ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		var t TaskRow
		if err := rows.Scan(&t.TaskID, &t.Title, &t.RepoKey, &t.Stage, &t.Status,
			&t.Authority, &t.Path, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Log 写入 work_log（hash 链：entry_hash = sha256(prev_hash + 内容)）
func (s *Store) Log(taskID, stage, action, operator, model, detail string) error {
	var prev string
	s.QueryRow(`SELECT entry_hash FROM work_log ORDER BY id DESC LIMIT 1`).Scan(&prev)
	body := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", prev, taskID, stage, action, operator, model, detail)
	sum := sha256.Sum256([]byte(body))
	_, err := s.Exec(`
INSERT INTO work_log (task_id, stage, action, operator, model, detail, prev_hash, entry_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, stage, action, operator, model, detail, prev, hex.EncodeToString(sum[:]))
	return err
}

type LogRow struct {
	ID        int64  `json:"id"`
	TaskID    string `json:"task_id"`
	Stage     string `json:"stage"`
	Action    string `json:"action"`
	Operator  string `json:"operator"`
	Model     string `json:"model"`
	Detail    string `json:"detail"`
	EntryHash string `json:"entry_hash"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) ListLogs(limit int) ([]LogRow, error) {
	rows, err := s.Query(`SELECT id,task_id,stage,action,operator,model,detail,entry_hash,created_at
	                      FROM work_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogRow
	for rows.Next() {
		var r LogRow
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Stage, &r.Action, &r.Operator,
			&r.Model, &r.Detail, &r.EntryHash, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NewApproval 阶段完成进入待审批
func (s *Store) NewApproval(taskID, stage, role, artifact string) error {
	_, err := s.Exec(`INSERT INTO approval (task_id, stage, role, artifact) VALUES (?, ?, ?, ?)`,
		taskID, stage, role, artifact)
	return err
}

// Decide 审批裁决（approved/rejected + 批注）
func (s *Store) Decide(taskID, stage, decision, comment, by string) error {
	r, err := s.Exec(`
UPDATE approval SET decision=?, comment=?, decided_by=?, decided_at=CURRENT_TIMESTAMP
WHERE task_id=? AND stage=? AND decision IS NULL`,
		decision, comment, by, taskID, stage)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return fmt.Errorf("无待审批记录: %s/%s", taskID, stage)
	}
	return nil
}

// ── authn 存储 ───────────────────────────────────────────────

func (s *Store) AddUser(username, hash, role string) error {
	_, err := s.Exec(`INSERT INTO users (username, pass_hash, role) VALUES (?, ?, ?)
	                  ON CONFLICT(username) DO UPDATE SET pass_hash=excluded.pass_hash, role=excluded.role`,
		username, hash, role)
	return err
}

func (s *Store) GetUser(username string) (hash, role string, err error) {
	err = s.QueryRow(`SELECT pass_hash, role FROM users WHERE username=?`, username).Scan(&hash, &role)
	return
}

func (s *Store) AddSession(token, username, role string, exp time.Time) error {
	_, err := s.Exec(`INSERT INTO sessions (token, username, role, expires_at) VALUES (?, ?, ?, ?)`,
		token, username, role, exp.Unix())
	return err
}

func (s *Store) GetSession(token string) (username, role string, exp time.Time, err error) {
	var secs int64
	err = s.QueryRow(`SELECT username, role, expires_at FROM sessions WHERE token=?`, token).
		Scan(&username, &role, &secs)
	if err != nil {
		return
	}
	exp = time.Unix(secs, 0)
	return
}

// ApprovalRoleOf 查询任务当前待审批的角色
func (s *Store) ApprovalRoleOf(taskID string) (string, error) {
	var role string
	err := s.QueryRow(`SELECT role FROM approval WHERE task_id=? AND decision IS NULL
	                   ORDER BY id DESC LIMIT 1`, taskID).Scan(&role)
	return role, err
}
