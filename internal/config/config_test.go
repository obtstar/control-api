package config

import "testing"

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
