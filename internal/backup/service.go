package backup

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "time"

    "github.com/rafalmasiarek/porta/internal/archive"
    "github.com/rafalmasiarek/porta/internal/config"
    "github.com/rafalmasiarek/porta/internal/s3util"
    "github.com/rafalmasiarek/porta/internal/logx"
)

type StateStatus string

const (
    StatusPacking  StateStatus = "packing"
    StatusReady    StateStatus = "ready"
    StatusSyncing  StateStatus = "syncing"
    StatusComplete StateStatus = "complete"
    StatusBroken   StateStatus = "broken"
)

type State struct {
    BackupID    string      `json:"backup_id"`
    Scope       string      `json:"scope"`
    Status      StateStatus `json:"status"`
    CreatedAt   time.Time   `json:"created_at"`
    UpdatedAt   time.Time   `json:"updated_at"`
    LastError   string      `json:"last_error,omitempty"`
    CompletedAt *time.Time  `json:"completed_at,omitempty"`
}

type Manifest struct {
    Version        int                 `json:"version"`
    Scope          string              `json:"scope"`
    Source         string              `json:"source"`
    BackupID       string              `json:"backup_id"`
    CreatedAt      time.Time           `json:"created_at"`
    ChunkSizeBytes int64               `json:"chunk_size_bytes"`
    Include        []string            `json:"include"`
    Exclude        []string            `json:"exclude"`
    Files          []archive.FileEntry `json:"files"`
    Packs          []archive.Pack      `json:"packs"`
    Status         string              `json:"status"`
}

type Scope struct {
    Name           string
    Source         string
    ChunkSizeBytes int64
    Include        []string
    Exclude        []string
}

type Service struct {
    Cfg       *config.Config
    Root      string
    Scope     Scope
    Storage   *s3util.Client
    SpoolRoot string
}

func New(cfg *config.Config, root string) (*Service, error) {
    storage, err := s3util.New(cfg.Storage)
    if err != nil {
        return nil, err
    }
    scope := Scope{
        Name: "default",
        Source: cfg.Backup.Source,
        ChunkSizeBytes: int64(cfg.Backup.ChunkSizeMB) * 1024 * 1024,
        Include: append([]string{}, cfg.Backup.Include...),
        Exclude: append([]string{}, cfg.Backup.Exclude...),
    }
    return &Service{
        Cfg: cfg,
        Root: root,
        Scope: scope,
        Storage: storage,
        SpoolRoot: filepath.Join(root, ".porta", "spool", scope.Name),
    }, nil
}

func (s *Service) remoteRoot() string {
    return s.Storage.Join("backups", s.Scope.Name)
}
func (s *Service) remoteKey(backupID, name string) string {
    return s.Storage.Join("backups", s.Scope.Name, backupID, name)
}

func (s *Service) Create(ctx context.Context, backupID string) (string, error) {
    if backupID == "" {
        backupID = time.Now().UTC().Format("2006-01-02T15-04-05Z")
    }
    jobDir := filepath.Join(s.SpoolRoot, backupID)
    if err := os.MkdirAll(jobDir, 0o755); err != nil {
        return "", err
    }

    st, _ := s.loadState(jobDir)
    if st.BackupID == "" {
        st = State{
            BackupID: backupID,
            Scope: s.Scope.Name,
            Status: StatusPacking,
            CreatedAt: time.Now().UTC(),
            UpdatedAt: time.Now().UTC(),
        }
        if err := s.saveState(jobDir, st); err != nil {
            return "", err
        }
    }

    manifestPath := filepath.Join(jobDir, "manifest.draft.json")
    manifest, err := s.loadOrCreateManifest(manifestPath, backupID)
    if err != nil {
        st.Status = StatusBroken
        st.LastError = err.Error()
        st.UpdatedAt = time.Now().UTC()
        _ = s.saveState(jobDir, st)
        return "", err
    }

    for _, pack := range manifest.Packs {
        packPath := filepath.Join(jobDir, pack.Name)
        if _, err := os.Stat(packPath); err == nil {
            continue
        }
        logx.Info("porta", "creating pack %s", pack.Name)
        if err := archive.CreatePack(s.Scope.Source, pack, packPath); err != nil {
            st.Status = StatusBroken
            st.LastError = err.Error()
            st.UpdatedAt = time.Now().UTC()
            _ = s.saveState(jobDir, st)
            return "", err
        }
    }

    st.Status = StatusReady
    st.LastError = ""
    st.UpdatedAt = time.Now().UTC()
    if err := s.saveState(jobDir, st); err != nil {
        return "", err
    }

    logx.Info("porta", "local backup prepared %s", jobDir)
    if s.RemoteAvailable(ctx) {
        if err := s.SyncOne(ctx, backupID); err != nil {
            return backupID, nil
        }
    } else {
        logx.Info("porta", "remote storage unavailable, sync deferred")
    }

    return backupID, nil
}

