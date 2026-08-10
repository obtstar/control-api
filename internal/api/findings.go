// 问题一览端点：每次请求实时解析 FINDINGS.md 权威表（文件极小，不做缓存）
package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type finding struct {
	ID         string `json:"id"`
	Date       string `json:"date"`
	Source     string `json:"source"`
	Phenomenon string `json:"phenomenon"`
	Evidence   string `json:"evidence"`
	Impact     string `json:"impact"`
	Status     string `json:"status"`
	Target     string `json:"target"`
}

// listFindings GET /api/findings
func (s *server) listFindings(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.cfg.Paths.Home, "control-center", "docs", "FINDINGS.md")
	data, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("读取 FINDINGS.md: %w", err))
		return
	}
	writeJSON(w, parseFindings(data))
}

// parseFindings 解析 Markdown 表格数据行；表头/分隔行/非 FINDING 开头行/列数不足行跳过
func parseFindings(data []byte) []finding {
	findings := make([]finding, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| FINDING-") {
			continue
		}
		cells := strings.Split(line, "|")
		// 整行形如 "| c1 | c2 | ... | c8 |"：首尾为空串，8 列需至少 10 段
		if len(cells) < 10 {
			continue
		}
		f := finding{}
		dst := []*string{&f.ID, &f.Date, &f.Source, &f.Phenomenon, &f.Evidence, &f.Impact, &f.Status, &f.Target}
		for i, p := range dst {
			*p = strings.TrimSpace(cells[i+1])
		}
		findings = append(findings, f)
	}
	return findings
}
