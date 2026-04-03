package cli

import (
    "context"
    "encoding/json"
    "flag"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/rafalmasiarek/porta/internal/agent"
    "github.com/rafalmasiarek/porta/internal/backup"
    "github.com/rafalmasiarek/porta/internal/config"
    "github.com/rafalmasiarek/porta/internal/crypto"
    "github.com/rafalmasiarek/porta/internal/envruntime"
    "github.com/rafalmasiarek/porta/internal/process"
)

func Run(args []string, version string) error {
    if len(args) == 0 {
        printHelp()
        return nil
    }

    switch args[0] {
    case "version":
        fmt.Println(version)
        return nil
    case "status":
        return statusCmd(args[1:])
    case "run":
        return runCmd(args[1:])
    case "backup":
        return backupCmd(args[1:])
    case "secrets":
        return secretsCmd(args[1:])
    case "agent":
        return agentCmd(args[1:])
    default:
        return fmt.Errorf("unknown command %q", args[0])
    }
}

func printHelp() {
    fmt.Println(`porta commands:
  porta version
  porta status [--config porta.yml]
  porta run [--phase attach|detach] [--config porta.yml]
  porta backup create [--config porta.yml] [--backup-id ID]
  porta backup sync [--config porta.yml] [--backup-id ID]
  porta backup list [--config porta.yml]
  porta backup restore [--config porta.yml] [--backup-id latest|ID] [--dest DIR] [--file PATH]
  porta secrets encrypt --in FILE --pub FILE [--out FILE]
  porta secrets decrypt --in FILE [--key FILE] [--out FILE]
  porta agent start
  porta agent render --platform linux|macos|windows`)
}

func loadConfigWithRuntime(configPath string) (*config.Config, string, []string, error) {
    resolvedPath := configPath
    if resolvedPath == "" {
        cwd, err := os.Getwd()
        if err != nil {
            return nil, "", nil, err
        }
        found, err := config.FindConfig(cwd)
        if err != nil {
            return nil, "", nil, err
        }
        resolvedPath = found
    }

    root := filepath.Dir(resolvedPath)

    runtime, err := envruntime.Load(root, crypto.DefaultPrivateKeyPath())
    if err != nil {
        return nil, "", nil, err
    }

    cfg, root, err := config.Load(resolvedPath, runtime)
    if err != nil {
        return nil, "", nil, err
    }

    return cfg, root, envruntime.Merge(runtime), nil
}

func statusCmd(args []string) error {
    fs := flag.NewFlagSet("status", flag.ContinueOnError)
    configPath := fs.String("config", "", "Path to porta.yml")
    if err := fs.Parse(args); err != nil {
        return err
    }

    cfg, root, _, err := loadConfigWithRuntime(*configPath)
    if err != nil {
        return err
    }

    svc, err := backup.New(cfg, root)
    if err != nil {
        return err
    }

    svc.ReconcileSpool(time.Now().UTC())

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    storageReachable := svc.RemoteAvailable(ctx)
    backupDue, err := svc.ShouldCreateNewBackup(time.Now().UTC())
    if err != nil {
        return err
    }

    result := map[string]any{
        "storage_reachable": storageReachable,
        "backup_due":        backupDue,
        "spool_root":        svc.SpoolRoot,
        "source":            cfg.Backup.Source,
        "interval":          cfg.Backup.Interval,
    }

    b, err := json.MarshalIndent(result, "", "  ")
    if err != nil {
        return err
    }

    fmt.Println(string(b))
    return nil
}

func runCmd(args []string) error {
    fs := flag.NewFlagSet("run", flag.ContinueOnError)
    configPath := fs.String("config", "", "Path to porta.yml")
    phase := fs.String("phase", "attach", "attach or detach")

    if err := fs.Parse(args); err != nil {
        return err
    }

    cfg, root, mergedEnv, err := loadConfigWithRuntime(*configPath)
    if err != nil {
        return err
    }

    mergedEnv = append(mergedEnv,
        "PORTA_ROOT="+root,
        "PORTA_PHASE="+*phase,
    )

    switch *phase {

    case "attach":
        for _, command := range cfg.Hooks.OnAttach {
            if err := process.Run(command, mergedEnv, root); err != nil {
                return err
            }
        }

        for _, job := range cfg.Jobs {
            if job.RunOn != "" && job.RunOn != "attach" {
                continue
            }

            if strings.EqualFold(job.Mode, "background") {
                if err := process.StartBackground(job.Command, mergedEnv, job.WorkingDir, job.LogFile); err != nil {
                    return err
                }
            } else {
                if err := process.Run(job.Command, mergedEnv, job.WorkingDir); err != nil {
                    return err
                }
            }
        }

        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        svc, err := backup.New(cfg, root)
        if err != nil {
            return err
        }

        svc.ReconcileSpool(time.Now().UTC())

        if err := svc.SyncAll(ctx); err != nil {
            fmt.Println("[porta] backup sync completed with recoverable errors:", err)
        }

    case "detach":
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        svc, err := backup.New(cfg, root)
        if err != nil {
            return err
        }

        svc.ReconcileSpool(time.Now().UTC())

        if err := svc.SyncAll(ctx); err != nil {
            fmt.Println("[porta] backup sync completed with recoverable errors:", err)
        }

        for _, job := range cfg.Jobs {
            if job.RunOn != "detach" {
                continue
            }

            if strings.EqualFold(job.Mode, "background") {
                if err := process.StartBackground(job.Command, mergedEnv, "/", job.LogFile); err != nil {
                    return err
                }
            } else {
                if err := process.Run(job.Command, mergedEnv, "/"); err != nil {
                    return err
                }
            }
        }

        for _, command := range cfg.Hooks.OnDetach {
            if err := process.Run(command, mergedEnv, "/"); err != nil {
                return err
            }
        }

    default:
        return fmt.Errorf("invalid phase %q", *phase)
    }

    return nil
}

