//go:build darwin

package agent

import (
    "context"
    "os"
    "path/filepath"
    "sort"
)

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
                out = append(out, MountedVolume{
                    Root:       root,
                    ConfigPath: cfg,
                })
                break
            }
        }
    }

    sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
    return out, nil
}

func watchVolumeChanges(ctx context.Context, trigger chan<- struct{}) error {
    <-ctx.Done()
    return nil
}