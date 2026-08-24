//go:build linux

package autostart

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func Install(configPath string, interval time.Duration, system bool) (string, error) {
	sourceExe, err := os.Executable()
	if err != nil {
		return "", err
	}
	sourceExe, err = filepath.Abs(sourceExe)
	if err != nil {
		return "", err
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(configPath); err != nil {
		return "", fmt.Errorf("agent config not found: %w", err)
	}

	var unitPath, installedExe string
	var systemctlPrefix []string
	if system {
		if os.Geteuid() != 0 {
			return "", fmt.Errorf("--system requires root; run with sudo")
		}
		unitPath = "/etc/systemd/system/jjp-agent.service"
		installedExe = "/usr/local/bin/jjp"
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		unitPath = filepath.Join(home, ".config", "systemd", "user", "jjp-agent.service")
		installedExe = filepath.Join(home, ".local", "bin", "jjp")
		systemctlPrefix = []string{"--user"}
	}

	if err := copyExecutable(sourceExe, installedExe); err != nil {
		return "", fmt.Errorf("install binary: %w", err)
	}

	wantedBy := "default.target"
	if system {
		wantedBy = "multi-user.target"
	}
	unit := fmt.Sprintf(`[Unit]
Description=Jjamppong node agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s agent --config %s --interval %s
Restart=always
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=%s
`, quote(installedExe), quote(configPath), interval.String(), wantedBy)

	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return "", err
	}

	args := append(append([]string{}, systemctlPrefix...), "daemon-reload")
	if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
		return unitPath, fmt.Errorf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))
	}
	args = append(append([]string{}, systemctlPrefix...), "enable", "--now", "jjp-agent.service")
	if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
		return unitPath, fmt.Errorf("systemctl enable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return unitPath, nil
}

func copyExecutable(src, dst string) error {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func quote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
