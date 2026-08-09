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
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
	APIKey string `yaml:"api_key"`
}

type DBConfig struct {
	Path string `yaml:"path"` // SQLite 文件（默认 ~/data/control.db）
}

type LLMConfig struct {
	Endpoint string `yaml:"endpoint"` // LiteLLM 网关
	APIKey   string `yaml:"api_key"`  // 经 env 注入，不落盘
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
}

type AgentConfig struct {
	// pi 调用模板，占位符：{{.Prompt}} {{.TaskID}} {{.Stage}} {{.WorkDir}}
	// 默认 print 模式；RPC 模式协议在 control-pi 实测后调整
	Command    string `yaml:"command"`
	TimeoutSec int    `yaml:"timeout_sec"`
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
		Agent: AgentConfig{
			Command:    "pi -m {{.Model}} -p {{.Prompt}}",
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
	if v := os.Getenv("LITELLM_ENDPOINT"); v != "" {
		cfg.LLM.Endpoint = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("LITELLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	return cfg, nil
}

func (c *Config) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(c.Path, data, 0o600)
}
