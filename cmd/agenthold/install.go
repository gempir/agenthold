package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	configPath := fs.String("config", "/etc/agenthold/agenthold.yaml", "absolute configuration file path")
	unitDir := fs.String("unit-dir", "/etc/systemd/system", "systemd unit directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("install accepts flags only")
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd installation requires Linux")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	data, err := systemdUnit(exe, *configPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(*configPath); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if err := os.MkdirAll(*unitDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(*unitDir, ".agenthold-*.service")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0o644); err == nil {
		_, err = io.WriteString(tmp, data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	target := filepath.Join(*unitDir, "agenthold.service")
	if err := os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\nRun: systemctl daemon-reload && systemctl enable --now agenthold\n", target)
	return nil
}

func systemdUnit(exe, configPath string) (string, error) {
	for _, path := range []string{exe, configPath} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return "", fmt.Errorf("service paths must be absolute and contain no control characters")
		}
	}
	quote := func(value string) string {
		value = strings.ReplaceAll(value, "%", "%%")
		value = strings.ReplaceAll(value, "\\", "\\\\")
		value = strings.ReplaceAll(value, "\"", "\\\"")
		return "\"" + value + "\""
	}
	// KillMode=process keeps microVMs alive across controller restarts. The
	// driver adopts their recorded processes and persistent disks on startup.
	return "[Unit]\nDescription=agenthold controller and sandbox node\nAfter=network-online.target\nWants=network-online.target\n\n" +
		"[Service]\nType=simple\nUser=root\nExecStart=" + quote(exe) + "\nEnvironment=" + quote("AGENTHOLD_CONFIG="+configPath) + "\n" +
		"WorkingDirectory=/\nRestart=on-failure\nRestartSec=3\nTimeoutStopSec=20\nKillMode=process\nDelegate=yes\nUMask=0077\n\n" +
		"[Install]\nWantedBy=multi-user.target\n", nil
}
