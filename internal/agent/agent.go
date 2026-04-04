package agent

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/rafalmasiarek/porta/internal/backup"
    "github.com/rafalmasiarek/porta/internal/config"
    "github.com/rafalmasiarek/porta/internal/crypto"
    "github.com/rafalmasiarek/porta/internal/envruntime"
    "github.com/rafalmasiarek/porta/internal/logx"
    "github.com/rafalmasiarek/porta/internal/process"
)

type MountedVolume struct {
    Root       string
    ConfigPath string
}

type Snapshot struct {
    Config    *config.Config
    Root      string
    Env       []string
    Signature string
}

type Agent struct {
    mu     sync.Mutex
    active map[string]*Snapshot
    lastOp map[string]time.Time
}

func Start(ctx context.Context) error {
    a := &Agent{
        active: map[string]*Snapshot{},
        lastOp: map[string]time.Time{},
    }

    logx.Info("porta-agent", "started")

    trigger := make(chan struct{}, 1)

    go func() {
        if err := watchVolumeChanges(ctx, trigger); err != nil {
            logx.Info("porta-agent", "watcher error: %v", err)
        }
    }()

    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
            a.reconcile(ctx)
        case <-trigger:
            a.reconcile(ctx)
        }
    }
}

func (a *Agent) reconcile(ctx context.Context) {
    volumes, err := detectVolumes()
    if err != nil {
        logx.Info("porta-agent", "detect error: %v", err)
        return
    }

    current := map[string]MountedVolume{}
    for _, v := range volumes {
        current[v.Root] = v
    }

    a.mu.Lock()
    defer a.mu.Unlock()

    for root, snap := range a.active {
        if _, ok := current[root]; !ok {
            if a.debounce(root) {
                continue
            }
            logx.Info("porta-agent", "volume removed: %s", root)
            a.runDetach(snap)
            delete(a.active, root)
        }
    }

    for root, vol := range current {
        if _, ok := a.active[root]; !ok {
            if a.debounce(root) {
                continue
            }

            logx.Info("porta-agent", "volume detected: %s", root)

            snap, err := buildSnapshot(vol.ConfigPath)
            if err != nil {
                logx.Info("porta-agent", "snapshot error: %v", err)
                continue
            }

            a.active[root] = snap
            a.runAttach(ctx, snap)
            continue
        }

        currentSnap := a.active[root]

        newSnap, err := buildSnapshot(vol.ConfigPath)
        if err != nil {
            logx.Info("porta-agent", "reload snapshot error: %v", err)
            continue
        }

        if newSnap.Signature != currentSnap.Signature {
            logx.Info("porta-agent", "configuration changed, reloading: %s", root)
            a.active[root] = newSnap
        }

        a.reconcileBackup(ctx, a.active[root])
    }
}

func (a *Agent) debounce(root string) bool {
    now := time.Now()

    if last, ok := a.lastOp[root]; ok && now.Sub(last) < 2*time.Second {
        return true
    }

    a.lastOp[root] = now
    return false
}

func buildSnapshot(configPath string) (*Snapshot, error) {
    root := filepath.Dir(configPath)

    runtimeEnv, err := envruntime.Load(root, crypto.DefaultPrivateKeyPath())
    if err != nil {
        return nil, err
    }

    var cfg *config.Config
    var cfgRoot string

    for i := 0; i < 5; i++ {
        cfg, cfgRoot, err = config.Load(configPath, runtimeEnv)
        if err == nil {
            break
        }
        time.Sleep(150 * time.Millisecond)
    }

    if err != nil {
        return nil, err
    }

    env := envruntime.Merge(runtimeEnv)

    sig, err := signature(root, configPath)
    if err != nil {
        return nil, err
    }

    return &Snapshot{
        Config:    cfg,
        Root:      cfgRoot,
        Env:       env,
        Signature: sig,
    }, nil
}

func signature(root, configPath string) (string, error) {
    h := sha256.New()

    for _, candidate := range []string{
        configPath,
        filepath.Join(root, ".porta.enc"),
        filepath.Join(root, "config.enc"),
    } {
        if st, err := os.Stat(candidate); err == nil {
            h.Write([]byte(candidate))
            h.Write([]byte(st.ModTime().UTC().Format(time.RFC3339Nano)))
            h.Write([]byte(fmt.Sprintf("%d", st.Size())))
        }
    }

    return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *Agent) runAttach(ctx context.Context, snap *Snapshot) {
    wd := snap.Root

    for _, hook := range snap.Config.Hooks.OnAttach {
        if !JobMatchesOS(hook.OS) {
            continue
        }

        if err := process.Run(hook.Command, snap.Env, wd, hook.Debug); err != nil {
            logx.Info("porta-agent", "attach hook error: %v", err)
        }
    }

    for _, job := range snap.Config.Jobs {
        if !JobMatchesOS(job.OS) {
            continue
        }

        if job.RunOn != "" && job.RunOn != "attach" {
            continue
        }

        jobWD := job.WorkingDir
        if jobWD == "" {
            jobWD = wd
        }

        if job.Mode == "background" {
            if err := process.StartBackground(job.Command, snap.Env, jobWD, job.LogFile, job.Debug); err != nil {
                logx.Info("porta-agent", "background job error: %v", err)
            }
        } else {
            if err := process.Run(job.Command, snap.Env, jobWD, job.Debug); err != nil {
                logx.Info("porta-agent", "foreground job error: %v", err)
            }
        }
    }

    a.reconcileBackup(ctx, snap)
}

func (a *Agent) runDetach(snap *Snapshot) {
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()

    svc, err := backup.New(snap.Config, snap.Root)
    if err == nil {
        svc.ReconcileSpool(time.Now().UTC())
        _ = svc.SyncAll(ctx)
    }

    wd := snap.Root

    for _, job := range snap.Config.Jobs {
        if !JobMatchesOS(job.OS) {
            continue
        }

        if job.RunOn != "detach" {
            continue
        }

        jobWD := job.WorkingDir
        if jobWD == "" {
            jobWD = wd
        }

        if job.Mode == "background" {
            if err := process.StartBackground(job.Command, snap.Env, jobWD, job.LogFile, job.Debug); err != nil {
                logx.Info("porta-agent", "background detach job error: %v", err)
            }
        } else {
            if err := process.Run(job.Command, snap.Env, jobWD, job.Debug); err != nil {
                logx.Info("porta-agent", "foreground detach job error: %v", err)
            }
        }
    }

    for _, hook := range snap.Config.Hooks.OnDetach {
        if !JobMatchesOS(hook.OS) {
            continue
        }

        if err := process.Run(hook.Command, snap.Env, wd, hook.Debug); err != nil {
            logx.Info("porta-agent", "detach hook error: %v", err)
        }
    }
}

func (a *Agent) reconcileBackup(ctx context.Context, snap *Snapshot) {
    svc, err := backup.New(snap.Config, snap.Root)
    if err != nil {
        logx.Info("porta-agent", "backup service error: %v", err)
        return
    }

    now := time.Now().UTC()

    svc.ReconcileSpool(now)

    syncCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
    defer cancel()

    if err := svc.SyncAll(syncCtx); err != nil {
        logx.Info("porta-agent", "sync had recoverable errors: %v", err)
    }

    due, err := svc.ShouldCreateNewBackup(now)
    if err != nil {
        due = true
    }

    if due {
        createCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
        defer cancel()

        if _, err := svc.Create(createCtx, ""); err != nil {
            logx.Info("porta-agent", "backup create error: %v", err)
        }
    }
}