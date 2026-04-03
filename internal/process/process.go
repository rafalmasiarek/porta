package process

import (
    "os"
    "os/exec"
    "path/filepath"

    "github.com/rafalmasiarek/porta/internal/logx"
)

func Run(command string, env []string, workingDir string) error {
    logx.Info("porta", "running command: %s", command)
    cmd := exec.Command("bash", "-lc", command)
    cmd.Env = env
    if workingDir == "" {
        workingDir = "/"
    }
    cmd.Dir = workingDir
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}

func StartBackground(command string, env []string, workingDir, logPath string) error {
    logx.Info("porta", "starting background command: %s", command)
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
    cmd := exec.Command("bash", "-lc", command)
    cmd.Env = env
    if workingDir == "" {
        workingDir = "/"
    }
    cmd.Dir = workingDir
    cmd.Stdout = logFile
    cmd.Stderr = logFile
    return cmd.Start()
}
