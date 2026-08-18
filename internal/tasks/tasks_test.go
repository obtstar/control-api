// tasks 包测试：frontmatter 解析/回写（FINDING-038 分隔符探测加固）
package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 首行必须恰为 "---"：以 "----" 等前缀碰撞开头的文件应得"缺少 frontmatter"的
// 明确错误，而非截断后的 yaml 解析错（FINDING-038 加固前为后者）
func TestParseFileOpeningLineExact(t *testing.T) {
	p := filepath.Join(t.TempDir(), "task.md")
	content := "----\ntask_id: TASK-009\n---\n\n# 正文\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseFile(p)
	if err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Fatalf("应报 frontmatter 缺失类错误，得: %v", err)
	}
}

// 正文含独立 "---" 行（常见分隔线）时 WriteMeta 不得错位：
// 状态回写后正文逐字节保留，且可再次解析（回归守卫）
func TestWriteMetaPreservesBodyWithDashes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "TASK-009"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "\n# 标题\n\n第一节\n\n---\n\n第二节（--- 分隔线后）\n"
	content := "---\ntask_id: TASK-009\ntitle: 测试\nstatus: pending\n---\n" + body
	p := filepath.Join(dir, "TASK-009", "task.md")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	m.Status = "running"
	if err := WriteMeta(m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), body) {
		t.Fatalf("正文被改写:\n%s", data)
	}
	m2, err := ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Status != "running" {
		t.Fatalf("Status = %q, want running", m2.Status)
	}
}
