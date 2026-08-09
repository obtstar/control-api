package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"control-api/internal/config"
	"control-api/internal/tasks"
)

func writeFile(p, c string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(c), 0o644)
}

func TestBuildArgvTemplateMode(t *testing.T) {
	r := &Runner{Cfg: config.AgentConfig{Command: "fake-pi -m {{.Model}} {{.Prompt}}"}}
	m := &tasks.Meta{TaskID: "TASK-001", Title: "测试", Path: "/tmp/t", Status: "running", Stage: "design"}
	argv, prompt, err := r.buildArgv(m, "design", "coding")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) < 4 || argv[0] != "fake-pi" {
		t.Fatalf("argv = %v", argv)
	}
	if argv[2] != "coding" {
		t.Errorf("模型占位符未替换: %q", argv[2])
	}
	if !strings.Contains(prompt, "TASK-001") {
		t.Errorf("提示词应含任务ID: %q", prompt)
	}
}

func TestBuildArgvRealMode(t *testing.T) {
	r := &Runner{Cfg: config.AgentConfig{
		Bin:       "pi",
		Models:    map[string]string{"coding": "kimi-for-coding"},
		SkillsDir: "/nonexistent/skills",
	}}
	m := &tasks.Meta{TaskID: "TASK-001", Title: "测试", Path: "/tmp/t", Stage: "design"}
	argv, _, err := r.buildArgv(m, "design", "coding")
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "pi" {
		t.Errorf("argv[0] = %q, want pi", argv[0])
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--print", "--no-session", "--model", "kimi-for-coding"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv 缺少 %q: %s", want, joined)
		}
	}
}

func TestStageSkillsNonexistentDir(t *testing.T) {
	r := &Runner{Cfg: config.AgentConfig{SkillsDir: "/nonexistent/skills"}}
	if got := r.skills(&tasks.Meta{TaskID: "TASK-001"}, "design"); len(got) != 0 {
		t.Errorf("skills 目录不存在时应返回空，got %v", got)
	}
}

func TestStageSkillsIncludesEnforce(t *testing.T) {
	dir := t.TempDir()
	write := func(p, c string) {
		t.Helper()
		if err := writeFile(p, c); err != nil {
			t.Fatal(err)
		}
	}
	write(dir+"/stage/design/SKILL.md", "x")
	write(dir+"/domain/frontend-dev/SKILL.md", "x")
	write(dir+"/enforce/authority-check/SKILL.md", "x")
	write(dir+"/enforce/grounding-check/SKILL.md", "x")

	r := &Runner{Cfg: config.AgentConfig{SkillsDir: dir}}
	got := r.skills(&tasks.Meta{TaskID: "TASK-001", Domain: "frontend-dev"}, "design")
	want := []string{dir + "/stage/design", dir + "/domain/frontend-dev", dir + "/enforce/authority-check", dir + "/enforce/grounding-check"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
