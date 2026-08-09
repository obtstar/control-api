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
	Approval string `yaml:"approval"`  // required | auto | gitlab/team_mr_review
	OnReject string `yaml:"on_reject"` // retry | back_to_coding
}

type Pipeline struct {
	Stages []Stage `yaml:"-"`
}

type file struct {
	Pipeline struct {
		Stages []Stage `yaml:"stages"`
	} `yaml:"pipeline"`
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
	return &Pipeline{Stages: f.Pipeline.Stages}, nil
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

// NeedsApproval 该阶段完成是否需要人工审批（auto 直接过闸）
func (p *Pipeline) NeedsApproval(id string) bool {
	_, s := p.stage(id)
	return s != nil && s.Approval == "required"
}

// RejectTarget 驳回后的目标阶段（默认重做本阶段）
func (p *Pipeline) RejectTarget(id string) string {
	_, s := p.stage(id)
	if s != nil && s.OnReject == "back_to_coding" {
		return "coding"
	}
	return id
}
