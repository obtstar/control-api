// work_log hash 链校验（FINDING-005）：重算校验 + 分叉检测，只读
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// LogChainResult VerifyLog 通过时的结果
type LogChainResult struct {
	Total       int    // 链上总条数
	GenesisPrev string // 首条 prev_hash（可为外部归档尾哈希，只记录不判错）
}

// entryHash 链哈希公式（与存量数据兼容，不得变更）：
// sha256(prev_hash|taskID|stage|action|operator|model|detail)
func entryHash(prev, taskID, stage, action, operator, model, detail string) string {
	body := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", prev, taskID, stage, action, operator, model, detail)
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// VerifyLog 按 id 升序逐条校验 work_log hash 链。
// 发现分叉（两条记录引用同一 prev）、链断裂或哈希不符时，
// 返回第一个问题条目的 id 与原因；首条 prev_hash 不判错（可为外部归档续接）。
func (s *Store) VerifyLog() (*LogChainResult, error) {
	rows, err := s.Query(`
SELECT id, COALESCE(task_id,''), COALESCE(stage,''), action, operator,
       COALESCE(model,''), COALESCE(detail,''), prev_hash, entry_hash
FROM work_log ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("verify work_log: %w", err)
	}
	defer rows.Close()

	res := &LogChainResult{}
	var prevEntry string
	seenPrev := map[string]int64{} // prev_hash -> 首个引用它的条目 id
	for rows.Next() {
		var id int64
		var taskID, stage, action, operator, model, detail, prevHash, stored string
		if err := rows.Scan(&id, &taskID, &stage, &action, &operator,
			&model, &detail, &prevHash, &stored); err != nil {
			return res, fmt.Errorf("verify work_log scan: %w", err)
		}
		if res.Total == 0 {
			res.GenesisPrev = prevHash
		} else if referrer, ok := seenPrev[prevHash]; ok && prevHash != "" {
			return res, fmt.Errorf("work_log 链分叉: id=%d 与 id=%d 引用同一 prev_hash", id, referrer)
		} else if prevHash != prevEntry {
			return res, fmt.Errorf("work_log 链断裂: id=%d prev_hash 不等于前一条 entry_hash", id)
		}
		if got := entryHash(prevHash, taskID, stage, action, operator, model, detail); got != stored {
			return res, fmt.Errorf("work_log 哈希不符: id=%d 重算值与存储 entry_hash 不一致", id)
		}
		if prevHash != "" {
			seenPrev[prevHash] = id
		}
		prevEntry = stored
		res.Total++
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("verify work_log: %w", err)
	}
	return res, nil
}
