package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadChecks(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "checks.yaml")
	writeFile(t, good, "checks:\n  - id: a\n    doc: d.md\n    doc_must: [\"x\"]\n")
	ch, err := LoadChecks(good)
	if err != nil {
		t.Fatalf("合法 checks.yaml 不应报错: %v", err)
	}
	if len(ch.Checks) != 1 || ch.Checks[0].ID != "a" || ch.Checks[0].DocMust[0] != "x" {
		t.Fatalf("解析结果不符: %+v", ch.Checks)
	}

	cases := map[string]string{
		"非法yaml": "checks: [unclosed",
		"缺id":    "checks:\n  - doc: d.md\n",
		"文件不存在":  "",
	}
	for name, body := range cases {
		p := filepath.Join(dir, "none.yaml")
		if body != "" {
			p = filepath.Join(dir, strings.ReplaceAll(name, "/", "_")+".yaml")
			writeFile(t, p, body)
		}
		if _, err := LoadChecks(p); err == nil {
			t.Fatalf("%s：应报错", name)
		}
	}
}

// 文本包含型对账：doc 相对 home/control-center，fact 相对 home
func TestRunTextChecks(t *testing.T) {
	newCheck := func() Check {
		return Check{
			ID: "backend-stack", Doc: "docs/01.md",
			DocMust: []string{"Go"}, DocMustNot: []string{"Spring Boot"},
			Fact:     "control-api/go.mod",
			FactMust: []string{"module control-api"}, FactMustNot: []string{"springframework"},
		}
	}
	cases := []struct {
		name    string
		doc     string // 空串 = 不写 doc 文件
		fact    string // 空串 = 不写 fact 文件
		wantSev Severity
		wantMsg string
	}{
		{"全部一致", "后端 Go 1.25", "module control-api", Pass, ""},
		{"doc缺必需表述", "后端 Java", "module control-api", Conflict, "缺少必需表述"},
		{"doc现禁用表述", "Go 与 Spring Boot 并存", "module control-api", Conflict, "禁用表述"},
		{"doc缺失", "", "module control-api", Warn, "文档缺失"},
		{"fact缺失", "Go", "", Warn, "事实来源缺失"},
		{"fact现禁用表述", "Go", "module control-api\nspringframework", Conflict, "事实来源"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.doc != "" {
				writeFile(t, filepath.Join(home, "control-center", "docs", "01.md"), tc.doc)
			}
			if tc.fact != "" {
				writeFile(t, filepath.Join(home, "control-api", "go.mod"), tc.fact)
			}
			got := Run(home, &Checks{Checks: []Check{newCheck()}})
			if len(got) != 1 {
				t.Fatalf("应恰 1 条结论，得 %d: %+v", len(got), got)
			}
			if got[0].Severity != tc.wantSev {
				t.Fatalf("级别: want %s got %s (%s)", tc.wantSev, got[0].Severity, got[0].Message)
			}
			if tc.wantMsg != "" && !strings.Contains(got[0].Message, tc.wantMsg) {
				t.Fatalf("结论不含 %q: %s", tc.wantMsg, got[0].Message)
			}
			if got[0].Basis == "" {
				t.Fatal("结论必须携带依据出处")
			}
		})
	}
}

// package.json 依赖键名对账（fact_json_deps）
func TestRunJSONDeps(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "control-center", "docs", "06.md"), "React PrimeReact Vite")
	writeFile(t, filepath.Join(home, "control-web", "package.json"),
		`{"dependencies":{"react":"18","primereact":"10"},"devDependencies":{"vite":"5"}}`)
	ch := Check{ID: "frontend-stack", Doc: "docs/06.md",
		DocMust: []string{"React"}, Fact: "control-web/package.json",
		FactJSONDeps: []string{"react", "primereact", "vite"}}
	if got := Run(home, &Checks{Checks: []Check{ch}}); got[0].Severity != Pass {
		t.Fatalf("依赖齐全应 PASS: %+v", got[0])
	}

	ch.FactJSONDeps = append(ch.FactJSONDeps, "next")
	got := Run(home, &Checks{Checks: []Check{ch}})
	if got[0].Severity != Conflict || !strings.Contains(got[0].Message, "next") {
		t.Fatalf("缺依赖应 CONFLICT 并点名: %+v", got[0])
	}
}

// 注册表字段对账（registry_fields）
func TestRunRegistry(t *testing.T) {
	ch := Check{ID: "registry-schema", Fact: "control-center/registry/repos.yaml",
		RegistryFields: &RegistryFields{
			Required: []string{"repo_key", "git_url"},
			Allowed:  []string{"repo_key", "git_url", "name"},
		}}
	cases := []struct {
		name    string
		repos   string
		wantSev Severity
		wantMsg string
	}{
		{"合规", "repos:\n  - repo_key: a\n    git_url: u\n    name: n\n", Pass, ""},
		{"未声明字段", "repos:\n  - repo_key: a\n    git_url: u\n    path: p\n", Warn, "未声明字段"},
		{"缺必需字段", "repos:\n  - repo_key: a\n", Conflict, "缺必需字段"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeFile(t, filepath.Join(home, "control-center", "registry", "repos.yaml"), tc.repos)
			got := Run(home, &Checks{Checks: []Check{ch}})
			if got[0].Severity != tc.wantSev {
				t.Fatalf("级别: want %s got %s (%s)", tc.wantSev, got[0].Severity, got[0].Message)
			}
			if tc.wantMsg != "" && !strings.Contains(got[0].Message, tc.wantMsg) {
				t.Fatalf("结论不含 %q: %s", tc.wantMsg, got[0].Message)
			}
		})
	}
}

func TestHasConflict(t *testing.T) {
	if HasConflict([]Result{{Severity: Pass}, {Severity: Warn}}) {
		t.Fatal("PASS/WARN 不算冲突")
	}
	if !HasConflict([]Result{{Severity: Conflict}}) {
		t.Fatal("CONFLICT 应检出")
	}
}

// KB 镜像新鲜度对账（FINDING-051）：镜像首行须携 kb-mirror 出处头且
// sha256 与上游内容一致；缺失/无头/过期均 WARN（派生物可再生成，故不 CONFLICT）
func TestRunMirrorFresh(t *testing.T) {
	sum := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	ptr := func(s string) *string { return &s }
	cases := []struct {
		name     string
		upstream string
		mirror   *string // nil = 镜像文件不存在
		wantSev  Severity
	}{
		{"新鲜一致", "正文", ptr("<!-- kb-mirror: upstream=up.md sha256=" + sum("正文") + " -->\nsynced-at: x -->\n\n正文"), Pass},
		{"镜像过期", "新正文", ptr("<!-- kb-mirror: upstream=up.md sha256=" + sum("旧正文") + " -->\n旧正文"), Warn},
		{"镜像缺失", "正文", nil, Warn},
		{"缺出处头", "正文", ptr("正文"), Warn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeFile(t, filepath.Join(home, "up.md"), tc.upstream)
			if tc.mirror != nil {
				writeFile(t, filepath.Join(home, "mirror.md"), *tc.mirror)
			}
			ch := &Checks{Checks: []Check{{ID: "kb-mirror", Desc: "镜像新鲜",
				MirrorPairs: []MirrorPair{{Upstream: "up.md", Mirror: "mirror.md"}}}}}
			res := Run(home, ch)
			if len(res) != 1 || res[0].Severity != tc.wantSev {
				t.Fatalf("应单条 %s: %+v", tc.wantSev, res)
			}
		})
	}
}
