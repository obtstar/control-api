// Package config 加载 config.yaml（环境变量覆盖），0600 权限保存。
// 模板来源：control-piekbs internal/config
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

type LLMConfig struct {
	Endpoint string `yaml:"endpoint"` // LiteLLM 网关
	// APIKey 运行时无消费方，仅占位/env 覆盖入口（LITELLM_API_KEY）；
	// 密钥只经 env 注入，Save 强制写空不落盘（FINDING-010）
	APIKey string `yaml:"api_key"`
}

// KBConfig KB grounding（18.3 依据检索，FINDING-016）。
// Mode 默认 off：当前 KB 为空（FINDING-017），enforce 会暂停所有任务，故保守默认。
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
}

type Config struct {
	Path   string       `yaml:"-"`
	Server ServerConfig `yaml:"server"`
	DB     DBConfig     `yaml:"db"`
	LLM    LLMConfig    `yaml:"llm"`
	Paths  PathsConfig  `yaml:"paths"`
	Agent  AgentConfig  `yaml:"agent"`
	KB     KBConfig     `yaml:"kb"`
}

type AgentConfig struct {
	// Bin pi 可执行文件名（默认 pi，PATH 查找）
	Bin string `yaml:"bin"`
	// Models 流水线模型别名 → pi 模型模式（provider/id 或模糊模式）
	// 别名见 pipeline.yaml 各阶段 model 字段：cheap/coding/heavy
	Models map[string]string `yaml:"models"`
	// SkillsDir 编排 skill 根目录（orchestration/skills），为空则不加载 skill
	SkillsDir string `yaml:"skills_dir"`
	// ScriptsDir 平台脚本目录（control-center/scripts，branch.sh 等），注入 pi 的 PATH
	ScriptsDir string `yaml:"scripts_dir"`
	// Command 可选：测试用命令模板覆盖（fake-pi），占位符：
	// {{.Prompt}} {{.TaskID}} {{.Stage}} {{.Model}} {{.WorkDir}}
	// 设置后忽略 Bin/Models/SkillsDir，直接按模板执行
	Command    string `yaml:"command"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

// ResolveModel 模型别名 → pi 模型模式；未注册别名原样透传
func (a AgentConfig) ResolveModel(alias string) string {
	if m, ok := a.Models[alias]; ok {
		return m
	}
	return alias
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
		LLM:    LLMConfig{Endpoint: "http://litellm.internal:4000"},
		KB:     KBConfig{Endpoint: "http://127.0.0.1:8766", Mode: "off"},
		Agent: AgentConfig{
			Bin:        "pi",
			Models:     map[string]string{"cheap": "kimi-for-coding-highspeed", "coding": "kimi-for-coding", "heavy": "k3"},
			SkillsDir:  filepath.Join(h, "control-center", "orchestration", "skills"),
			ScriptsDir: filepath.Join(h, "control-center", "scripts"),
			TimeoutSec: 1800,
		},
		Paths: PathsConfig{
			Home:     h,
			GitDir:   filepath.Join(h, ".repos"),
			WtRoot:   filepath.Join(h, "wt"),
			TasksDir: filepath.Join(h, "control-center", "tasks"),
			WikiDir:  filepath.Join(h, "control-wiki"),
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
	if v := os.Getenv("LITELLM_ENDPOINT"); v != "" {
		cfg.LLM.Endpoint = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("LITELLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("CONTROL_KB_API_KEY"); v != "" {
		cfg.KB.APIKey = v
	}
	return cfg, nil
}

// Save 0600 落盘。密钥只经 env 注入：序列化前强制清空 server.api_key 与
// llm.api_key，防止 env 覆盖进内存的密钥被回写落盘（FINDING-010）。
func (c *Config) Save() error {
	safe := *c
	safe.Server.APIKey = ""
	safe.LLM.APIKey = ""
	data, err := yaml.Marshal(&safe)
	if err != nil {
		return err
	}
	return os.WriteFile(c.Path, data, 0o600)
}
