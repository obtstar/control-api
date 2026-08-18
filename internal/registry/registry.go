// Package registry 仓库注册表（14.2）：control-api 启动时读取 repos.yaml，
// 运行时缓存于内存、不落库；任务创建时校验 repo_key 已登记（FINDING-019/046：
// 此前权柄文档声称的运行时行为不存在，任务可指向未登记仓库）。
package registry

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Repo 一条仓库登记。仅取运行时消费所需字段子集，未列出字段（api_type/
// token_ref/max_worktrees 等调度字段）解析时忽略，向前兼容 14.2 后续扩展。
type Repo struct {
	RepoKey         string `yaml:"repo_key"`
	Name            string `yaml:"name"`
	Path            string `yaml:"path"`
	GitURL          string `yaml:"git_url"`
	DefaultBranch   string `yaml:"default_branch"`
	ExecutorAllowed bool   `yaml:"executor_allowed"`
	Disabled        bool   `yaml:"disabled"`
}

type file struct {
	Repos []Repo `yaml:"repos"`
}

// Registry 注册表内存缓存（14.2：缓存于内存，不落库；热加载为规划中，暂重启生效）
type Registry struct {
	byKey map[string]Repo
}

// Load 读取并解析 repos.yaml。注册表为平台关键配置：文件缺失/解析失败/
// 条目缺 repo_key 均返回错误，由启动方 fail-fast（不降级为空表静默放行）。
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取注册表 %s: %w", path, err)
	}
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("解析注册表 %s: %w", path, err)
	}
	r := &Registry{byKey: make(map[string]Repo, len(f.Repos))}
	for _, repo := range f.Repos {
		if repo.RepoKey == "" {
			return nil, fmt.Errorf("注册表 %s 含缺 repo_key 的条目", path)
		}
		r.byKey[repo.RepoKey] = repo
	}
	return r, nil
}

// Registered repo_key 已登记且未禁用。disabled 的退役仓视为未登记，
// 防止新任务再指向它；executor_allowed 只管 worktree 调度，不影响存在性判定。
func (r *Registry) Registered(key string) bool {
	repo, ok := r.byKey[key]
	return ok && !repo.Disabled
}
