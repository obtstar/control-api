// service 包单测（TASK-000015）：unitContent 纯函数断言；
// exec systemctl 部分不 mock（设计取舍：安装时真实调用，见 service.go install）。
package service

import (
	"strings"
	"testing"
)

func TestUnitContent(t *testing.T) {
	unit := unitContent("/home/dev/control-api/control-api", "/home/dev")
	for _, want := range []string{
		"[Unit]",
		"Description=control-api (Agent 平台编排后端)",
		"After=default.target",
		"ExecStart=/home/dev/control-api/control-api serve",
		"EnvironmentFile=-/home/dev/control.env",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit 缺少 %q\n---unit---\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "%!") {
		t.Errorf("unit 含未格式化占位符: %s", unit)
	}
}
