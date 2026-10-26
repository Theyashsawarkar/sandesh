package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// UnitPath is where the background sync's systemd user unit lives.
func UnitPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", "sandesh.service")
}

// Unit is the systemd user unit that runs `sandesh daemon`.
func Unit(bin string) string {
	return fmt.Sprintf(`[Unit]
Description=sandesh: Gmail and Google Chat sync
Documentation=https://github.com/Theyashsawarkar/sandesh
After=network-online.target
StartLimitIntervalSec=0

[Service]
ExecStart=%s daemon
Restart=on-failure
RestartSec=5
RestartSteps=5
RestartMaxDelaySec=60

[Install]
WantedBy=default.target
`, bin)
}

// serviceBinary prefers the sandesh on PATH (it survives rebuilds and
// updates), else this binary.
func serviceBinary() string {
	if p, err := exec.LookPath("sandesh"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return Executable()
}

// InstallService writes, enables and (re)starts the background sync.
func InstallService() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("no systemd here: run `sandesh daemon` from your session startup instead")
	}
	if err := os.MkdirAll(filepath.Dir(UnitPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(UnitPath(), []byte(Unit(serviceBinary())), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "sandesh.service"}, {"restart", "sandesh.service"}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// ServiceInstalled: the unit file exists.
func ServiceInstalled() bool {
	_, err := os.Stat(UnitPath())
	return err == nil
}
