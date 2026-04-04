package cli

import (
    "context"
    "encoding/json"
    "flag"
    "fmt"
    "os"
    "path/filepath"
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
  porta backup create [--config porta.yml]
  porta backup sync [--config porta.yml]
  porta backup list [--config porta.yml]
  porta backup restore [--config porta.yml]
  porta agent start`)
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
    configPath := fs.String("config", "", "")

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

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    result := map[string]any{
        "storage_reachable": svc.RemoteAvailable(ctx),
        "spool":             svc.SpoolRoot,
        "source":            cfg.Backup.Source,
    }

    b, _ := json.MarshalIndent(result, "", "  ")
    fmt.Println(string(b))

    return nil
}

func runCmd(args []string) error {
    fs := flag.NewFlagSet("run", flag.ContinueOnError)
    configPath := fs.String("config", "", "")
    phase := fs.String("phase", "attach", "")

    if err := fs.Parse(args); err != nil {
        return err
    }

    cfg, root, env, err := loadConfigWithRuntime(*configPath)
    if err != nil {
        return err
    }

    switch *phase {
    case "attach":
        for _, hook := range cfg.Hooks.OnAttach {
            if !agent.JobMatchesOS(hook.OS) {
                continue
            }

            if err := process.Run(hook.Command, env, root, hook.Debug); err != nil {
                return err
            }
        }

        for _, job := range cfg.Jobs {
            if !agent.JobMatchesOS(job.OS) {
                continue
            }

            if job.RunOn != "" && job.RunOn != "attach" {
                continue
            }

            jobWD := job.WorkingDir
            if jobWD == "" {
                jobWD = root
            }

            if job.Mode == "background" {
                if err := process.StartBackground(job.Command, env, jobWD, job.LogFile, job.Debug); err != nil {
                    return err
                }
            } else {
                if err := process.Run(job.Command, env, jobWD, job.Debug); err != nil {
                    return err
                }
            }
        }

    case "detach":
        for _, job := range cfg.Jobs {
            if !agent.JobMatchesOS(job.OS) {
                continue
            }

            if job.RunOn != "detach" {
                continue
            }

            jobWD := job.WorkingDir
            if jobWD == "" {
                jobWD = root
            }

            if job.Mode == "background" {
                if err := process.StartBackground(job.Command, env, jobWD, job.LogFile, job.Debug); err != nil {
                    return err
                }
            } else {
                if err := process.Run(job.Command, env, jobWD, job.Debug); err != nil {
                    return err
                }
            }
        }

        for _, hook := range cfg.Hooks.OnDetach {
            if !agent.JobMatchesOS(hook.OS) {
                continue
            }

            if err := process.Run(hook.Command, env, root, hook.Debug); err != nil {
                return err
            }
        }

    default:
        return fmt.Errorf("invalid phase %q", *phase)
    }

    return nil
}

func backupCmd(args []string) error {
    cfg, root, _, err := loadConfigWithRuntime("")
    if err != nil {
        return err
    }

    svc, err := backup.New(cfg, root)
    if err != nil {
        return err
    }

    ctx := context.Background()

    switch args[0] {
    case "create":
        _, err := svc.Create(ctx, "")
        return err

    case "sync":
        return svc.SyncAll(ctx)

    case "list":
        list, err := svc.List(ctx)
        if err != nil {
            return err
        }

        for _, m := range list {
            fmt.Println(m.BackupID, m.CreatedAt)
        }
    }

    return nil
}

func secretsCmd(args []string) error {
    return fmt.Errorf("not implemented")
}

func agentCmd(args []string) error {
    return agent.Start(context.Background())
}