func (s *Service) SyncAll(ctx context.Context) error {
    entries, err := os.ReadDir(s.SpoolRoot)
    if err != nil {
        if os.IsNotExist(err) {
            fmt.Println("[porta] nothing to sync")
            return nil
        }
        return err
    }

    names := make([]string, 0, len(entries))
    for _, e := range entries {
        if e.IsDir() {
            names = append(names, e.Name())
        }
    }
    sort.Strings(names)

    var errs []string
    for _, name := range names {
        if err := s.SyncOne(ctx, name); err != nil {
            errs = append(errs, name+": "+err.Error())
            logx.Info("porta", "sync skipped: %s %v", name, err)
            continue
        }
    }
    if len(errs) > 0 {
        return fmt.Errorf(strings.Join(errs, "; "))
    }
    return nil
}

func (s *Service) SyncOne(ctx context.Context, backupID string) error {
    jobDir := filepath.Join(s.SpoolRoot, backupID)
    st, err := s.loadState(jobDir)
    if err != nil {
        return s.handleBrokenDirectory(jobDir, backupID, err)
    }
    manifestBytes, err := os.ReadFile(filepath.Join(jobDir, "manifest.draft.json"))
    if err != nil {
        return s.handleBrokenDirectory(jobDir, backupID, err)
    }

    var m Manifest
    if err := json.Unmarshal(manifestBytes, &m); err != nil {
        return s.handleBrokenDirectory(jobDir, backupID, err)
    }

    st.Status = StatusSyncing
    st.UpdatedAt = time.Now().UTC()
    _ = s.saveState(jobDir, st)

    for _, pack := range m.Packs {
        key := s.remoteKey(backupID, pack.Name)
        exists, err := s.Storage.Exists(ctx, key)
        if err != nil {
            st.Status = StatusReady
            st.LastError = err.Error()
            st.UpdatedAt = time.Now().UTC()
            _ = s.saveState(jobDir, st)
            return err
        }
        if exists {
            continue
        }
        local := filepath.Join(jobDir, pack.Name)
        if _, err := os.Stat(local); err != nil {
            st.Status = StatusBroken
            st.LastError = "missing pack " + pack.Name
            st.UpdatedAt = time.Now().UTC()
            _ = s.saveState(jobDir, st)
            return fmt.Errorf("missing pack %s", pack.Name)
        }
        logx.Info("porta", "uploading %s", pack.Name)
        if err := s3util.Retry(ctx, 3, func() error { return s.Storage.UploadFile(ctx, local, key) }); err != nil {
            st.Status = StatusReady
            st.LastError = err.Error()
            st.UpdatedAt = time.Now().UTC()
            _ = s.saveState(jobDir, st)
            return err
        }
    }

    m.Status = string(StatusComplete)
    manifestBytes, err = json.MarshalIndent(m, "", "  ")
    if err != nil {
        return err
    }
    if err := s3util.Retry(ctx, 3, func() error {
        return s.Storage.UploadBytes(ctx, manifestBytes, s.remoteKey(backupID, "manifest.json"), "application/json")
    }); err != nil {
        st.Status = StatusReady
        st.LastError = err.Error()
        st.UpdatedAt = time.Now().UTC()
        _ = s.saveState(jobDir, st)
        return err
    }

    now := time.Now().UTC()
    st.Status = StatusComplete
    st.CompletedAt = &now
    st.LastError = ""
    st.UpdatedAt = now
    _ = s.saveState(jobDir, st)

    if err := os.RemoveAll(jobDir); err != nil {
        return err
    }
    logx.Info("porta", "backup synced %s", backupID)
    return nil
}

