package archive

import (
    "archive/tar"
    "compress/gzip"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "time"

    "github.com/rafalmasiarek/porta/internal/filter"
)

type FileEntry struct {
    Path    string    `json:"path"`
    Size    int64     `json:"size"`
    Mode    int64     `json:"mode"`
    ModTime time.Time `json:"mod_time"`
}

type Pack struct {
    Name  string      `json:"name"`
    Files []FileEntry `json:"files"`
}

func ScanFiles(root string, include, exclude []string) ([]FileEntry, error) {
    files := make([]FileEntry, 0)
    err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
        if err != nil {
            if os.IsPermission(err) {
                if info != nil && info.IsDir() {
                    return filepath.SkipDir
                }
                return nil
            }
            return err
        }
        rel, err := filepath.Rel(root, p)
        if err != nil {
            return err
        }
        rel = filepath.ToSlash(rel)
        if rel == "." {
            return nil
        }
        if info.IsDir() {
            if shouldSkipDir(rel, include, exclude) {
                return filepath.SkipDir
            }
            return nil
        }
        if !filter.Match(rel, include, exclude) {
            return nil
        }
        files = append(files, FileEntry{
            Path: rel,
            Size: info.Size(),
            Mode: int64(info.Mode()),
            ModTime: info.ModTime().UTC(),
        })
        return nil
    })
    if err != nil {
        return nil, err
    }
    sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
    return files, nil
}

func shouldSkipDir(rel string, include, exclude []string) bool {
    rel = filepath.ToSlash(rel)
    base := filepath.Base(rel)
    for _, pattern := range exclude {
        pattern = filepath.ToSlash(strings.TrimSpace(pattern))
        if strings.HasSuffix(pattern, "/**") {
            prefix := strings.TrimSuffix(pattern, "/**")
            if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
                return true
            }
        }
        if ok, _ := filepath.Match(pattern, rel); ok {
            return true
        }
        if ok, _ := filepath.Match(pattern, base); ok {
            return true
        }
    }
    if len(include) == 0 {
        return false
    }
    for _, pattern := range include {
        pattern = filepath.ToSlash(pattern)
        if strings.HasSuffix(pattern, "/**") {
            prefix := strings.TrimSuffix(pattern, "/**")
            if rel == prefix || strings.HasPrefix(prefix, rel+"/") || strings.HasPrefix(rel, prefix+"/") {
                return false
            }
        }
        if ok, _ := filepath.Match(pattern, rel); ok {
            return false
        }
        if strings.HasPrefix(pattern, rel+"/") {
            return false
        }
    }
    return true
}

func BuildPacks(files []FileEntry, chunkSizeBytes int64) []Pack {
    packs := make([]Pack, 0)
    if chunkSizeBytes <= 0 {
        chunkSizeBytes = 50 * 1024 * 1024
    }
    var current Pack
    var currentSize int64
    packNum := 1

    flush := func() {
        if len(current.Files) == 0 {
            return
        }
        current.Name = fmt.Sprintf("part-%05d.tgz", packNum)
        packs = append(packs, current)
        packNum++
        current = Pack{}
        currentSize = 0
    }

    for _, f := range files {
        if currentSize > 0 && currentSize+f.Size > chunkSizeBytes {
            flush()
        }
        current.Files = append(current.Files, f)
        currentSize += f.Size
    }
    flush()
    return packs
}

func CreatePack(root string, pack Pack, outputPath string) error {
    tmp := outputPath + ".tmp"
    if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
        return err
    }
    f, err := os.Create(tmp)
    if err != nil {
        return err
    }
    defer f.Close()

    gz := gzip.NewWriter(f)
    tw := tar.NewWriter(gz)

    for _, entry := range pack.Files {
        fullPath := filepath.Join(root, filepath.FromSlash(entry.Path))
        src, err := os.Open(fullPath)
        if err != nil {
            return err
        }
        hdr := &tar.Header{
            Name: entry.Path,
            Size: entry.Size,
            Mode: entry.Mode,
            ModTime: entry.ModTime,
        }
        if err := tw.WriteHeader(hdr); err != nil {
            src.Close()
            return err
        }
        if _, err := io.Copy(tw, src); err != nil {
            src.Close()
            return err
        }
        src.Close()
    }

    if err := tw.Close(); err != nil {
        return err
    }
    if err := gz.Close(); err != nil {
        return err
    }
    if err := f.Close(); err != nil {
        return err
    }
    return os.Rename(tmp, outputPath)
}

func ExtractPack(packPath, destDir string, wanted map[string]bool) error {
    f, err := os.Open(packPath)
    if err != nil {
        return err
    }
    defer f.Close()

    gz, err := gzip.NewReader(f)
    if err != nil {
        return err
    }
    defer gz.Close()

    tr := tar.NewReader(gz)
    for {
        hdr, err := tr.Next()
        if err == io.EOF {
            return nil
        }
        if err != nil {
            return err
        }
        name := filepath.ToSlash(strings.TrimPrefix(hdr.Name, "./"))
        if len(wanted) > 0 && !wanted[name] {
            if _, err := io.Copy(io.Discard, tr); err != nil {
                return err
            }
            continue
        }
        out := filepath.Join(destDir, filepath.FromSlash(name))
        if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
            return err
        }
        of, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
        if err != nil {
            return err
        }
        if _, err := io.Copy(of, tr); err != nil {
            of.Close()
            return err
        }
        of.Close()
        _ = os.Chtimes(out, time.Now(), hdr.ModTime)
    }
}
