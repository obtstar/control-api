package reconcile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Run 逐项执行对账。doc 相对 home/control-center，fact 相对 home；
// 无发现的检查项产出一条 PASS，有发现的逐条产出 WARN/CONFLICT
func Run(home string, checks *Checks) []Result {
	out := make([]Result, 0, len(checks.Checks))
	for _, ch := range checks.Checks {
		found := runOne(home, ch)
		if len(found) == 0 {
			desc := ch.Desc
			if desc == "" {
				desc = "声明与事实一致"
			}
			found = []Result{{ID: ch.ID, Severity: Pass, Message: desc, Basis: basis(ch.ID)}}
		}
		out = append(out, found...)
	}
	return out
}

func basis(id string) string {
	return "orchestration/reconcile/checks.yaml#" + id + "（方案 D-2：文档声明须与代码事实一致）"
}

func runOne(home string, ch Check) []Result {
	var out []Result
	mk := func(sev Severity, format string, a ...any) {
		out = append(out, Result{ID: ch.ID, Severity: sev, Message: fmt.Sprintf(format, a...), Basis: basis(ch.ID)})
	}
	if ch.Doc != "" {
		checkTextSide(mk, home, ch, true)
	}
	if ch.Fact != "" {
		checkTextSide(mk, home, ch, false)
		if len(ch.FactJSONDeps) > 0 {
			checkJSONDeps(mk, home, ch)
		}
	}
	if ch.RegistryFields != nil {
		checkRegistry(mk, home, ch)
	}
	return out
}

type emit func(sev Severity, format string, a ...any)

// checkTextSide 文本包含型校验；docSide=true 时读 home/control-center/<doc>
func checkTextSide(mk emit, home string, ch Check, docSide bool) {
	rel, must, mustNot := ch.Fact, ch.FactMust, ch.FactMustNot
	path := filepath.Join(home, ch.Fact)
	label, missWord := "事实来源", "事实来源缺失"
	if docSide {
		rel, must, mustNot = ch.Doc, ch.DocMust, ch.DocMustNot
		path = filepath.Join(home, "control-center", ch.Doc)
		label, missWord = "文档", "文档缺失"
	}
	if len(must) == 0 && len(mustNot) == 0 {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		mk(Warn, "%s: %s（%s）", label, missWord, rel)
		return
	}
	content := string(b)
	for _, m := range must {
		if !strings.Contains(content, m) {
			mk(Conflict, "%s %s 缺少必需表述 %q", label, rel, m)
		}
	}
	for _, m := range mustNot {
		if strings.Contains(content, m) {
			mk(Conflict, "%s %s 出现禁用表述 %q", label, rel, m)
		}
	}
}

// checkJSONDeps package.json 依赖键名校验（dependencies + devDependencies 并集）
func checkJSONDeps(mk emit, home string, ch Check) {
	b, err := os.ReadFile(filepath.Join(home, ch.Fact))
	if err != nil {
		return // 缺失已由文本侧 WARN 报告
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		mk(Warn, "事实来源: %s 非合法 JSON（%v）", ch.Fact, err)
		return
	}
	for _, dep := range ch.FactJSONDeps {
		_, inDeps := pkg.Dependencies[dep]
		_, inDev := pkg.DevDependencies[dep]
		if !inDeps && !inDev {
			mk(Conflict, "事实来源 %s 依赖键 %q 不存在", ch.Fact, dep)
		}
	}
}

// checkRegistry 注册表字段校验：未知字段 WARN（增量漂移），缺 required CONFLICT
func checkRegistry(mk emit, home string, ch Check) {
	b, err := os.ReadFile(filepath.Join(home, ch.Fact))
	if err != nil {
		mk(Warn, "事实来源: 注册表缺失（%s）", ch.Fact)
		return
	}
	var reg struct {
		Repos []map[string]any `yaml:"repos"`
	}
	if err := yaml.Unmarshal(b, &reg); err != nil {
		mk(Warn, "事实来源: 注册表非合法 yaml（%v）", err)
		return
	}
	allowed := map[string]bool{}
	for _, f := range ch.RegistryFields.Allowed {
		allowed[f] = true
	}
	for i, repo := range reg.Repos {
		name, _ := repo["repo_key"].(string)
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		for k := range repo {
			if !allowed[k] {
				mk(Warn, "注册表条目 %s 含 14.2 未声明字段 %q", name, k)
			}
		}
		for _, req := range ch.RegistryFields.Required {
			if _, ok := repo[req]; !ok {
				mk(Conflict, "注册表条目 %s 缺必需字段 %q", name, req)
			}
		}
	}
}
