package process

import (
    "os"
    "os/exec"
    "path/filepath"
    "runtime"

    "github.com/rafalmasiarek/porta/internal/logx"
)

func Run(command string, env []string, workingDir string, debug bool) error {
    logx.Info("porta", "running command: %s", command)

    cmd := commandForOS(command)
    cmd.Env = mergeEnv(env)

    if workingDir == "" {
        workingDir = defaultWorkingDir()
    }
    cmd.Dir = workingDir

    if debug {
        out, err := cmd.CombinedOutput()
        if len(out) > 0 {
            logx.Info("porta", "command output: %s", string(out))
        }
        if err != nil {
            logx.Info("porta", "command failed: %v", err)
        }
        return err
    }

    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}

func StartBackground(command string, env []string, workingDir, logPath string, debug bool) error {
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

    cmd := commandForOS(command)
    cmd.Env = mergeEnv(env)
    cmd.Dir = workingDir
    cmd.Stdout = logFile
    cmd.Stderr = logFile

    if debug {
        logx.Info("porta", "background log file: %s", logPath)
    }

    return cmd.Start()
}

func mergeEnv(extra []string) []string {
    if len(extra) == 0 {
        return os.Environ()
    }

    merged := map[string]string{}

    for _, item := range os.Environ() {
        key, val, ok := splitEnv(item)
        if ok {
            merged[key] = val
        }
    }

    for _, item := range extra {
        key, val, ok := splitEnv(item)
        if ok {
            merged[key] = val
        }
    }

    out := make([]string, 0, len(merged))
    for k, v := range merged {
        out = append(out, k+"="+v)
    }
    return out
}

func splitEnv(s string) (string, string, bool) {
    for i := 0; i < len(s); i++ {
        if s[i] == '=' {
            return s[:i], s[i+1:], true
        }
    }
    return "", "", false
}

func commandForOS(command string) *exec.Cmd {
    if runtime.GOOS == "windows" {
        return exec.Command("cmd", "/C", command)
    }
    return exec.Command("/bin/sh", "-lc", command)
}

func defaultWorkingDir() string {
    if runtime.GOOS == "windows" {
        if wd, err := os.Getwd(); err == nil && wd != "" {
            return wd
        }
        return "."
    }
    return "/"
}