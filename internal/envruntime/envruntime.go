package envruntime

import (
    "bufio"
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strings"

    "github.com/rafalmasiarek/porta/internal/crypto"
)

func DetectEncryptedEnv(root string) string {
    candidates := []string{
        filepath.Join(root, ".porta.enc"),
        filepath.Join(root, "config.enc"),
    }
    for _, p := range candidates {
        if _, err := os.Stat(p); err == nil {
            return p
        }
    }
    return ""
}

func Load(root string, privateKeyPath string) (map[string]string, error) {
    path := DetectEncryptedEnv(root)
    if path == "" {
        return map[string]string{}, nil
    }
    content, err := crypto.DecryptToString(path, privateKeyPath)
    if err != nil {
        return nil, err
    }
    return ParseEnv(content)
}

func ParseEnv(content string) (map[string]string, error) {
    values := map[string]string{}
    scanner := bufio.NewScanner(strings.NewReader(content))
    lineNum := 0
    for scanner.Scan() {
        lineNum++
        line := strings.TrimSpace(scanner.Text())
        if line == "" || strings.HasPrefix(line, "#") {
            continue
        }
        parts := strings.SplitN(line, "=", 2)
        if len(parts) != 2 {
            return nil, fmt.Errorf("invalid env line %d", lineNum)
        }
        key := strings.TrimSpace(parts[0])
        value := strings.TrimSpace(parts[1])
        value = strings.Trim(value, `"'`)
        values[key] = value
    }
    if err := scanner.Err(); err != nil {
        return nil, err
    }
    return values, nil
}

func Merge(runtime map[string]string) []string {
    merged := map[string]string{}
    for _, item := range os.Environ() {
        parts := strings.SplitN(item, "=", 2)
        if len(parts) == 2 {
            merged[parts[0]] = parts[1]
        }
    }
    for k, v := range runtime {
        merged[k] = v
    }
    keys := make([]string, 0, len(merged))
    for k := range merged {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    out := make([]string, 0, len(keys))
    for _, k := range keys {
        out = append(out, k+"="+merged[k])
    }
    return out
}

func Resolve(name string, runtime map[string]string) string {
    if v, ok := runtime[name]; ok {
        return v
    }
    return os.Getenv(name)
}
