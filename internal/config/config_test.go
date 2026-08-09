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
