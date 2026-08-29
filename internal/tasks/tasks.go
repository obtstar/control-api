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
	Archived  bool   `yaml:"archived"`  // 归档（delivered 后可归档，默认 false）
	Path      string `yaml:"-"`         // 目录路径（非 frontmatter 字段）
}

// splitFrontmatter 切分 frontmatter（lines[0] 与闭合行）与正文。
// 行级精确匹配 "---"：取代子串探测，"----"/"\n---x" 等前缀碰撞不再错位（FINDING-038）
func splitFrontmatter(s string) (fm, rest string, err error) {
	lines := strings.Split(s, "\n")
	if lines[0] != "---" {
		return "", "", fmt.Errorf("缺少 frontmatter")
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i:], "\n"), nil
		}
	}
	return "", "", fmt.Errorf("frontmatter 未闭合")
}

// ParseFile 读取 task.md 的 YAML frontmatter（--- 之间）
func ParseFile(path string) (*Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, _, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var m Meta
	if err := yaml.Unmarshal([]byte(fm), &m); err != nil {
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
	_, rest, err := splitFrontmatter(string(data))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	fm, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	out := "---\n" + string(fm) + rest
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
