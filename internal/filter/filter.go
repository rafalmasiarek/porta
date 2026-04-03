package filter

import (
    "path/filepath"
    "strings"
)

func Match(rel string, include, exclude []string) bool {
    rel = filepath.ToSlash(strings.TrimPrefix(rel, "./"))
    base := filepath.Base(rel)

    if len(include) > 0 {
        matched := false
        for _, pattern := range include {
            if matchOne(rel, base, pattern) {
                matched = true
                break
            }
        }
        if !matched {
            return false
        }
    }

    for _, pattern := range exclude {
        if matchOne(rel, base, pattern) {
            return false
        }
    }
    return true
}

func matchOne(rel, base, pattern string) bool {
    pattern = filepath.ToSlash(strings.TrimSpace(pattern))
    if pattern == "" {
        return false
    }

    if strings.HasSuffix(pattern, "/**") {
        prefix := strings.TrimSuffix(pattern, "/**")
        return rel == prefix || strings.HasPrefix(rel, prefix+"/")
    }
    if ok, _ := filepath.Match(pattern, rel); ok {
        return true
    }
    if ok, _ := filepath.Match(pattern, base); ok {
        return true
    }
    return false
}