func (s *Service) List(ctx context.Context) ([]Manifest, error) {
    objs, err := s.Storage.List(ctx, s.remoteRoot(), true)
    if err != nil {
        return nil, err
    }
    list := make([]Manifest, 0)
    for _, obj := range objs {
        if !strings.HasSuffix(obj.Key, "/manifest.json") {
            continue
        }
        b, err := s.Storage.ReadAll(ctx, obj.Key)
        if err != nil {
            return nil, err
        }
        var m Manifest
        if err := json.Unmarshal(b, &m); err != nil {
            return nil, err
        }
        list = append(list, m)
    }
    sort.Slice(list, func(i,j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
    return list, nil
}

func (s *Service) Restore(ctx context.Context, backupID, destDir, fileFilter string) error {
    m, err := s.fetchManifest(ctx, backupID)
    if err != nil {
        return err
    }

    wanted := map[string]bool{}
    if fileFilter != "" {
        wanted[fileFilter] = true
    }
    cacheDir := filepath.Join(s.Root, ".porta", "restore-cache", s.Scope.Name, m.BackupID)
    if err := os.MkdirAll(cacheDir, 0o755); err != nil {
        return err
    }
    for _, pack := range m.Packs {
        if fileFilter != "" && !packContainsFile(pack, fileFilter) {
            continue
        }
        localPack := filepath.Join(cacheDir, pack.Name)
        if _, err := os.Stat(localPack); err != nil {
            logx.Info("porta", "downloading %s", pack.Name)
            if err := s.Storage.DownloadToFile(ctx, s.remoteKey(m.BackupID, pack.Name), localPack); err != nil {
                return err
            }
        }
        logx.Info("porta", "extracting %s", pack.Name)
        if err := archive.ExtractPack(localPack, destDir, wanted); err != nil {
            return err
        }
    }
    logx.Info("porta", "restore complete %s", m.BackupID)
    return nil
}

func (s *Service) ShouldCreateNewBackup(now time.Time) (bool, error) {
    last, err := s.lastSuccessfulTime()
    if err != nil {
        return true, nil
    }
    return !now.Before(s.Cfg.Backup.Interval.NextFrom(last)), nil
}

func (s *Service) lastSuccessfulTime() (time.Time, error) {
    manifests, err := s.List(context.Background())
    if err == nil && len(manifests) > 0 {
        return manifests[0].CreatedAt, nil
    }
    entries, err := os.ReadDir(s.SpoolRoot)
    if err != nil {
        return time.Time{}, err
    }
    latest := time.Time{}
    for _, e := range entries {
        if !e.IsDir() {
            continue
        }
        st, err := s.loadState(filepath.Join(s.SpoolRoot, e.Name()))
        if err != nil {
            continue
        }
        if st.Status == StatusComplete && st.CompletedAt != nil && st.CompletedAt.After(latest) {
            latest = *st.CompletedAt
        }
    }
    if latest.IsZero() {
        return time.Time{}, fmt.Errorf("no successful backup found")
    }
    return latest, nil
}

func (s *Service) ReconcileSpool(now time.Time) {
    entries, err := os.ReadDir(s.SpoolRoot)
    if err != nil {
        return
    }
    for _, e := range entries {
        if !e.IsDir() {
            continue
        }
        jobDir := filepath.Join(s.SpoolRoot, e.Name())
        _, stateErr := os.Stat(filepath.Join(jobDir, "state.json"))
        _, manifestErr := os.Stat(filepath.Join(jobDir, "manifest.draft.json"))
        if stateErr != nil && manifestErr != nil {
            logx.Info("porta", "removing broken backup directory %s", e.Name())
            _ = os.RemoveAll(jobDir)
            continue
        }
        st, err := s.loadState(jobDir)
        if err != nil {
            if s.isOlderThan(jobDir, now, s.Cfg.Backup.BrokenAfter) {
                logx.Info("porta", "removing broken backup directory %s", e.Name())
                _ = os.RemoveAll(jobDir)
            }
            continue
        }
        if manifestErr != nil {
            st.Status = StatusBroken
            st.LastError = "missing manifest.draft.json"
            st.UpdatedAt = time.Now().UTC()
            _ = s.saveState(jobDir, st)
        }
        if st.Status == StatusBroken && s.isOlderThan(jobDir, now, s.Cfg.Backup.BrokenAfter) {
            logx.Info("porta", "cleaning old broken backup %s", e.Name())
            _ = os.RemoveAll(jobDir)
        }
    }
    s.enforceRetention()
}

func (s *Service) enforceRetention() {
    if s.Cfg.Backup.RetentionLocal <= 0 {
        return
    }
    entries, err := os.ReadDir(s.SpoolRoot)
    if err != nil {
        return
    }
    type item struct {
        name string
        t time.Time
    }
    items := make([]item, 0)
    for _, e := range entries {
        if !e.IsDir() {
            continue
        }
        st, err := s.loadState(filepath.Join(s.SpoolRoot, e.Name()))
        if err != nil {
            continue
        }
        if st.Status == StatusBroken {
            continue
        }
        items = append(items, item{name: e.Name(), t: st.CreatedAt})
    }
    sort.Slice(items, func(i,j int) bool { return items[i].t.After(items[j].t) })
    for i := s.Cfg.Backup.RetentionLocal; i < len(items); i++ {
        _ = os.RemoveAll(filepath.Join(s.SpoolRoot, items[i].name))
    }
}

func (s *Service) handleBrokenDirectory(jobDir, backupID string, cause error) error {
    st, _ := s.loadState(jobDir)
    if st.BackupID == "" {
        st.BackupID = backupID
        st.Scope = s.Scope.Name
        st.CreatedAt = time.Now().UTC()
    }
    st.Status = StatusBroken
    st.LastError = cause.Error()
    st.UpdatedAt = time.Now().UTC()
    _ = s.saveState(jobDir, st)
    return cause
}

func (s *Service) isOlderThan(jobDir string, now time.Time, spec config.IntervalSpec) bool {
    st, err := os.Stat(jobDir)
    if err != nil {
        return false
    }
    threshold := spec.NextFrom(st.ModTime().UTC())
    return !now.Before(threshold)
}

func (s *Service) fetchManifest(ctx context.Context, backupID string) (*Manifest, error) {
    if backupID == "" || backupID == "latest" {
        list, err := s.List(ctx)
        if err != nil {
            return nil, err
        }
        if len(list) == 0 {
            return nil, fmt.Errorf("no backups found")
        }
        m := list[0]
        return &m, nil
    }
    b, err := s.Storage.ReadAll(ctx, s.remoteKey(backupID, "manifest.json"))
    if err != nil {
        return nil, err
    }
    var m Manifest
    if err := json.Unmarshal(b, &m); err != nil {
        return nil, err
    }
    return &m, nil
}

func (s *Service) loadOrCreateManifest(path, backupID string) (*Manifest, error) {
    if b, err := os.ReadFile(path); err == nil {
        var m Manifest
        if err := json.Unmarshal(b, &m); err != nil {
            return nil, err
        }
        return &m, nil
    }

    files, err := archive.ScanFiles(s.Scope.Source, s.Scope.Include, s.Scope.Exclude)
    if err != nil {
        return nil, err
    }
    packs := archive.BuildPacks(files, s.Scope.ChunkSizeBytes)
    m := &Manifest{
        Version: 1,
        Scope: s.Scope.Name,
        Source: s.Scope.Source,
        BackupID: backupID,
        CreatedAt: time.Now().UTC(),
        ChunkSizeBytes: s.Scope.ChunkSizeBytes,
        Include: append([]string{}, s.Scope.Include...),
        Exclude: append([]string{}, s.Scope.Exclude...),
        Files: files,
        Packs: packs,
        Status: string(StatusReady),
    }
    return m, s.saveManifest(path, m)
}

func (s *Service) saveManifest(path string, m *Manifest) error {
    b, err := json.MarshalIndent(m, "", "  ")
    if err != nil {
        return err
    }
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, b, 0o644); err != nil {
        return err
    }
    return os.Rename(tmp, path)
}

func (s *Service) loadState(jobDir string) (State, error) {
    b, err := os.ReadFile(filepath.Join(jobDir, "state.json"))
    if err != nil {
        return State{}, err
    }
    var st State
    if err := json.Unmarshal(b, &st); err != nil {
        return State{}, err
    }
    return st, nil
}

func (s *Service) saveState(jobDir string, st State) error {
    b, err := json.MarshalIndent(st, "", "  ")
    if err != nil {
        return err
    }
    tmp := filepath.Join(jobDir, "state.json.tmp")
    if err := os.WriteFile(tmp, b, 0o644); err != nil {
        return err
    }
    return os.Rename(tmp, filepath.Join(jobDir, "state.json"))
}

func (s *Service) RemoteAvailable(ctx context.Context) bool {
    _, err := s.Storage.List(ctx, s.remoteRoot(), false)
    return err == nil
}

func packContainsFile(pack archive.Pack, file string) bool {
    for _, f := range pack.Files {
        if f.Path == file {
            return true
        }
    }
    return false
}
