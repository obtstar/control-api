// Package reconcile 对账 loop（方案 D-2）：文档声明 vs 代码/配置事实。
// 检查项声明在 control-center orchestration/reconcile/checks.yaml；
// 结论携带依据出处，CONFLICT 由人/agent 按 18.5 登记 FINDINGS（本包不写台账）。
package reconcile

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Severity 对账结论级别：PASS 一致 / WARN 提示（缺失、增量漂移）/ CONFLICT 矛盾
type Severity string

const (
	Pass     Severity = "PASS"
	Warn     Severity = "WARN"
	Conflict Severity = "CONFLICT"
)

// Result 单项对账结论；Basis 指向检查项声明出处（机制携带理由）
type Result struct {
	ID       string
	Severity Severity
	Message  string
	Basis    string
}

// RegistryFields 注册表字段校验声明（registry_fields 型检查）
type RegistryFields struct {
	Required []string `yaml:"required"`
	Allowed  []string `yaml:"allowed"`
}

// MirrorPair KB 镜像对照（mirror_pairs 型检查，FINDING-051）：
// upstream/mirror 均相对 home；mirror 首行须携 kb-mirror 出处头（sha256=上游内容哈希）
type MirrorPair struct {
	Upstream string `yaml:"upstream"`
	Mirror   string `yaml:"mirror"`
}

// Check 单项对账声明：doc_* 为文档侧（相对 control-center 根），fact_* 为事实侧（相对 home）
type Check struct {
	ID             string          `yaml:"id"`
	Desc           string          `yaml:"desc"`
	Doc            string          `yaml:"doc"`
	DocMust        []string        `yaml:"doc_must"`
	DocMustNot     []string        `yaml:"doc_must_not"`
	Fact           string          `yaml:"fact"`
	FactMust       []string        `yaml:"fact_must"`
	FactMustNot    []string        `yaml:"fact_must_not"`
	FactJSONDeps   []string        `yaml:"fact_json_deps"`
	RegistryFields *RegistryFields `yaml:"registry_fields"`
	MirrorPairs    []MirrorPair    `yaml:"mirror_pairs"`
}

// Checks checks.yaml 顶层结构
type Checks struct {
	Checks []Check `yaml:"checks"`
}

// LoadChecks 加载并校验 checks.yaml；文件缺失/非法 yaml/条目缺 id 均报错
func LoadChecks(path string) (*Checks, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取检查项声明: %w", err)
	}
	var c Checks
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("解析检查项声明 %s: %w", path, err)
	}
	for i, ch := range c.Checks {
		if ch.ID == "" {
			return nil, fmt.Errorf("检查项声明第 %d 项缺 id", i+1)
		}
	}
	return &c, nil
}

// HasConflict 结论集中是否存在 CONFLICT（子命令据此置退出码 1）
func HasConflict(rs []Result) bool {
	for _, r := range rs {
		if r.Severity == Conflict {
			return true
		}
	}
	return false
}
