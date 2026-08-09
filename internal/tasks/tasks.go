// Package tasks 任务即文档：解析 tasks/TASK-*/task.md（frontmatter 为权威），
// 同步到 task_index（派生索引，可重建）
package tasks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Meta struct {
	TaskID    string `yaml:"task_id"`
	Title     string `yaml:"title"`
	RepoKey   string `yaml:"repo_key"`
	Domain    string `yaml:"domain"` // 领域（可选）：加载 skills/domain/<domain>/
	Stage     string `yaml:"stage"`  // 当前阶段（空=首阶段）
	Status    string `yaml:"status"` // pending/running/awaiting_approval/paused/merged/delivered
	Priority  string `yaml:"priority"`
	Authority string `yaml:"authority"` // 恒 L1（需求级）
	Path      string `yaml:"-"`         // 目录路径（非 frontmatter 字段）
}

// ParseFile 读取 task.md 的 YAML frontmatter（--- 之间）
func ParseFile(path string) (*Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(data)
	if !strings.HasPrefix(s, "---") {
		return nil, fmt.Errorf("%s: 缺少 frontmatter", path)
	}
	end := strings.Index(s[3:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("%s: frontmatter 未闭合", path)
	}
	var m Meta
	if err := yaml.Unmarshal([]byte(s[3:3+end]), &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m.Path = filepath.Dir(path)
	if m.TaskID == "" {
		m.TaskID = filepath.Base(m.Path)
	}
	if m.Authority == "" {
		m.Authority = "L1"
	}
	if m.Status == "" {
		m.Status = "pending"
	}
	return &m, nil
}

// WriteMeta 将状态回写 frontmatter（保留正文）
func WriteMeta(m *Meta) error {
	path := filepath.Join(m.Path, "task.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := string(data)
	end := strings.Index(s[3:], "\n---")
	if !strings.HasPrefix(s, "---") || end < 0 {
		return fmt.Errorf("%s: frontmatter 异常", path)
	}
	fm, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	out := "---\n" + string(fm) + s[3+end:]
	return os.WriteFile(path, []byte(out), 0o644)
}

// Scan 扫描 tasks/ 下全部任务（TASK-*/task.md）
func Scan(dir string) ([]*Meta, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		f := filepath.Join(dir, e.Name(), "task.md")
		if _, err := os.Stat(f); err != nil {
			continue
		}
		m, err := ParseFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[tasks] 跳过 %s: %v\n", e.Name(), err)
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
