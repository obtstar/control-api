// Package config 加载 config.yaml（环境变量覆盖），0600 权限保存。
// 模板来源：control-piekbs internal/config
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// APIKey 运行时无消费方，仅占位/env 覆盖入口（CONTROL_API_KEY）；
	// 密钥只经 env 注入，Save 强制写空不落盘（FINDING-010）
	APIKey string `yaml:"api_key"`
	// WebhookSecret merge webhook 共享密钥（X-Webhook-Token 头比对）；
	// 空 = webhook 端点未启用（一律 503）。env CONTROL_WEBHOOK_SECRET 优先
	WebhookSecret string `yaml:"webhook_secret"`
}

type DBConfig struct {
	Path string `yaml:"path"` // SQLite 文件（默认 ~/data/control.db）
}

// KBConfig KB grounding（18.3 依据检索，FINDING-016）。
// Mode 默认 off：当前 KB 为空（FINDING-017），enforce 会暂停所有任务，故保守默认。
// C1 执行层迁移（TASK-004）后 grounded 检查点位于 engine.Advance 入口。
type KBConfig struct {
	Endpoint string `yaml:"endpoint"` // PieKBS REST（默认 http://127.0.0.1:8766）
	APIKey   string `yaml:"api_key"`  // env CONTROL_KB_API_KEY 优先
	Mode     string `yaml:"mode"`     // off | warn | enforce（默认 off）
}

type PathsConfig struct {
	Home     string `yaml:"home"`      // 工作用户 home（默认 /home/dev）
	GitDir   string `yaml:"repos_dir"` // ~/.repos
	WtRoot   string `yaml:"wt_root"`   // ~/wt
	TasksDir string `yaml:"tasks_dir"` // tasks/（任务即文档）
	WikiDir  string `yaml:"wiki_dir"`  // ~/control-wiki
	// ContractPath/FindingsPath 契约与问题台账文件位置（FINDING-040：
	// 搬仓时配置覆盖，不再硬编码 home 下固定相对路径）
	ContractPath string `yaml:"contract_path"`
	FindingsPath string `yaml:"findings_path"`
	// RegistryPath 仓库注册表 repos.yaml（14.2：启动时读取，内存缓存；
	// FINDING-019/046：任务创建校验 repo_key 已登记）
	RegistryPath string `yaml:"registry_path"`
}

type Config struct {
	Path   string       `yaml:"-"`
	Server ServerConfig `yaml:"server"`
	DB     DBConfig     `yaml:"db"`
	Paths  PathsConfig  `yaml:"paths"`
	KB     KBConfig     `yaml:"kb"`
}

func home() string {
	if h := os.Getenv("CONTROL_HOME"); h != "" {
		return filepath.Dir(h) // CONTROL_HOME 指向 control-center 仓，取其父级
	}
	h, _ := os.UserHomeDir()
	return h
}

func defaults() *Config {
	h := home()
	return &Config{
		Server: ServerConfig{Host: "127.0.0.1", Port: 8765},
		DB:     DBConfig{Path: filepath.Join(h, "data", "control.db")},
		KB:     KBConfig{Endpoint: "http://127.0.0.1:8766", Mode: "off"},
		Paths: PathsConfig{
			Home:         h,
			GitDir:       filepath.Join(h, ".repos"),
			WtRoot:       filepath.Join(h, "wt"),
			TasksDir:     filepath.Join(h, "control-center", "tasks"),
			WikiDir:      filepath.Join(h, "control-wiki"),
			ContractPath: filepath.Join(h, "control-api", "docs", "api", "openapi.yaml"),
			FindingsPath: filepath.Join(h, "control-center", "docs", "FINDINGS.md"),
			RegistryPath: filepath.Join(h, "control-center", "registry", "repos.yaml"),
		},
	}
}

// Load 读取 ~/control-api.yaml（CONTROL_CONFIG 覆盖路径），env 覆盖字段
func Load() (*Config, error) {
	cfg := defaults()
	path := os.Getenv("CONTROL_CONFIG")
	if path == "" {
		path = filepath.Join(cfg.Paths.Home, "control-api.yaml")
	}
	cfg.Path = path

	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析 %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		// 首次运行：写入默认配置（0600）
		if err := cfg.Save(); err != nil {
			return nil, err
		}
	}

	// env 覆盖（密钥只走 env）
	if v := os.Getenv("CONTROL_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("CONTROL_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.Server.Port)
	}
	if v := os.Getenv("CONTROL_API_KEY"); v != "" {
		cfg.Server.APIKey = v
	}
	if v := os.Getenv("CONTROL_WEBHOOK_SECRET"); v != "" {
		cfg.Server.WebhookSecret = v
	}
	if v := os.Getenv("CONTROL_KB_API_KEY"); v != "" {
		cfg.KB.APIKey = v
	}
	return cfg, nil
}

// Save 0600 落盘。密钥只经 env 注入：序列化前强制清空 server.api_key、
// kb.api_key、server.webhook_secret，防止 env 覆盖进内存的
// 密钥被回写落盘（FINDING-010；kb/webhook 两项 FINDING-038 补齐）。
func (c *Config) Save() error {
	safe := *c
	safe.Server.APIKey = ""
	safe.KB.APIKey = ""
	safe.Server.WebhookSecret = ""
	data, err := yaml.Marshal(&safe)
	if err != nil {
		return err
	}
	return os.WriteFile(c.Path, data, 0o600)
}
