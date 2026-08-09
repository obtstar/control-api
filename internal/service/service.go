// Package service systemd 用户级服务管理（模板：control-piekbs internal/service）
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"control-api/internal/config"
)

const unitName = "control-api.service"

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	return filepath.Join(dir, unitName), os.MkdirAll(dir, 0o755)
}

func Run(cfg *config.Config, action string) error {
	switch action {
	case "install":
		return install(cfg)
	case "uninstall":
		return uninstall()
	case "status":
		return systemctl("status", unitName)
	default:
		return fmt.Errorf("未知 action: %s（install|uninstall|status）", action)
	}
}

func install(cfg *config.Config) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	path, err := unitPath()
	if err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=control-api (Agent 平台编排后端)
After=default.target

[Service]
ExecStart=%s serve
EnvironmentFile=-%s/control.env
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, exe, cfg.Paths.Home)
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", unitName); err != nil {
		return err
	}
	fmt.Println("已安装并启动:", path)
	return nil
}

func uninstall() error {
	_ = systemctl("disable", "--now", unitName)
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("已卸载:", unitName)
	return systemctl("daemon-reload")
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
