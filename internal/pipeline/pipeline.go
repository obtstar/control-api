// Package pipeline 加载 orchestration/workflows/*.yaml，提供状态机流转。
// 规则：每阶段一审批闸（18 章）；approval: required|auto|team_mr_review；
// on_reject: retry（重做本阶段）| back_to_coding（打回编码）
// 热加载（FINDING-008）：Load 后记录文件 mtime+size，每次访问前 stat 比对，
// 变化则重新解析（含权力校验）；解析失败沿用旧配置并记日志，不打爆运行中的服务。
package pipeline

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

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
	// RetryBackoffSeconds 连败自动重试退避表（秒，FINDING-027）：
	// 第 n 次失败后等待 list[min(n-1, len-1)] 秒重跑；空表按 defaultRetryBackoff
	RetryBackoffSeconds []int `yaml:"retry_backoff_seconds"`
}

// defaultFailureThreshold CircuitBreaker 缺省连败阈值
const defaultFailureThreshold = 3

// defaultRetryBackoff 缺省退避表：第 1 次 30s、第 2 次起 60s
var defaultRetryBackoff = []int{30, 60}

// FailureThreshold 连败熔断阈值（缺省/为 0 时默认 3）
func (p *Pipeline) FailureThreshold() int {
	_, cb := p.snapshot()
	if cb.ConsecutiveFailures <= 0 {
		return defaultFailureThreshold
	}
	return cb.ConsecutiveFailures
}

// RetryBackoff 第 failures 次失败后的重试等待时长（failures>=1）
func (p *Pipeline) RetryBackoff(failures int) time.Duration {
	_, cb := p.snapshot()
	list := cb.RetryBackoffSeconds
	if len(list) == 0 {
		list = defaultRetryBackoff
	}
	i := failures - 1
	if i < 0 {
		i = 0
	}
	if i >= len(list) {
		i = len(list) - 1
	}
	if list[i] <= 0 {
		list[i] = 30
	}
	return time.Duration(list[i]) * time.Second
}

// Model 返回阶段模型别名（默认 coding）
func (p *Pipeline) Model(id string) string {
	stages, _ := p.snapshot()
	_, s := stageOf(stages, id)
	if s == nil || s.Model == "" {
		return "coding"
	}
	return s.Model
}

type Pipeline struct {
	Stages         []Stage        `yaml:"-"`
	CircuitBreaker CircuitBreaker `yaml:"-"`
	// 热加载状态（Load 填充；手工构造的静态实例 path 为空 = 不热加载）
	mu    sync.RWMutex
	path  string
	mtime time.Time
	size  int64
}

type file struct {
	Pipeline struct {
		Stages []Stage `yaml:"stages"`
	} `yaml:"pipeline"`
	CircuitBreaker CircuitBreaker `yaml:"circuit_breaker"`
}

func Load(path string) (*Pipeline, error) {
	p, err := parseFile(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil {
		p.path, p.mtime, p.size = path, info.ModTime(), info.Size()
	}
	return p, nil
}

func parseFile(path string) (*Pipeline, error) {
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
	if err := validateStages(f.Pipeline.Stages); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Pipeline{Stages: f.Pipeline.Stages, CircuitBreaker: f.CircuitBreaker}, nil
}

// refresh 热加载检查：源文件 mtime/size 变化则重新解析；失败沿用旧配置（FINDING-008）。
// 失败也推进 mtime，避免每次访问都重读坏文件刷日志；修复文件后 mtime 再变即恢复。
func (p *Pipeline) refresh() {
	if p.path == "" {
		return // 手工构造的静态实例（测试）
	}
	info, err := os.Stat(p.path)
	if err != nil {
		return // stat 失败无法判断变化，沿用旧配置
	}
	p.mu.RLock()
	stale := !info.ModTime().Equal(p.mtime) || info.Size() != p.size
	p.mu.RUnlock()
	if !stale {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if info.ModTime().Equal(p.mtime) && info.Size() == p.size {
		return // 并发下已被其他 goroutine 刷新
	}
	np, err := parseFile(p.path)
	if err != nil {
		log.Printf("[pipeline] 热加载 %s 失败，沿用旧配置: %v", p.path, err)
	} else {
		p.Stages, p.CircuitBreaker = np.Stages, np.CircuitBreaker
		log.Printf("[pipeline] 热加载 %s：%d 阶段", p.path, len(np.Stages))
	}
	p.mtime, p.size = info.ModTime(), info.Size()
}

// snapshot 热加载检查后读当前配置（所有访问器统一入口）
func (p *Pipeline) snapshot() ([]Stage, CircuitBreaker) {
	p.refresh()
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Stages, p.CircuitBreaker
}

// validateStages 权力模型加载校验：merge 阶段为终审闸，审批不得为 auto
// （依据：control-center/docs/architecture/00-principles.md 每阶段一道审批闸 +
// 18-authority.md merge 终审归团队 MR 评审，平台无权自动放行）。
func validateStages(stages []Stage) error {
	for _, s := range stages {
		if s.ID == "merge" && s.Approval != "required" && s.Approval != "team_mr_review" {
			return fmt.Errorf("merge 阶段 approval=%q 非法：终审不得为 auto，须为 required 或 team_mr_review"+
				"（依据 control-center/docs/architecture/00-principles.md 审批闸原则 + 18-authority.md merge 终审归团队）",
				s.Approval)
		}
	}
	return nil
}

func stageOf(stages []Stage, id string) (int, *Stage) {
	for i := range stages {
		if stages[i].ID == id {
			return i, &stages[i]
		}
	}
	return -1, nil
}

// First 返回首个阶段 id
func (p *Pipeline) First() string {
	stages, _ := p.snapshot()
	return stages[0].ID
}

// IsLast 判断是否为末阶段
func (p *Pipeline) IsLast(id string) bool {
	stages, _ := p.snapshot()
	return stages[len(stages)-1].ID == id
}

// Next 返回下一阶段 id（末阶段返回空串）
func (p *Pipeline) Next(id string) (string, error) {
	stages, _ := p.snapshot()
	i, s := stageOf(stages, id)
	if s == nil {
		return "", fmt.Errorf("未知阶段: %s", id)
	}
	if i == len(stages)-1 {
		return "", nil
	}
	return stages[i+1].ID, nil
}

// Approval 返回阶段审批方式（unknown → ""）
func (p *Pipeline) Approval(id string) string {
	stages, _ := p.snapshot()
	_, s := stageOf(stages, id)
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
	stages, _ := p.snapshot()
	_, s := stageOf(stages, id)
	if s != nil && s.OnReject == "back_to_coding" {
		return "coding"
	}
	return id
}
