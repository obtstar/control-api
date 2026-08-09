// Package agent pi 进程执行层：每阶段拉起一次 pi（非交互 print 模式），
// 产物落 tasks/<id>/ 目录（Git 权威），输出摘要记 work_log。
// 真实调用：pi --print --no-session --model <模式> --skill <阶段skill> <提示词>
// 测试/回退：agent.command 配置模板（fake-pi）时走模板执行。
package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"control-api/internal/config"
	"control-api/internal/tasks"
)

type Runner struct {
	Cfg config.AgentConfig
}

// RunStage 在任务工作目录执行一个阶段，返回产物路径；model 为流水线别名（cheap/coding/heavy）
func (r *Runner) RunStage(m *tasks.Meta, stage, model string) (string, error) {
	argv, _, err := r.buildArgv(m, stage, model)
	if err != nil {
		return "", err
	}

	timeout := time.Duration(r.Cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = m.Path
	if r.Cfg.ScriptsDir != "" { // branch.sh 等平台脚本入 PATH（pi 进程内可裸调）
		cmd.Env = append(os.Environ(), "PATH="+r.Cfg.ScriptsDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	runErr := cmd.Run()

	// 产物落盘：stage 报告（无论成败都留档）
	artifact := filepath.Join(m.Path, fmt.Sprintf("report-%s.md", stage))
	content := fmt.Sprintf("# %s / %s\n\n```\n%s\n%s\n```\n",
		m.TaskID, stage, stdout.String(), stderr.String())
	os.WriteFile(artifact, []byte(content), 0o644)

	if ctx.Err() == context.DeadlineExceeded {
		return artifact, fmt.Errorf("阶段 %s 执行超时（%v）", stage, timeout)
	}
	if runErr != nil {
		return artifact, fmt.Errorf("阶段 %s 执行失败: %w", stage, runErr)
	}
	return artifact, nil
}

// buildArgv 构造 pi 调用参数；agent.command 配置模板时按模板展开（测试用）
func (r *Runner) buildArgv(m *tasks.Meta, stage, model string) ([]string, string, error) {
	prompt := buildPrompt(m, stage)
	if r.Cfg.Command != "" {
		argv := []string{}
		for _, tok := range strings.Fields(r.Cfg.Command) {
			argv = append(argv, strings.NewReplacer(
				"{{.Prompt}}", prompt,
				"{{.TaskID}}", m.TaskID,
				"{{.Stage}}", stage,
				"{{.Model}}", model,
				"{{.WorkDir}}", m.Path,
			).Replace(tok))
		}
		return argv, prompt, nil
	}

	bin := r.Cfg.Bin
	if bin == "" {
		bin = "pi"
	}
	argv := []string{bin, "--print", "--no-session", "--model", r.Cfg.ResolveModel(model)}
	for _, sk := range r.skills(m, stage) {
		argv = append(argv, "--skill", sk)
	}
	argv = append(argv, prompt)
	return argv, prompt, nil
}

// skills 当前阶段需加载的 skill：阶段 skill + 领域 skill（task.md domain 指定）+ enforce 四件套
func (r *Runner) skills(m *tasks.Meta, stage string) []string {
	if r.Cfg.SkillsDir == "" {
		return nil
	}
	var out []string
	if p := filepath.Join(r.Cfg.SkillsDir, "stage", stage); exists(p) {
		out = append(out, p)
	}
	if m.Domain != "" {
		if p := filepath.Join(r.Cfg.SkillsDir, "domain", m.Domain); exists(p) {
			out = append(out, p)
		}
	}
	enforce := filepath.Join(r.Cfg.SkillsDir, "enforce")
	if entries, err := os.ReadDir(enforce); err == nil {
		for _, e := range entries {
			if e.IsDir() && exists(filepath.Join(enforce, e.Name())) {
				out = append(out, filepath.Join(enforce, e.Name()))
			}
		}
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// buildPrompt 阶段提示词：任务需求 + 阶段指令 + 权柄约束（18 章硬编码）
func buildPrompt(m *tasks.Meta, stage string) string {
	body := readBody(filepath.Join(m.Path, "task.md"))
	return fmt.Sprintf(`任务: %s（%s）
阶段: %s

需求（L1 权柄，不可修改）:
%s

约束（必须遵守）:
1. 有据可依：所有产出引用 KB 依据（文档+段落），无据则输出 NO_BASIS 并停止
2. 不逆行：不得修改比 %s 更高级别的文档
3. 产出写入当前目录，文件名以 %s- 前缀
`, m.TaskID, m.Title, stage, body, stage, stage)
}

// readBody 提取 task.md 正文（frontmatter 之后）
func readBody(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := string(data)
	if !strings.HasPrefix(s, "---") {
		return s
	}
	if i := strings.Index(s[3:], "\n---"); i >= 0 {
		return strings.TrimSpace(s[3+i+4:])
	}
	return s
}
