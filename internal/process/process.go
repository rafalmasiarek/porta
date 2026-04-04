package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/rafalmasiarek/porta/internal/logx"
)

func Run(command string, env []string, workingDir string) error {
	logx.Info("porta", "running command: %s", command)

	cmd := buildCommand(command)
	cmd.Env = env

	if workingDir == "" {
		workingDir = defaultWorkingDir()
	}
	cmd.Dir = workingDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func StartBackground(command string, env []string, workingDir, logPath string) error {
	logx.Info("porta", "starting background command: %s", command)

	if workingDir == "" {
		workingDir = defaultWorkingDir()
	}

	if logPath == "" {
		logPath = filepath.Join(workingDir, ".porta", "logs", "background.log")
	}

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	cmd := buildCommand(command)
	cmd.Env = env
	cmd.Dir = workingDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	return cmd.Start()
}

func buildCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", command)
	}
	return exec.Command("bash", "-lc", command)
}

func defaultWorkingDir() string {
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}

	if runtime.GOOS == "windows" {
		return `C:\`
	}

	return "/"
}