func backupCmd(args []string) error {
    if len(args) == 0 {
        return fmt.Errorf("backup subcommand required")
    }

    switch args[0] {

    case "create":
        fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
        configPath := fs.String("config", "", "Path to porta.yml")
        backupID := fs.String("backup-id", "", "Backup ID")

        if err := fs.Parse(args[1:]); err != nil {
            return err
        }

        cfg, root, _, err := loadConfigWithRuntime(*configPath)
        if err != nil {
            return err
        }

        svc, err := backup.New(cfg, root)
        if err != nil {
            return err
        }

        ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
        defer cancel()

        id, err := svc.Create(ctx, *backupID)
        if err != nil {
            return err
        }

        fmt.Println("[porta] backup created", id)
        return nil

    case "sync":
        fs := flag.NewFlagSet("backup sync", flag.ContinueOnError)
        configPath := fs.String("config", "", "Path to porta.yml")
        backupID := fs.String("backup-id", "", "Backup ID")

        if err := fs.Parse(args[1:]); err != nil {
            return err
        }

        cfg, root, _, err := loadConfigWithRuntime(*configPath)
        if err != nil {
            return err
        }

        svc, err := backup.New(cfg, root)
        if err != nil {
            return err
        }

        svc.ReconcileSpool(time.Now().UTC())

        ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
        defer cancel()

        if *backupID != "" {
            return svc.SyncOne(ctx, *backupID)
        }

        return svc.SyncAll(ctx)

    case "list":
        fs := flag.NewFlagSet("backup list", flag.ContinueOnError)
        configPath := fs.String("config", "", "Path to porta.yml")

        if err := fs.Parse(args[1:]); err != nil {
            return err
        }

        cfg, root, _, err := loadConfigWithRuntime(*configPath)
        if err != nil {
            return err
        }

        svc, err := backup.New(cfg, root)
        if err != nil {
            return err
        }

        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        list, err := svc.List(ctx)
        if err != nil {
            return err
        }

        for _, m := range list {
            fmt.Printf("%s\t%s\t%d packs\t%s\n",
                m.BackupID,
                m.CreatedAt.Format(time.RFC3339),
                len(m.Packs),
                m.Status,
            )
        }

        return nil

    case "restore":
        fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
        configPath := fs.String("config", "", "Path to porta.yml")
        backupID := fs.String("backup-id", "latest", "Backup ID or latest")
        destDir := fs.String("dest", "", "Destination directory")
        filePath := fs.String("file", "", "Single file to restore")

        if err := fs.Parse(args[1:]); err != nil {
            return err
        }

        cfg, root, _, err := loadConfigWithRuntime(*configPath)
        if err != nil {
            return err
        }

        if *destDir == "" {
            *destDir = cfg.Backup.Source
        }

        svc, err := backup.New(cfg, root)
        if err != nil {
            return err
        }

        ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
        defer cancel()

        return svc.Restore(
            ctx,
            *backupID,
            *destDir,
            strings.TrimPrefix(filepath.ToSlash(*filePath), "./"),
        )

    default:
        return fmt.Errorf("unknown backup command %q", args[0])
    }
}

func secretsCmd(args []string) error {
    if len(args) == 0 {
        return fmt.Errorf("secrets subcommand required")
    }

    switch args[0] {

    case "encrypt":
        fs := flag.NewFlagSet("secrets encrypt", flag.ContinueOnError)
        in := fs.String("in", "", "Input file")
        out := fs.String("out", "", "Output file")
        pub := fs.String("pub", "", "Public key path")

        if err := fs.Parse(args[1:]); err != nil {
            return err
        }

        if *in == "" || *pub == "" {
            return fmt.Errorf("--in and --pub are required")
        }

        return crypto.EncryptFile(*in, *out, *pub)

    case "decrypt":
        fs := flag.NewFlagSet("secrets decrypt", flag.ContinueOnError)
        in := fs.String("in", "", "Input file")
        out := fs.String("out", "", "Output file")
        key := fs.String("key", crypto.DefaultPrivateKeyPath(), "Private key path")

        if err := fs.Parse(args[1:]); err != nil {
            return err
        }

        if *in == "" {
            return fmt.Errorf("--in is required")
        }

        return crypto.DecryptFile(*in, *out, *key)

    default:
        return fmt.Errorf("unknown secrets command %q", args[0])
    }
}

func agentCmd(args []string) error {
    if len(args) == 0 {
        return fmt.Errorf("agent command required")
    }

    switch args[0] {

    case "start":
        return agent.Start(context.Background())

    case "render":
        fs := flag.NewFlagSet("agent render", flag.ContinueOnError)
        platform := fs.String("platform", "linux", "linux, macos, or windows")

        if err := fs.Parse(args[1:]); err != nil {
            return err
        }

        exe, _ := os.Executable()

        switch *platform {

        case "linux":
            fmt.Printf(`[Unit]
Description=porta auto runner

[Service]
Type=simple
ExecStart=%s agent start
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, exe)

        case "macos":
            fmt.Printf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.porta.agent</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>agent</string><string>start</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
`, exe)

        case "windows":
            fmt.Printf(`# Windows Task Scheduler example
# Program: %s
# Arguments: agent start
`, exe)

        default:
            return fmt.Errorf("invalid platform %q", *platform)
        }

        return nil

    default:
        return fmt.Errorf("unknown agent command %q", args[0])
    }
}
