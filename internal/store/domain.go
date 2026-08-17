// store 领域方法：task_index 同步、审批队列、work_log hash 链
package store

import (
	"database/sql"
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
	UpdatedBy string `json:"updated_by"`
	Path      string `json:"path"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Store) ListTasks() ([]TaskRow, error) {
	rows, err := s.Query(`
SELECT t.task_id, t.title, t.repo_key, t.stage, t.status, t.authority,
       COALESCE(l.operator, '') AS updated_by, t.path, t.updated_at
FROM task_index t
LEFT JOIN (
  SELECT task_id, operator, MAX(id) AS max_id
  FROM work_log
  GROUP BY task_id
) latest ON t.task_id = latest.task_id
LEFT JOIN work_log l ON l.id = latest.max_id
ORDER BY t.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		var t TaskRow
		if err := rows.Scan(&t.TaskID, &t.Title, &t.RepoKey, &t.Stage, &t.Status,
			&t.Authority, &t.UpdatedBy, &t.Path, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Log 写入 work_log（hash 链：entry_hash = sha256(prev_hash + 内容)）。
// prev 读取与 INSERT 放在同一事务（连接串 _txlock=immediate，Begin 即取写锁），
// 串行化并发写，避免两条记录引用同一 prev 造成分叉链（FINDING-005）。
func (s *Store) Log(taskID, stage, action, operator, model, detail string) error {
	tx, err := s.Begin()
	if err != nil {
		return fmt.Errorf("work_log 链事务: %w", err)
	}
	defer tx.Rollback()
	var prev string
	err = tx.QueryRow(`SELECT entry_hash FROM work_log ORDER BY id DESC LIMIT 1`).Scan(&prev)
	if err == sql.ErrNoRows {
		prev = "" // 创世行（重置续接的首行由外部归档注入，不经此路径）
	} else if err != nil {
		return fmt.Errorf("work_log 读 prev_hash: %w", err)
	}
	_, err = tx.Exec(`
INSERT INTO work_log (task_id, stage, action, operator, model, detail, prev_hash, entry_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, stage, action, operator, model, detail, prev,
		entryHash(prev, taskID, stage, action, operator, model, detail))
	if err != nil {
		return fmt.Errorf("work_log 写入: %w", err)
	}
	return tx.Commit()
}

// ActionAgentError 阶段执行失败写入的 work_log action（连败计数以此为据）
const ActionAgentError = "agent_error"

// ConsecutiveFailures 统计任务最近的连续阶段失败次数（从 work_log 派生，不改 schema）：
// 按 id 倒序数前缀连续的 ActionAgentError 条数，遇到任何非失败 action 即停
func (s *Store) ConsecutiveFailures(taskID string) (int, error) {
	rows, err := s.Query(`SELECT action FROM work_log WHERE task_id=? ORDER BY id DESC`, taskID)
	if err != nil {
		return 0, fmt.Errorf("consecutive failures %s: %w", taskID, err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			return 0, fmt.Errorf("consecutive failures %s: %w", taskID, err)
		}
		if action != ActionAgentError {
			break
		}
		n++
	}
	return n, rows.Err()
}

// LastFailureAt 返回任务最近一次 agent_error 的时间；无失败记录返回零值。
// created_at 经驱动归一化为 RFC3339（兼容解析旧式 "YYYY-MM-DD HH:MM:SS" 文本）。
func (s *Store) LastFailureAt(taskID string) (time.Time, error) {
	var ts string
	err := s.QueryRow(`SELECT created_at FROM work_log WHERE task_id=? AND action=?
	                   ORDER BY id DESC LIMIT 1`, taskID, ActionAgentError).Scan(&ts)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("last failure %s: %w", taskID, err)
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("解析 work_log 时间 %q: 非常见格式", ts)
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

type PendingApproval struct {
	TaskID    string `json:"task_id"`
	Title     string `json:"title"`
	Stage     string `json:"stage"`
	Role      string `json:"role"`
	Artifact  string `json:"artifact"`
	CreatedAt string `json:"created_at"`
}

// ListPendingApprovals 返回当前角色可审批的 awaiting_approval 任务；admin 返回全部。
func (s *Store) ListPendingApprovals(role string) ([]PendingApproval, error) {
	query := `
SELECT a.task_id, t.title, a.stage, a.role, COALESCE(a.artifact, ''), a.created_at
FROM approval a
JOIN task_index t ON t.task_id = a.task_id
WHERE a.decision IS NULL AND t.status = 'awaiting_approval'`
	args := []any{}
	if role != "admin" {
		query += " AND a.role = ?"
		args = append(args, role)
	}
	query += " ORDER BY a.created_at DESC"
	rows, err := s.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingApproval
	for rows.Next() {
		var p PendingApproval
		if err := rows.Scan(&p.TaskID, &p.Title, &p.Stage, &p.Role, &p.Artifact, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
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
