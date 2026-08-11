// pipeline.yaml 解析测试：顶层 circuit_breaker 段不再被静默丢弃（FINDING-002）
package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesCircuitBreaker(t *testing.T) {
	dir := t.TempDir()
	content := `
pipeline:
  stages:
    - id: coding
      approval: required
      on_reject: retry
      agent: [implement]        # 未实现字段继续容忍
circuit_breaker:
  consecutive_failures: 5
  token_budget_per_task: 500000
  action: auto_pause_and_notify
authority:                      # 其他顶层段继续容忍
  grounding: required
`
	path := filepath.Join(dir, "pipeline.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cb := p.CircuitBreaker
	if cb.ConsecutiveFailures != 5 {
		t.Fatalf("consecutive_failures = %d, want 5", cb.ConsecutiveFailures)
	}
	if cb.TokenBudgetPerTask != 500000 {
		t.Fatalf("token_budget_per_task = %d, want 500000", cb.TokenBudgetPerTask)
	}
	if cb.Action != "auto_pause_and_notify" {
		t.Fatalf("action = %q, want auto_pause_and_notify", cb.Action)
	}
	if got := p.FailureThreshold(); got != 5 {
		t.Fatalf("FailureThreshold = %d, want 5", got)
	}
}

// 缺省/为 0 时阈值默认 3
func TestFailureThresholdDefault(t *testing.T) {
	cases := []struct {
		name string
		p    *Pipeline
		want int
	}{
		{"未配置", &Pipeline{}, 3},
		{"配置为 0", &Pipeline{CircuitBreaker: CircuitBreaker{ConsecutiveFailures: 0}}, 3},
		{"配置为 2", &Pipeline{CircuitBreaker: CircuitBreaker{ConsecutiveFailures: 2}}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.FailureThreshold(); got != c.want {
				t.Fatalf("FailureThreshold = %d, want %d", got, c.want)
			}
		})
	}
}

// merge 终审闸权力模型校验（FINDING 权力模型）：approval 不得为 auto，
// 依据 control-center/docs/architecture/00-principles.md 审批闸原则 + 18-authority.md merge 终审归团队
func TestLoadValidatesMergeApproval(t *testing.T) {
	cases := []struct {
		name    string
		stages  string
		wantErr bool
	}{
		{"merge=auto 拒载", `
    - id: coding
      approval: required
    - id: merge
      approval: auto
    - id: deliver
      approval: auto
`, true},
		{"merge=team_mr_review 通过", `
    - id: coding
      approval: required
    - id: merge
      approval: team_mr_review
    - id: deliver
      approval: auto
`, false},
		{"merge=required 通过", `
    - id: coding
      approval: required
    - id: merge
      approval: required
    - id: deliver
      approval: auto
`, false},
		{"无 merge 阶段通过", `
    - id: coding
      approval: required
    - id: deliver
      approval: auto
`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "pipeline.yaml")
			if err := os.WriteFile(path, []byte("pipeline:\n  stages:"+c.stages), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if c.wantErr && err == nil {
				t.Fatal("应拒绝加载")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("应通过: %v", err)
			}
		})
	}
}
