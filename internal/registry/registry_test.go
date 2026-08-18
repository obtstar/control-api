// registry.Load 与 repo_key 校验测试（FINDING-019/046：
// 14.2 声明"control-api 启动时读取 repos.yaml"，此前无任何注册表消费代码）。
// 临时文件实测，不 mock 文件系统。
package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// 写临时 repos.yaml 夹具
func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validYAML = `repos:
  - repo_key: control-api
    name: 平台后端
    path: control-api
    git_url: git@github.com:obtstar/control-api.git
    default_branch: dev
    executor_allowed: false
  - repo_key: billing-core
    path: wt/billing-core/dev
    git_url: ssh://git@git.internal/billing/billing-core.git
    executor_allowed: true
    disabled: false
  - repo_key: legacy-sys
    path: wt/legacy-sys/dev
    git_url: ssh://git@git.internal/legacy/legacy-sys.git
    disabled: true
`

func TestLoadLookup(t *testing.T) {
	r, err := Load(writeFixture(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		key  string
		want bool
	}{
		{"control-api", true},   // 平台仓已登记（executor_allowed=false 不影响存在性）
		{"billing-core", true},  // 业务仓已登记
		{"legacy-sys", false},   // disabled 视为未登记（退役仓不再接新任务）
		{"no-such-repo", false}, // 未登记
		{"", false},             // 空键
		{"CONTROL-API", false},  // 大小写敏感，不模糊匹配
	}
	for _, c := range cases {
		if got := r.Registered(c.key); got != c.want {
			t.Errorf("Registered(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "none.yaml")); err == nil {
		t.Fatal("注册表文件缺失应报错（启动失败，14.2 启动时读取）")
	}
}

func TestLoadBadYAML(t *testing.T) {
	if _, err := Load(writeFixture(t, "repos: [{{")); err == nil {
		t.Fatal("非法 YAML 应报错")
	}
}

func TestLoadEmptyRepoKey(t *testing.T) {
	if _, err := Load(writeFixture(t, "repos:\n  - name: 缺键条目\n")); err == nil {
		t.Fatal("缺 repo_key 的条目应报错")
	}
}
