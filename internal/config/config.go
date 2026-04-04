package config

import (
    "bufio"
    "fmt"
    "os"
    "path/filepath"
    "strconv"
    "strings"
    "time"

    "github.com/rafalmasiarek/porta/internal/envruntime"
)

type Config struct {
    Version    int
    Autorun    bool
    Storage    Storage
    Backup     Backup
    Hooks      Hooks
    Jobs       []Job
    RuntimeEnv map[string]string
}

type Storage struct {
    Bucket    string
    Prefix    string
    Endpoint  string
    Region    string
    AccessKey string
    SecretKey string
    UseSSL    *bool
    PathStyle bool
}

type Backup struct {
    Source             string
    IntervalRaw        string
    Interval           IntervalSpec
    ChunkSizeMB        int
    RetentionLocal     int
    BrokenCleanupAfter string
    BrokenAfter        IntervalSpec
    Include            []string
    Exclude            []string
}

type Hook struct {
    Command string
    OS      string
}

type Hooks struct {
    OnAttach []Hook
    OnDetach []Hook
}

type Job struct {
    Name       string
    Command    string
    Mode       string
    RunOn      string
    LogFile    string
    WorkingDir string
    OS         string
}

type IntervalSpec struct {
    Raw    string
    Unit   string
    Amount int
}

func DefaultBackup() Backup {
    return Backup{
        Source:             ".",
        IntervalRaw:        "12h",
        ChunkSizeMB:        50,
        RetentionLocal:     3,
        BrokenCleanupAfter: "24h",
        Include:            []string{},
        Exclude: []string{
            ".porta/**",
            ".git/**",
            ".porta.enc",
            "config.enc",
            ".DS_Store",
            "._*",
            ".Spotlight-V100/**",
            ".Trashes/**",
            ".fseventsd/**",
        },
    }
}

func FindConfig(start string) (string, error) {
    cur := start
    for {
        for _, name := range []string{"porta.yml", ".porta.yml"} {
            candidate := filepath.Join(cur, name)
            if _, err := os.Stat(candidate); err == nil {
                return candidate, nil
            }
        }
        parent := filepath.Dir(cur)
        if parent == cur {
            return "", fmt.Errorf("porta.yml not found from %s", start)
        }
        cur = parent
    }
}

