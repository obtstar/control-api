// Package pipeline 加载 orchestration/workflows/*.yaml，提供状态机流转。
// 规则：每阶段一审批闸（18 章）；approval: required|auto|team_mr_review；
// on_reject: retry（重做本阶段）| back_to_coding（打回编码）
package pipeline

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Stage struct {
	ID       string `yaml:"id"`
	Approval string `yaml:"approval"`  // required | auto | team_mr_review
	OnReject string `yaml:"on_reject"` // retry | back_to_coding
	Model    string `yaml:"model"`     // LiteLLM 别名：cheap/coding/heavy（空=coding）
	Webhook  string `yaml:"webhook"`   // 终审回传事件名（如 merge_event，仅声明）
	DoneWhen string `yaml:"done_when"` // 完成判据（如 merged_by_teammate，仅声明）
}

// CircuitBreaker 任务级熔断（pipeline.yaml 顶层 circuit_breaker 段）
type CircuitBreaker struct {
	ConsecutiveFailures int    `yaml:"consecutive_failures"`  // 连败阈值（<=0 时按默认 3）
	TokenBudgetPerTask  int    `yaml:"token_budget_per_task"` // 声明项，暂未执行
	Action              string `yaml:"action"`                // auto_pause_and_notify 等，均按 auto pause 处理
}

// defaultFailureThreshold CircuitBreaker 缺省连败阈值
const defaultFailureThreshold = 3

// FailureThreshold 连败熔断阈值（缺省/为 0 时默认 3）
func (p *Pipeline) FailureThreshold() int {
	if p.CircuitBreaker.ConsecutiveFailures <= 0 {
		return defaultFailureThreshold
	}
	return p.CircuitBreaker.ConsecutiveFailures
}

// Model 返回阶段模型别名（默认 coding）
func (p *Pipeline) Model(id string) string {
	_, s := p.stage(id)
	if s == nil || s.Model == "" {
		return "coding"
	}
	return s.Model
}

type Pipeline struct {
	Stages         []Stage        `yaml:"-"`
	CircuitBreaker CircuitBreaker `yaml:"-"`
}

type file struct {
	Pipeline struct {
		Stages []Stage `yaml:"stages"`
	} `yaml:"pipeline"`
	CircuitBreaker CircuitBreaker `yaml:"circuit_breaker"`
}

func Load(path string) (*Pipeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load pipeline: %w", err)
	}
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(f.Pipeline.Stages) == 0 {
		return nil, fmt.Errorf("%s: pipeline.stages 为空", path)
	}
	return &Pipeline{Stages: f.Pipeline.Stages, CircuitBreaker: f.CircuitBreaker}, nil
}

func (p *Pipeline) stage(id string) (int, *Stage) {
	for i := range p.Stages {
		if p.Stages[i].ID == id {
			return i, &p.Stages[i]
		}
	}
	return -1, nil
}

// First 返回首个阶段 id
func (p *Pipeline) First() string { return p.Stages[0].ID }

// IsLast 判断是否为末阶段
func (p *Pipeline) IsLast(id string) bool {
	return p.Stages[len(p.Stages)-1].ID == id
}

// Next 返回下一阶段 id（末阶段返回空串）
func (p *Pipeline) Next(id string) (string, error) {
	i, s := p.stage(id)
	if s == nil {
		return "", fmt.Errorf("未知阶段: %s", id)
	}
	if i == len(p.Stages)-1 {
		return "", nil
	}
	return p.Stages[i+1].ID, nil
}

// Approval 返回阶段审批方式（unknown → ""）
func (p *Pipeline) Approval(id string) string {
	_, s := p.stage(id)
	if s == nil {
		return ""
	}
	return s.Approval
}

// IsTeamReview 阶段终审走团队 MR（Web 审批无效，等合并 webhook 回传）
func (p *Pipeline) IsTeamReview(id string) bool { return p.Approval(id) == "team_mr_review" }

// NeedsApproval 该阶段完成是否需要审批（required 人工 / team_mr_review 团队 MR 终审）
func (p *Pipeline) NeedsApproval(id string) bool {
	a := p.Approval(id)
	return a == "required" || a == "team_mr_review"
}

// RejectTarget 驳回后的目标阶段（默认重做本阶段）
func (p *Pipeline) RejectTarget(id string) string {
	_, s := p.stage(id)
	if s != nil && s.OnReject == "back_to_coding" {
		return "coding"
	}
	return id
}
