package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResolveModel(t *testing.T) {
	cfg := AgentConfig{Models: map[string]string{
		"cheap":  "kimi-for-coding-highspeed",
		"coding": "kimi-for-coding",
		"heavy":  "k3",
	}}
	tests := []struct {
		alias string
		want  string
	}{
		{"cheap", "kimi-for-coding-highspeed"},
		{"coding", "kimi-for-coding"},
		{"heavy", "k3"},
		{"unknown", "unknown"}, // 未注册别名原样透传
	}
	for _, tc := range tests {
		if got := cfg.ResolveModel(tc.alias); got != tc.want {
			t.Errorf("ResolveModel(%q) = %q, want %q", tc.alias, got, tc.want)
		}
	}
}

func TestResolveModelEmptyMap(t *testing.T) {
	if got := (AgentConfig{}).ResolveModel("coding"); got != "coding" {
		t.Errorf("空映射应透传别名，got %q", got)
	}
}

// KB 段默认值：mode 必须为 off（KB 当前为空，enforce 会暂停全部任务，FINDING-017）
func TestKBDefaultsConservative(t *testing.T) {
	cfg := defaults()
	if cfg.KB.Mode != "off" {
		t.Errorf("KB.Mode = %q, want off", cfg.KB.Mode)
	}
	if cfg.KB.Endpoint != "http://127.0.0.1:8766" {
		t.Errorf("KB.Endpoint = %q", cfg.KB.Endpoint)
	}
}

// CONTROL_KB_API_KEY env 覆盖 kb.api_key
func TestKBAPIKeyEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONTROL_CONFIG", dir+"/control-api.yaml")
	t.Setenv("CONTROL_KB_API_KEY", "envkey")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KB.APIKey != "envkey" {
		t.Errorf("KB.APIKey = %q, want envkey", cfg.KB.APIKey)
	}
}

// Save 不得把 env 注入的密钥落盘（FINDING-010 密钥不落盘）：
// server.api_key 与 llm.api_key 序列化前强制写空，其余字段无损
func TestSaveStripsAPIKeys(t *testing.T) {
	dir := t.TempDir()
	cfg := defaults()
	cfg.Path = filepath.Join(dir, "control-api.yaml")
	cfg.Server.APIKey = "server-secret"
	cfg.LLM.APIKey = "llm-secret"
	cfg.Server.Port = 9999
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "server-secret") || strings.Contains(string(data), "llm-secret") {
		t.Fatalf("密钥落盘:\n%s", data)
	}
	// 读回校验：两处 api_key 为空，其他字段无损
	back := &Config{}
	if err := yaml.Unmarshal(data, back); err != nil {
		t.Fatal(err)
	}
	if back.Server.APIKey != "" || back.LLM.APIKey != "" {
		t.Errorf("api_key 应为空: server=%q llm=%q", back.Server.APIKey, back.LLM.APIKey)
	}
	if back.Server.Port != 9999 {
		t.Errorf("Server.Port = %d, want 9999（其他字段不得受损）", back.Server.Port)
	}
	if back.LLM.Endpoint != cfg.LLM.Endpoint {
		t.Errorf("LLM.Endpoint = %q, want %q", back.LLM.Endpoint, cfg.LLM.Endpoint)
	}
}