func Load(configPath string, runtime map[string]string) (*Config, string, error) {
    path := configPath
    if path == "" {
        cwd, err := os.Getwd()
        if err != nil {
            return nil, "", err
        }
        found, err := FindConfig(cwd)
        if err != nil {
            return nil, "", err
        }
        path = found
    }

    cfg := &Config{
        Version:    1,
        Backup:     DefaultBackup(),
        RuntimeEnv: runtime,
    }

    f, err := os.Open(path)
    if err != nil {
        return nil, "", err
    }
    defer f.Close()

    root := filepath.Dir(path)

    var section string
    var sub string
    var currentJob *Job
    var currentHook *Hook

    scanner := bufio.NewScanner(f)

    for scanner.Scan() {
        line := scanner.Text()
        trimmed := strings.TrimSpace(line)

        if trimmed == "" || strings.HasPrefix(trimmed, "#") {
            continue
        }

        indent := len(line) - len(strings.TrimLeft(line, " "))

        if indent == 0 {
            currentJob = nil
            currentHook = nil
            sub = ""

            if strings.HasSuffix(trimmed, ":") {
                section = strings.TrimSuffix(trimmed, ":")
                continue
            }

            key, val, ok := parseKeyValue(trimmed)
            if !ok {
                return nil, "", fmt.Errorf("invalid line: %s", line)
            }
            applyRoot(cfg, key, val)
            continue
        }

        switch section {
        case "storage":
            key, val, ok := parseKeyValue(trimmed)
            if !ok {
                return nil, "", fmt.Errorf("invalid storage line: %s", line)
            }
            applyStorage(&cfg.Storage, key, val)

        case "backup":
            if strings.HasSuffix(trimmed, ":") {
                sub = strings.TrimSuffix(trimmed, ":")
                continue
            }

            if strings.HasPrefix(trimmed, "- ") {
                value := interpolate(unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))), runtime)

                switch sub {
                case "include":
                    cfg.Backup.Include = append(cfg.Backup.Include, value)
                case "exclude":
                    cfg.Backup.Exclude = append(cfg.Backup.Exclude, value)
                default:
                    return nil, "", fmt.Errorf("unknown backup list %q", sub)
                }
                continue
            }

            key, val, ok := parseKeyValue(trimmed)
            if !ok {
                return nil, "", fmt.Errorf("invalid backup line: %s", line)
            }
            applyBackup(&cfg.Backup, key, val)

        case "hooks":
            if strings.HasSuffix(trimmed, ":") {
                sub = strings.TrimSuffix(trimmed, ":")
                continue
            }

            if strings.HasPrefix(trimmed, "- ") {
                val := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))

                // Backward-compatible plain string hook:
                // - "echo hello"
                if !strings.Contains(val, ":") {
                    hook := Hook{
                        Command: interpolate(unquote(val), runtime),
                        OS:      "all",
                    }

                    switch sub {
                    case "on_attach":
                        cfg.Hooks.OnAttach = append(cfg.Hooks.OnAttach, hook)
                        currentHook = &cfg.Hooks.OnAttach[len(cfg.Hooks.OnAttach)-1]
                    case "on_detach":
                        cfg.Hooks.OnDetach = append(cfg.Hooks.OnDetach, hook)
                        currentHook = &cfg.Hooks.OnDetach[len(cfg.Hooks.OnDetach)-1]
                    default:
                        return nil, "", fmt.Errorf("unknown hooks list %q", sub)
                    }
                    continue
                }

                // Structured hook item:
                // - command: "..."
                hook := Hook{OS: "all"}

                switch sub {
                case "on_attach":
                    cfg.Hooks.OnAttach = append(cfg.Hooks.OnAttach, hook)
                    currentHook = &cfg.Hooks.OnAttach[len(cfg.Hooks.OnAttach)-1]
                case "on_detach":
                    cfg.Hooks.OnDetach = append(cfg.Hooks.OnDetach, hook)
                    currentHook = &cfg.Hooks.OnDetach[len(cfg.Hooks.OnDetach)-1]
                default:
                    return nil, "", fmt.Errorf("unknown hooks list %q", sub)
                }

                key, val, ok := parseKeyValue(val)
                if !ok {
                    return nil, "", fmt.Errorf("invalid hook line: %s", line)
                }
                applyHook(currentHook, key, val)
                continue
            }

            if currentHook == nil {
                return nil, "", fmt.Errorf("hook property without list item: %s", line)
            }

            key, val, ok := parseKeyValue(trimmed)
            if !ok {
                return nil, "", fmt.Errorf("invalid hook property: %s", line)
            }
            applyHook(currentHook, key, val)

        case "jobs":
            if strings.HasPrefix(trimmed, "- ") {
                cfg.Jobs = append(cfg.Jobs, Job{
                    Mode:  "foreground",
                    RunOn: "attach",
                    OS:    "all",
                })
                currentJob = &cfg.Jobs[len(cfg.Jobs)-1]

                rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
                if rest != "" {
                    key, val, ok := parseKeyValue(rest)
                    if !ok {
                        return nil, "", fmt.Errorf("invalid job line: %s", line)
                    }
                    applyJob(currentJob, key, val)
                }
                continue
            }

            if currentJob == nil {
                return nil, "", fmt.Errorf("job property without list item: %s", line)
            }

            key, val, ok := parseKeyValue(trimmed)
            if !ok {
                return nil, "", fmt.Errorf("invalid job property: %s", line)
            }
            applyJob(currentJob, key, val)
        }
    }

    if err := scanner.Err(); err != nil {
        return nil, "", err
    }

    cfg.Backup.Source = expand(root, cfg.Backup.Source, runtime)

    if cfg.Backup.ChunkSizeMB <= 0 {
        cfg.Backup.ChunkSizeMB = 50
    }
    if cfg.Backup.RetentionLocal <= 0 {
        cfg.Backup.RetentionLocal = 3
    }

    interval, err := ParseInterval(cfg.Backup.IntervalRaw)
    if err != nil {
        return nil, "", fmt.Errorf("backup.interval: %w", err)
    }
    cfg.Backup.Interval = interval

    broken, err := ParseInterval(cfg.Backup.BrokenCleanupAfter)
    if err != nil {
        return nil, "", fmt.Errorf("backup.broken_cleanup_after: %w", err)
    }
    cfg.Backup.BrokenAfter = broken

    for i := range cfg.Jobs {
        cfg.Jobs[i].WorkingDir = expand(root, cfg.Jobs[i].WorkingDir, runtime)
        if cfg.Jobs[i].WorkingDir == "" {
            cfg.Jobs[i].WorkingDir = root
        }
        if cfg.Jobs[i].LogFile != "" {
            cfg.Jobs[i].LogFile = expand(root, cfg.Jobs[i].LogFile, runtime)
        }
        if cfg.Jobs[i].OS == "" {
            cfg.Jobs[i].OS = "all"
        }
    }

    for i := range cfg.Hooks.OnAttach {
        cfg.Hooks.OnAttach[i].Command = interpolate(cfg.Hooks.OnAttach[i].Command, runtime)
        if cfg.Hooks.OnAttach[i].OS == "" {
            cfg.Hooks.OnAttach[i].OS = "all"
        }
    }
    for i := range cfg.Hooks.OnDetach {
        cfg.Hooks.OnDetach[i].Command = interpolate(cfg.Hooks.OnDetach[i].Command, runtime)
        if cfg.Hooks.OnDetach[i].OS == "" {
            cfg.Hooks.OnDetach[i].OS = "all"
        }
    }

    cfg.Storage.Endpoint = interpolate(cfg.Storage.Endpoint, runtime)
    cfg.Storage.Region = interpolate(cfg.Storage.Region, runtime)
    cfg.Storage.AccessKey = interpolate(cfg.Storage.AccessKey, runtime)
    cfg.Storage.SecretKey = interpolate(cfg.Storage.SecretKey, runtime)
    cfg.Storage.Bucket = interpolate(cfg.Storage.Bucket, runtime)
    cfg.Storage.Prefix = interpolate(cfg.Storage.Prefix, runtime)

    if cfg.Storage.Region == "" {
        cfg.Storage.Region = "us-east-1"
    }

    return cfg, root, nil
}

