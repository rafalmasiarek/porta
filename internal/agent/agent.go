package agent

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "sync"
    "time"

    "github.com/rafalmasiarek/porta/internal/backup"
    "github.com/rafalmasiarek/porta/internal/config"
    "github.com/rafalmasiarek/porta/internal/crypto"
    "github.com/rafalmasiarek/porta/internal/envruntime"
    "github.com/rafalmasiarek/porta/internal/process"
    "github.com/rafalmasiarek/porta/internal/logx"
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
    mu      sync.Mutex
    active  map[string]*Snapshot
    lastOp  map[string]time.Time
}

func Start(ctx context.Context) error {
    a := &Agent{
        active: map[string]*Snapshot{},
        lastOp: map[string]time.Time{},
    }
    logx.Info("porta-agent", "started")

    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
            a.reconcile(ctx)
        }
    }
}

func (a *Agent) reconcile(ctx context.Context) {
    volumes, _ := detectVolumes()
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

func detectVolumes() ([]MountedVolume, error) {
    entries, err := os.ReadDir("/Volumes")
    if err != nil {
        return nil, err
    }
    out := make([]MountedVolume, 0)
    for _, e := range entries {
        root := filepath.Join("/Volumes", e.Name())
        for _, name := range []string{"porta.yml", ".porta.yml"} {
            cfg := filepath.Join(root, name)
            if _, err := os.Stat(cfg); err == nil {
                out = append(out, MountedVolume{Root: root, ConfigPath: cfg})
                break
            }
        }
    }
    sort.Slice(out, func(i,j int) bool { return out[i].Root < out[j].Root })
    return out, nil
}

func buildSnapshot(configPath string) (*Snapshot, error) {
    root := filepath.Dir(configPath)
    runtime, err := envruntime.Load(root, crypto.DefaultPrivateKeyPath())
    if err != nil {
        return nil, err
    }

    var cfg *config.Config
    var cfgRoot string
    for i:=0; i<5; i++ {
        cfg, cfgRoot, err = config.Load(configPath, runtime)
        if err == nil {
            break
        }
        time.Sleep(150 * time.Millisecond)
    }
    if err != nil {
        return nil, err
    }
    env := envruntime.Merge(runtime)
    sig, err := signature(root, configPath)
    if err != nil {
        return nil, err
    }
    return &Snapshot{Config: cfg, Root: cfgRoot, Env: env, Signature: sig}, nil
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
    for _, command := range snap.Config.Hooks.OnAttach {
        if err := process.Run(command, snap.Env, "/"); err != nil {
            logx.Info("porta-agent", "attach hook error: %v", err)
        }
    }
    for _, job := range snap.Config.Jobs {
        if job.RunOn != "" && job.RunOn != "attach" {
            continue
        }
        if job.Mode == "background" {
            if err := process.StartBackground(job.Command, snap.Env, "/", job.LogFile); err != nil {
                logx.Info("porta-agent", "background job error: %v", err)
            }
        } else {
            if err := process.Run(job.Command, snap.Env, "/"); err != nil {
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
    for _, job := range snap.Config.Jobs {
        if job.RunOn != "detach" {
            continue
        }
        if job.Mode == "background" {
            if err := process.StartBackground(job.Command, snap.Env, "/", job.LogFile); err != nil {
                logx.Info("porta-agent", "background detach job error: %v", err)
            }
        } else {
            if err := process.Run(job.Command, snap.Env, "/"); err != nil {
                logx.Info("porta-agent", "foreground detach job error: %v", err)
            }
        }
    }
    for _, command := range snap.Config.Hooks.OnDetach {
        if err := process.Run(command, snap.Env, "/"); err != nil {
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
