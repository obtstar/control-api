// control-api — Agent 平台编排后端（团队项目中的个人 AI 助手）
// 结构模板参照 control-piekbs：cmd 入口 + internal 域内件
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"control-api/internal/api"
	"control-api/internal/authn"
	"control-api/internal/config"
	"control-api/internal/pipeline"
	"control-api/internal/reconcile"
	"control-api/internal/service"
	"control-api/internal/store"
)

var version = "0.1.0-dev"

func usage() {
	fmt.Println(`control-api — Agent 平台编排后端

用法: control-api <命令> [--config PATH]
  serve      启动 HTTP 服务（默认 127.0.0.1:8765）
  check      自检（配置/SQLite/目录/网络）
  verify-log 校验 work_log hash 链（只读，FINDING-005）
  reconcile  对账 loop：文档声明 vs 代码事实（方案 D-2，checks.yaml 驱动；有冲突退出码 1）
  service    服务管理: install|uninstall|status（systemd 用户级）
  version    打印版本

环境变量: CONTROL_CONFIG / CONTROL_HOME（默认 ~/control-center）`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cfg, err := config.Load()
	must(err)

	switch os.Args[1] {
	case "serve":
		must(api.Serve(cfg))
	case "check":
		must(runCheck(cfg))
	case "verify-log":
		must(verifyLog(cfg))
	case "reconcile":
		must(runReconcile(cfg))
	case "service":
		if len(os.Args) < 3 {
			usage()
			os.Exit(1)
		}
		must(service.Run(cfg, os.Args[2]))
	case "user":
		// control-api user add <username> <role>（密码从 stdin 安全读取）
		if len(os.Args) < 4 || os.Args[2] != "add" {
			fmt.Fprintln(os.Stderr, "用法: control-api user add <username> <role>")
			os.Exit(1)
		}
		must(addUser(cfg, os.Args[3], os.Args[4]))
	case "version", "-v", "--version":
		fmt.Println("control-api", version)
	case "-h", "--help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "未知命令:", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runCheck(cfg *config.Config) error {
	fmt.Println("config:", cfg.Path)
	fmt.Println("db:", cfg.DB.Path)
	st, err := store.Open(cfg.DB.Path)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Ping(); err != nil {
		return err
	}
	// FINDING-035：check 同步加载 pipeline，merge 权力校验不再只在 serve 路径生效
	plPath := filepath.Join(cfg.Paths.Home,
		"control-center", "orchestration", "workflows", "pipeline.yaml")
	if _, err := pipeline.Load(plPath); err != nil {
		return fmt.Errorf("pipeline 加载失败（%s）: %w", plPath, err)
	}
	fmt.Println("pipeline:", plPath)
	return nil
}

// verifyLog 校验 work_log hash 链（FINDING-005），只读；失败返回错误由 must 置退出码 1
func verifyLog(cfg *config.Config) error {
	st, err := store.Open(cfg.DB.Path)
	if err != nil {
		return err
	}
	defer st.Close()
	res, err := st.VerifyLog()
	if err != nil {
		return err
	}
	if res.GenesisPrev != "" {
		fmt.Println("创世行 prev_hash（外部归档续接，不判错）:", res.GenesisPrev)
	}
	fmt.Printf("链完整（%d 条）\n", res.Total)
	return nil
}

// runReconcile 执行对账 loop（方案 D-2）：checks.yaml 声明的文档↔事实逐项校验。
// 结果逐行输出并带依据出处；存在 CONFLICT 时返回错误置退出码 1，由人/agent 按 18.5 登记
func runReconcile(cfg *config.Config) error {
	ckPath := filepath.Join(cfg.Paths.Home,
		"control-center", "orchestration", "reconcile", "checks.yaml")
	checks, err := reconcile.LoadChecks(ckPath)
	if err != nil {
		return err
	}
	results := reconcile.Run(cfg.Paths.Home, checks)
	for _, r := range results {
		fmt.Printf("%s %s: %s\n", r.Severity, r.ID, r.Message)
		if r.Severity != reconcile.Pass {
			fmt.Printf("  依据: %s\n", r.Basis)
		}
	}
	if n := countConflicts(results); n > 0 {
		return fmt.Errorf("对账发现 %d 项冲突（按 docs/architecture/18-authority.md 18.5 登记 FINDINGS）", n)
	}
	return nil
}

func countConflicts(rs []reconcile.Result) int {
	n := 0
	for _, r := range rs {
		if r.Severity == reconcile.Conflict {
			n++
		}
	}
	return n
}

func addUser(cfg *config.Config, username, role string) error {
	st, err := store.Open(cfg.DB.Path)
	if err != nil {
		return err
	}
	defer st.Close()
	fmt.Print("密码（≥8 位）: ")
	var pw string
	fmt.Scanln(&pw)
	a := &authn.Auth{St: st}
	if err := a.CreateUser(username, pw, role); err != nil {
		return err
	}
	fmt.Printf("用户已创建: %s（%s）\n", username, role)
	return nil
}
