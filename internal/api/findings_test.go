// FINDINGS.md 解析与 GET /api/findings 测试
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"control-api/internal/config"
)

const findingsFixture = `# FINDINGS — 平台发现问题一览

| ID | 日期 | 来源 | 现象 | 证据 | 影响 | 状态 | 去向 |
|----|------|------|------|------|------|------|------|
| FINDING-001 | 2026-08-09 | 架构评审 | 现象甲 | 证据甲 | 影响甲 | open | |
| FINDING-002 | 2026-08-09 | web 核查 | 现象乙 | | 影响乙 | fixed | control-web abc1234 |

## 已修复（留存痕）

- 2026-08-09：非表格行，应忽略
`

func writeFindingsFile(t *testing.T, home string, content string) {
	t.Helper()
	dir := filepath.Join(home, "control-center", "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "FINDINGS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseFindings(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"正常两行", findingsFixture, 2},
		{"空内容", "", 0},
		{"仅表头与分隔行", "| ID | 日期 |\n|----|------|\n", 0},
		{"非 FINDING 开头行跳过", "| TASK-001 | a | b | c | d | e | f | g |\n", 0},
		{"列数不足跳过", "| FINDING-001 | 只有两列 |\n", 0},
		{"混杂行只取数据行", "前言\n" + findingsFixture + "\n尾注\n", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseFindings([]byte(c.input)); len(got) != c.want {
				t.Fatalf("len = %d, want %d", len(got), c.want)
			}
		})
	}
}

// 字段级断言：列映射正确，状态值原样透传，空单元格保留为空串
func TestParseFindingsFields(t *testing.T) {
	got := parseFindings([]byte(findingsFixture))
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	f0 := got[0]
	if f0.ID != "FINDING-001" || f0.Date != "2026-08-09" || f0.Source != "架构评审" ||
		f0.Phenomenon != "现象甲" || f0.Evidence != "证据甲" || f0.Impact != "影响甲" ||
		f0.Status != "open" || f0.Target != "" {
		t.Fatalf("字段映射错误: %+v", f0)
	}
	if got[1].Status != "fixed" || got[1].Target != "control-web abc1234" || got[1].Evidence != "" {
		t.Fatalf("状态透传/空列错误: %+v", got[1])
	}
}

func TestListFindings(t *testing.T) {
	newServer := func(home string) *server {
		return &server{cfg: &config.Config{Paths: config.PathsConfig{Home: home}}}
	}

	t.Run("文件缺失返回 500", func(t *testing.T) {
		s := newServer(t.TempDir())
		w := httptest.NewRecorder()
		s.listFindings(w, httptest.NewRequest(http.MethodGet, "/api/findings", nil))
		if w.Code != 500 {
			t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("正常解析返回 200", func(t *testing.T) {
		home := t.TempDir()
		writeFindingsFile(t, home, findingsFixture)
		s := newServer(home)
		w := httptest.NewRecorder()
		s.listFindings(w, httptest.NewRequest(http.MethodGet, "/api/findings", nil))
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
		}
		var got []finding
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].ID != "FINDING-001" || got[1].Status != "fixed" {
			t.Fatalf("响应内容错误: %s", w.Body.String())
		}
	})

	t.Run("无数据行返回空数组而非 null", func(t *testing.T) {
		home := t.TempDir()
		writeFindingsFile(t, home, "# 空表\n")
		s := newServer(home)
		w := httptest.NewRecorder()
		s.listFindings(w, httptest.NewRequest(http.MethodGet, "/api/findings", nil))
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var got []finding
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("响应不是 JSON 数组: %s", w.Body.String())
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("got = %v, want 空数组", got)
		}
	})
}