func ParseInterval(raw string) (IntervalSpec, error) {
    raw = strings.TrimSpace(raw)
    if raw == "" {
        return IntervalSpec{}, fmt.Errorf("value is empty")
    }

    units := []string{"mo", "y", "w", "d", "h", "m"}
    for _, unit := range units {
        if strings.HasSuffix(raw, unit) {
            num := strings.TrimSuffix(raw, unit)
            amount, err := strconv.Atoi(num)
            if err != nil || amount <= 0 {
                return IntervalSpec{}, fmt.Errorf("invalid interval %q", raw)
            }
            return IntervalSpec{
                Raw:    raw,
                Unit:   unit,
                Amount: amount,
            }, nil
        }
    }

    return IntervalSpec{}, fmt.Errorf("unsupported interval %q", raw)
}

func (i IntervalSpec) NextFrom(t time.Time) time.Time {
    switch i.Unit {
    case "m":
        return t.Add(time.Duration(i.Amount) * time.Minute)
    case "h":
        return t.Add(time.Duration(i.Amount) * time.Hour)
    case "d":
        return t.AddDate(0, 0, i.Amount)
    case "w":
        return t.AddDate(0, 0, 7*i.Amount)
    case "mo":
        return t.AddDate(0, i.Amount, 0)
    case "y":
        return t.AddDate(i.Amount, 0, 0)
    default:
        return t
    }
}

func applyRoot(cfg *Config, key, val string) {
    val = unquote(val)

    switch key {
    case "version":
        v, _ := strconv.Atoi(val)
        cfg.Version = v
    case "autorun":
        cfg.Autorun = parseBool(val)
    }
}

func applyStorage(st *Storage, key, val string) {
    val = unquote(val)

    switch key {
    case "bucket":
        st.Bucket = val
    case "prefix":
        st.Prefix = val
    case "endpoint":
        st.Endpoint = val
    case "region":
        st.Region = val
    case "access_key_id":
        st.AccessKey = val
    case "secret_access_key":
        st.SecretKey = val
    case "path_style":
        st.PathStyle = parseBool(val)
    case "use_ssl":
        b := parseBool(val)
        st.UseSSL = &b
    }
}

func applyBackup(b *Backup, key, val string) {
    val = unquote(val)

    switch key {
    case "source":
        b.Source = val
    case "interval":
        b.IntervalRaw = val
    case "chunk_size_mb":
        n, _ := strconv.Atoi(val)
        b.ChunkSizeMB = n
    case "retention_local":
        n, _ := strconv.Atoi(val)
        b.RetentionLocal = n
    case "broken_cleanup_after":
        b.BrokenCleanupAfter = val
    }
}

func applyHook(h *Hook, key, val string) {
    val = unquote(val)

    switch key {
    case "command":
        h.Command = val
    case "os":
        h.OS = val
    }
}

func applyJob(j *Job, key, val string) {
    val = unquote(val)

    switch key {
    case "name":
        j.Name = val
    case "command":
        j.Command = val
    case "mode":
        j.Mode = val
    case "run_on":
        j.RunOn = val
    case "log_file":
        j.LogFile = val
    case "working_dir":
        j.WorkingDir = val
    case "os":
        j.OS = val
    }
}

func parseKeyValue(s string) (string, string, bool) {
    parts := strings.SplitN(s, ":", 2)
    if len(parts) != 2 {
        return "", "", false
    }
    return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func parseBool(v string) bool {
    v = strings.ToLower(strings.TrimSpace(v))
    return v == "true" || v == "yes" || v == "1"
}

func unquote(s string) string {
    s = strings.TrimSpace(s)
    if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
        return s[1 : len(s)-1]
    }
    return s
}

func expand(root, p string, runtime map[string]string) string {
    if p == "" {
        return p
    }
    p = interpolate(p, runtime)
    if filepath.IsAbs(p) {
        return p
    }
    return filepath.Join(root, p)
}

func interpolate(s string, runtime map[string]string) string {
    return os.Expand(s, func(name string) string {
        return envruntime.Resolve(name, runtime)
    })
}